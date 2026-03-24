package facebook

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/playwright-community/playwright-go"
)

// Store defines the Firestore operations needed by the Facebook processor.
type Store interface {
	GetFacebookSubscriptions(ctx context.Context) ([]models.Subscription, error)
	FacebookAdExists(ctx context.Context, listingID string) (bool, error)
	SaveFacebookAd(ctx context.Context, ad *models.FacebookAdRecord) (bool, error)
	PruneFacebookAds(ctx context.Context, maxAgeMonths int, maxRecords int) error
	SavePriceHistory(ctx context.Context, model string, value float64) error
	IsProxyBlocked(ctx context.Context, ip string) (bool, error)
	BlockProxyIP(ctx context.Context, ip, city string) error
	GetCarfaxCache(ctx context.Context, year int, make, model, trim, postalPrefix string) (*storage.CarfaxCacheEntry, error)
	SaveCarfaxCache(ctx context.Context, entry *storage.CarfaxCacheEntry) error
	GetCarfaxOptions(ctx context.Context, property, normalizedParams string) ([]string, error)
	SaveCarfaxOptions(ctx context.Context, property, normalizedParams string, options []string) error
	GetTokenServiceURL(ctx context.Context) (string, error)
}

// Notifier defines the Discord notification operations.
type Notifier interface {
	SendFacebookDeal(ctx context.Context, title, url, summary, knownIssues string, askingPrice, carfaxValue, vmrRetail float64, isWarm, isLavaHot bool, subs []models.Subscription) error
}

// Processor handles Facebook Marketplace deal scraping and analysis.
type Processor struct {
	store                    Store
	notifier                 Notifier
	ai                       AIClient
	proxyURL                 string
	carfaxTokenServiceURL    string
	carfaxTokenServiceSecret string
}

// NewProcessor creates a new Facebook deal processor.
// proxyURL is optional — if empty, scraping runs without a proxy.
// carfaxTokenServiceURL/Secret are optional — if set, Carfax valuations use
// direct HTTP API calls with tokens from the remote service (much faster and
// more reliable). If not set, falls back to Playwright UI automation.
func NewProcessor(store Store, notifier Notifier, ai AIClient, proxyURL, carfaxTokenServiceURL, carfaxTokenServiceSecret string) *Processor {
	return &Processor{
		store:                    store,
		notifier:                 notifier,
		ai:                       ai,
		proxyURL:                 proxyURL,
		carfaxTokenServiceURL:    carfaxTokenServiceURL,
		carfaxTokenServiceSecret: carfaxTokenServiceSecret,
	}
}

// cityGroup holds subscriptions grouped by city.
type cityGroup struct {
	city     string
	category string
	radiusKm int
	postal   string
	brands   map[string]bool
	subs     []models.Subscription
}

// ProcessFacebookDeals is the main entry point for Facebook deal processing.
func (p *Processor) ProcessFacebookDeals(ctx context.Context) error {
	scrapeStart := time.Now()
	slog.Info("Starting Facebook deal processing", "processor", "facebook")

	tracker := metrics.NewTracker("facebook")
	defer tracker.LogSummary()

	// Load subscriptions
	subs, err := p.store.GetFacebookSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("failed to load facebook subscriptions: %w", err)
	}

	if len(subs) == 0 {
		slog.Info("No active Facebook subscriptions found", "processor", "facebook")
		return nil
	}

	// Initialize Playwright
	pm, err := NewBrowserManager(slog.Default(), p.proxyURL)
	if err != nil {
		return fmt.Errorf("failed to init playwright: %w", err)
	}
	defer pm.Close()

	// Initialize Carfax valuation client.
	// Resolve the token service URL dynamically from Firestore first (updated by
	// the tunnel script), falling back to the static env var. This avoids stale
	// URLs when the Cloudflare quick tunnel restarts and gets a new hostname.
	tokenServiceURL := p.carfaxTokenServiceURL
	if dynamicURL, err := p.store.GetTokenServiceURL(ctx); err != nil {
		slog.Warn("Failed to fetch dynamic token service URL, using static config",
			"processor", "facebook", "component", "carfax_http",
			"error", err, "static_url", p.carfaxTokenServiceURL)
	} else if dynamicURL != "" {
		tokenServiceURL = dynamicURL
		slog.Info("Using dynamic token service URL from Firestore",
			"processor", "facebook", "component", "carfax_http",
			"url", dynamicURL)
	}

	var carfaxClient CarfaxValuer
	if tokenServiceURL != "" {
		tokenClient := NewCarfaxTokenClient(tokenServiceURL, p.carfaxTokenServiceSecret)
		carfaxClient = NewCarfaxHTTPClient(tokenClient, p.proxyURL, p.store)
		slog.Info("Using Carfax HTTP client with token service",
			"processor", "facebook", "url", tokenServiceURL)
	} else {
		carfaxClient = NewCarfaxClient(pm)
		slog.Info("Using Carfax Playwright client (no token service configured)", "processor", "facebook")
	}

	// Group subscriptions by city
	groups := groupByCity(subs)
	slog.Info("Starting scrape cycle", "processor", "facebook", "subscriptions", len(subs), "cities", len(groups))

	for i, group := range groups {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i > 0 {
			randomDelay(3*time.Second, 6*time.Second)
		}
		p.processCity(ctx, group, carfaxClient, pm, tracker)
	}

	// Prune old ads — use a separate context so pruning isn't starved for time
	// when the scraping phase consumes most of the parent context's deadline.
	pruneCtx, pruneCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer pruneCancel()
	if err := p.store.PruneFacebookAds(pruneCtx, 6, 1000); err != nil {
		slog.Warn("Failed to prune old facebook ads", "processor", "facebook", "error", err)
	}

	slog.Info("Facebook scrape cycle complete", "processor", "facebook",
		"duration_ms", time.Since(scrapeStart).Milliseconds(),
		"cities", len(groups))
	return nil
}

func groupByCity(subs []models.Subscription) []cityGroup {
	m := make(map[string]*cityGroup)

	for _, sub := range subs {
		g, ok := m[sub.City]
		if !ok {
			g = &cityGroup{
				city:     sub.City,
				category: "Vehicles",
				radiusKm: sub.RadiusKm,
				postal:   PostalCodeForCity(sub.City),
				brands:   make(map[string]bool),
			}
			m[sub.City] = g
		}

		if sub.RadiusKm > g.radiusKm {
			g.radiusKm = sub.RadiusKm
		}

		if len(sub.FilterBrands) == 0 {
			g.brands = nil
		}
		if g.brands != nil {
			for _, b := range sub.FilterBrands {
				g.brands[b] = true
			}
		}

		g.subs = append(g.subs, sub)
	}

	groups := make([]cityGroup, 0, len(m))
	for _, g := range m {
		groups = append(groups, *g)
	}
	return groups
}

func (p *Processor) processCity(ctx context.Context, group cityGroup, carfaxClient CarfaxValuer, pm *BrowserManager, tracker *metrics.Tracker) {
	slog.Info("Processing city", "processor", "facebook", "city", group.city, "subscribers", len(group.subs))

	cfg := &FacebookScrapeConfig{
		City:     group.city,
		Category: group.category,
		RadiusKm: group.radiusKm,
	}

	// Scrape marketplace
	ads, err := ScrapeMarketplace(ctx, slog.Default(), pm, cfg, p.store)
	if err != nil {
		if isTransientError(err) {
			slog.Warn("Failed to scrape marketplace (transient)", "processor", "facebook", "city", group.city, "error", err)
		} else {
			slog.Error("Failed to scrape marketplace", "processor", "facebook", "city", group.city, "error", err)
		}
		return
	}

	if len(ads) == 0 {
		slog.Info("No ads found", "processor", "facebook", "city", group.city)
		return
	}

	tracker.TrackAdsScraped(len(ads))
	slog.Info("Processing ads", "processor", "facebook", "city", group.city, "count", len(ads))

	// Create a reusable page for listing details
	detailCtx, detailErr := pm.NewContext(group.city)
	if detailErr != nil {
		slog.Warn("Failed to create detail browser context", "processor", "facebook", "error", detailErr)
	}
	var detailPage playwright.Page
	if detailCtx != nil {
		defer detailCtx.Close()
		detailPage, detailErr = detailCtx.NewPage()
		if detailErr != nil {
			slog.Warn("Failed to create detail page", "processor", "facebook", "error", detailErr)
		}
	}

	carfaxFailures := 0
	const carfaxCircuitBreakerThreshold = 3

	cityStart := time.Now()
	dealsFound := 0
	adsProcessed := 0
	adsSkipped := 0

	for i, ad := range ads {
		if ctx.Err() != nil {
			return
		}

		// Scrape full description
		if detailPage != nil {
			if i > 0 {
				randomDelay(500*time.Millisecond, 1500*time.Millisecond)
			}
			desc, err := ScrapeListingDetail(ctx, slog.Default(), detailPage, ad.URL)
			if err != nil {
				slog.Debug("Failed to scrape listing detail", "processor", "facebook", "url", ad.URL, "error", err)
			} else if desc != "" {
				ad.Description = desc
			}
		}

		// Early dedup: check Firestore BEFORE the expensive NormalizeAd Gemini
		// call. If this listing ID was already processed (even if the previous
		// AnalyzeDeal failed), skip it to save AI quota.
		if ad.ListingID != "" {
			exists, err := p.store.FacebookAdExists(ctx, ad.ListingID)
			if err != nil {
				slog.Warn("Early dedup check failed", "processor", "facebook", "title", ad.Title, "error", err)
			} else if exists {
				slog.Debug("Skipping already-processed ad (early dedup)", "processor", "facebook", "title", ad.Title)
				adsSkipped++
				continue
			}
		}

		// Pre-filter: cheap keyword check on the ad title to skip obvious non-car
		// listings (boats, ATVs, trailers, motorcycles, etc.) BEFORE calling Gemini.
		// Without this, every ad—including "$500 lawn mower"—would burn a NormalizeAd
		// Gemini call just to learn it's not a car. The post-normalization
		// IsCarfaxEligible() check below is the authoritative filter; this is purely
		// a cost optimisation that catches the easy cases early.
		if isLikelyNonCar(ad.Title) {
			slog.Debug("Skipping likely non-car listing (keyword match)", "processor", "facebook", "title", ad.Title)
			adsSkipped++
			continue
		}

		// Pre-filter: skip ads that are too vague for Gemini to extract
		// meaningful vehicle data from. A single-word title with no year
		// digits and no description (e.g. "Bravo") will always make Gemini
		// return prose instead of JSON, wasting a call.
		if isTooVague(ad.Title, ad.Description, ad.Mileage) {
			slog.Debug("Skipping vague listing (insufficient info)", "processor", "facebook", "title", ad.Title)
			adsSkipped++
			continue
		}

		if isWantedAd(ad.Title, ad.Description) {
			slog.Debug("Skipping wanted/ISO ad", "processor", "facebook", "title", ad.Title)
			adsSkipped++
			continue
		}

		// Gemini Normalization
		randomDelay(100*time.Millisecond, 300*time.Millisecond)
		extraContext := ""
		if ad.Description != "" {
			extraContext = ad.Description + "\n"
		}
		if ad.Mileage != "" {
			extraContext += fmt.Sprintf("Mileage: %s. ", ad.Mileage)
		}
		if len(ad.Subtitles) > 1 {
			extraContext += fmt.Sprintf("Specs: %s", strings.Join(ad.Subtitles[1:], ", "))
		}

		carData, err := NormalizeAd(ctx, p.ai, ad.Title, extraContext)
		tracker.TrackGeminiCall(0, 0)
		if err != nil {
			slog.Error("Failed to normalize ad", "processor", "facebook", "title", ad.Title, "error", err)
			continue
		}
		tracker.TrackAdProcessed()
		adsProcessed++

		// Skip non-car vehicle types (motorcycles, boats, ATVs, trailers, etc.)
		if !carData.IsCarfaxEligible() {
			slog.Info("Skipping non-car vehicle", "processor", "facebook", "title", ad.Title, "type", carData.VehicleType)
			continue
		}

		// Skip ads where Gemini couldn't extract a valid year (avoids wasting Carfax/VMR calls)
		if carData.Year < 1900 || carData.Year > time.Now().Year()+2 {
			slog.Info("Skipping ad with invalid year", "processor", "facebook", "title", ad.Title, "year", carData.Year)
			continue
		}

		// Save to Firestore (deduplication by Facebook listing ID)
		adRecord := &models.FacebookAdRecord{
			ID:           ad.ListingID,
			Title:        ad.Title,
			Price:        fmt.Sprintf("%.0f", ad.Price),
			URL:          ad.URL,
			Year:         carData.Year,
			Make:         carData.Make,
			Model:        carData.Model,
			Mileage:      carData.Odometer,
			Transmission: carData.Transmission,
			Condition:    carData.Condition,
		}
		isNew, err := p.store.SaveFacebookAd(ctx, adRecord)
		if err != nil {
			slog.Error("Failed to save facebook ad", "processor", "facebook", "error", err)
			continue
		}
		if !isNew {
			slog.Debug("Skipping duplicate ad", "processor", "facebook", "title", ad.Title)
			continue
		}

		// Carfax Valuation
		var carfaxValue float64
		if carfaxFailures >= carfaxCircuitBreakerThreshold {
			slog.Warn("Carfax circuit breaker open", "processor", "facebook", "title", ad.Title, "consecutive_failures", carfaxFailures)
		} else {
			trimPicker := TrimPicker(func(ctx context.Context, year int, make, model string, options []string) string {
				return PickCheapestTrim(ctx, p.ai, year, make, model, options)
			})
			carfaxValue, err = carfaxClient.GetValue(ctx, carData.Year, carData.Make, carData.Model, carData.Trim, carData.Engine, carData.Transmission, carData.Drivetrain, carData.BodyStyle, group.postal, carData.Odometer, trimPicker)
			if err != nil {
				carfaxFailures++
				tracker.TrackCarfaxValuation(false)
				slog.Warn("Carfax valuation failed", "processor", "facebook", "title", ad.Title, "error", err, "consecutive_failures", carfaxFailures)
			} else {
				carfaxFailures = 0
				tracker.TrackCarfaxValuation(true)
				if saveErr := p.store.SavePriceHistory(ctx, carData.Model, carfaxValue); saveErr != nil {
					slog.Warn("Failed to save price history", "processor", "facebook", "model", carData.Model, "error", saveErr)
				}
			}
		}

		// VMR Canada Valuation (HTTP only — no browser needed)
		var vmrResult *VMRResult
		if carData.IsCarfaxEligible() {
			vmrResult, err = GetVMRValue(ctx, p.ai, carData.Year, carData.Make, carData.Model, carData.Trim, group.postal, carData.Odometer)
			if err != nil {
				slog.Warn("VMR valuation failed", "processor", "facebook", "title", ad.Title, "error", err)
			}
		}

		// Gemini FOMO Analysis
		randomDelay(100*time.Millisecond, 300*time.Millisecond)
		tracker.TrackGeminiCall(0, 0)
		analysis, err := AnalyzeDeal(ctx, p.ai, carData, carfaxValue, vmrResult, ad.Price)
		if err != nil {
			slog.Error("FOMO analysis failed", "processor", "facebook", "title", ad.Title, "error", err)
			continue
		}

		var vmrRetail float64
		if vmrResult != nil {
			vmrRetail = vmrResult.Retail
		}

		slog.Info("Facebook FOMO analysis result",
			"processor", "facebook",
			"title", ad.Title,
			"is_warm", analysis.IsWarm,
			"is_lava_hot", analysis.IsLavaHot,
			"ai_title", analysis.Title,
			"asking_price", ad.Price,
			"carfax_value", carfaxValue,
			"vmr_retail", vmrRetail,
			"year", carData.Year,
			"make", carData.Make,
			"model", carData.Model,
			"odometer", carData.Odometer,
		)

		// Fan out deal to subscribers — only warm or lava-hot deals get posted
		if analysis.IsWarm || analysis.IsLavaHot {
			dealsFound++
			tracker.TrackDealFound()
			p.fanOutDeal(ctx, group.subs, ad, analysis, carfaxValue, vmrRetail, tracker)
		}
	}

	slog.Info("City processing complete",
		"processor", "facebook", "component", "fb_scrape",
		"city", group.city,
		"duration_ms", time.Since(cityStart).Milliseconds(),
		"ads_scraped", len(ads),
		"ads_processed", adsProcessed,
		"ads_skipped", adsSkipped,
		"deals_found", dealsFound,
		"carfax_failures", carfaxFailures,
	)
}

func (p *Processor) fanOutDeal(ctx context.Context, subs []models.Subscription, ad models.ScrapedAd, analysis *models.FacebookDealAnalysis, carfaxValue, vmrRetail float64, tracker *metrics.Tracker) {
	// Filter subs by brand and send
	var matchingSubs []models.Subscription
	for _, sub := range subs {
		if len(sub.FilterBrands) > 0 {
			lwTitle := strings.ToLower(analysis.Title)
			found := false
			for _, b := range sub.FilterBrands {
				if strings.Contains(lwTitle, strings.ToLower(b)) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		matchingSubs = append(matchingSubs, sub)
	}

	if len(matchingSubs) == 0 {
		return
	}

	if err := p.notifier.SendFacebookDeal(ctx, analysis.Title, ad.URL, analysis.Summary, analysis.KnownIssues, ad.Price, carfaxValue, vmrRetail, analysis.IsWarm, analysis.IsLavaHot, matchingSubs); err != nil {
		slog.Error("Failed to send facebook deal", "processor", "facebook", "title", analysis.Title, "error", err)
	} else {
		tracker.TrackDiscordMessage()
		slog.Info("DEAL POSTED", "processor", "facebook", "title", analysis.Title, "subscribers", len(matchingSubs))
	}
}

// isTransientError returns true for network/proxy errors that are temporary and
// expected to self-resolve, so they can be logged at WARN instead of ERROR.
// nonCarKeywords are title substrings that strongly indicate a listing is not a
// car, truck, SUV, or van. Matched case-insensitively against the ad title as a
// cheap pre-filter BEFORE the Gemini NormalizeAd call, so we don't spend an AI
// call on something that is obviously not a car. This list doesn't need to be
// exhaustive—anything it misses will still be caught by the authoritative
// IsCarfaxEligible() check after normalization. Keep entries lowercase.
var nonCarKeywords = []string{
	"motorcycle", "motorbike", "dirt bike", "dirtbike", "sport bike",
	"boat", "pontoon", "kayak", "canoe", "jet ski", "jetski", "seadoo", "sea-doo", "outboard",
	"inflatable", "sailboat",
	"atv", "quad", "side by side", "side-by-side", "utv",
	"snowmobile", "skidoo", "ski-doo", "sled",
	"trailer", "camper", "motorhome", "motor home", "travel trailer", "fifth wheel", "5th wheel",
	"rv ", "r.v.", "winnebago", "toy hauler",
	"scooter", "moped", "vespa",
	"tractor", "excavator", "skid steer", "forklift", "loader", "backhoe",
	"golf cart", "go kart", "go-kart", "gokart",
	"lawnmower", "lawn mower", "snowblower", "snow blower",
	"generator", "parts only", "parting out",
	// Brands that never manufacture consumer cars — safe to pre-filter.
	"kawasaki", "harley", "ducati", "yamaha", "polaris",
	"can-am", "canam", "arctic cat",
	"peterbilt", "freightliner", "kenworth", "western star",
	// RV / camper manufacturers — none of these make consumer cars.
	"gulf stream", "gulfstream", "jayco", "coachmen", "forest river",
	"keystone", "newmar", "tiffin", "prime time", "heartland rv",
	"crossroads", "dutchmen", "palomino", "starcraft rv", "sunseeker",
}

// isLikelyNonCar returns true if the ad title contains a keyword strongly
// associated with non-car vehicles. This is a best-effort optimisation—false
// negatives are fine (the post-normalization IsCarfaxEligible check is the
// real gate), but false positives would hide real car deals, so only include
// keywords that are unambiguous.
func isLikelyNonCar(title string) bool {
	lower := strings.ToLower(title)
	for _, kw := range nonCarKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// hasDigit reports whether s contains at least one ASCII digit.
func hasDigit(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			return true
		}
	}
	return false
}

// isTooVague returns true for ads that lack enough information for Gemini to
// extract structured vehicle data. A single-word title without digits (e.g.
// "Bravo") is too vague unless the description contains real vehicle info
// (year/make/model). Mileage alone doesn't help — Gemini can't determine
// make/model from a title like "Bravo" with just "120000 km".
func isTooVague(title, description, mileage string) bool {
	title = strings.TrimSpace(title)
	words := strings.Fields(title)
	if len(words) >= 2 {
		return false // multi-word titles have enough context
	}
	if hasDigit(title) {
		return false // has a year or number — worth trying
	}
	// Single word, no digits: only a description with real vehicle details
	// (containing year digits like "2015 Fiat Bravo") can save it. Mileage
	// alone is not enough context for Gemini.
	desc := strings.TrimSpace(description)
	return desc == "" || !hasDigit(desc)
}

// isWantedAd returns true for "wanted/ISO/WTB" ads where the poster is
// looking to buy, not selling. These waste Gemini + Carfax/VMR calls and
// should never be posted to Discord.
func isWantedAd(title, description string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, prefix := range []string{
		"looking for ",
		"looking to buy ",
		"iso ",
		"wtb ",
		"wanted ",
		"wanting ",
		"in search of ",
	} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	transientPatterns := []string{
		"NS_ERROR_PROXY_CONNECTION_REFUSED",
		"NS_ERROR_NET_RESET",
		"NS_ERROR_CONNECTION_REFUSED",
		"connection refused",
		"connection reset",
		"i/o timeout",
		"deadline exceeded",
		"net::ERR_PROXY",
		"net::ERR_TIMED_OUT",
		"net::ERR_CONNECTION",
		"soft block detected",
	}
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func randomDelay(min, max time.Duration) {
	d := min + time.Duration(rand.Int63n(int64(max-min)))
	time.Sleep(d)
}
