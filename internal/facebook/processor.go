package facebook

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/playwright-community/playwright-go"
)

// Store defines the Firestore operations needed by the Facebook processor.
type Store interface {
	GetFacebookSubscriptions(ctx context.Context) ([]models.Subscription, error)
	SaveFacebookAd(ctx context.Context, ad *models.FacebookAdRecord) (bool, error)
	PruneFacebookAds(ctx context.Context, maxAgeMonths int, maxRecords int) error
	SavePriceHistory(ctx context.Context, model string, value float64) error
}

// Notifier defines the Discord notification operations.
type Notifier interface {
	SendFacebookDeal(ctx context.Context, title, url, summary string, askingPrice, carfaxValue float64, subs []models.Subscription) error
}

// Processor handles Facebook Marketplace deal scraping and analysis.
type Processor struct {
	store    Store
	notifier Notifier
	ai       AIClient
	proxyURL string
}

// NewProcessor creates a new Facebook deal processor.
func NewProcessor(store Store, notifier Notifier, ai AIClient, proxyURL string) *Processor {
	return &Processor{
		store:    store,
		notifier: notifier,
		ai:       ai,
		proxyURL: proxyURL,
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
	if p.proxyURL == "" {
		slog.Info("Facebook processor: proxy not configured, skipping", "processor", "facebook")
		return nil
	}

	slog.Info("Starting Facebook deal processing", "processor", "facebook")

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

	carfaxClient := NewCarfaxClient(pm)

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
		p.processCity(ctx, group, carfaxClient, pm)
	}

	// Prune old ads
	if err := p.store.PruneFacebookAds(ctx, 6, 1000); err != nil {
		slog.Warn("Failed to prune old facebook ads", "processor", "facebook", "error", err)
	}

	slog.Info("Facebook scrape cycle complete", "processor", "facebook")
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

func (p *Processor) processCity(ctx context.Context, group cityGroup, carfaxClient *CarfaxClient, pm *BrowserManager) {
	slog.Info("Processing city", "processor", "facebook", "city", group.city, "subscribers", len(group.subs))

	cfg := &FacebookScrapeConfig{
		City:     group.city,
		Category: group.category,
		RadiusKm: group.radiusKm,
	}

	// Scrape marketplace
	ads, err := ScrapeMarketplace(ctx, slog.Default(), pm, cfg)
	if err != nil {
		slog.Error("Failed to scrape marketplace", "processor", "facebook", "city", group.city, "error", err)
		return
	}

	if len(ads) == 0 {
		slog.Info("No ads found", "processor", "facebook", "city", group.city)
		return
	}

	slog.Info("Processing ads", "processor", "facebook", "city", group.city, "count", len(ads))

	// Create a reusable page for listing details
	detailCtx, detailErr := pm.NewContext()
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
		if err != nil {
			slog.Error("Failed to normalize ad", "processor", "facebook", "title", ad.Title, "error", err)
			continue
		}

		// Save to Firestore (deduplication)
		adRecord := &models.FacebookAdRecord{
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
		if !carData.IsCarfaxEligible() {
			slog.Info("Skipping carfax for non-car vehicle", "processor", "facebook", "title", ad.Title, "type", carData.VehicleType)
		} else if carfaxFailures >= carfaxCircuitBreakerThreshold {
			slog.Warn("Carfax circuit breaker open", "processor", "facebook", "title", ad.Title, "consecutive_failures", carfaxFailures)
		} else {
			trimPicker := TrimPicker(func(ctx context.Context, year int, make, model string, options []string) string {
				return PickCheapestTrim(ctx, p.ai, year, make, model, options)
			})
			carfaxValue, err = carfaxClient.GetValue(ctx, carData.Year, carData.Make, carData.Model, carData.Trim, carData.Engine, carData.Transmission, carData.Drivetrain, carData.BodyStyle, group.postal, carData.Odometer, trimPicker)
			if err != nil {
				carfaxFailures++
				slog.Warn("Carfax valuation failed", "processor", "facebook", "title", ad.Title, "error", err, "consecutive_failures", carfaxFailures)
			} else {
				carfaxFailures = 0
				if saveErr := p.store.SavePriceHistory(ctx, carData.Model, carfaxValue); saveErr != nil {
					slog.Warn("Failed to save price history", "processor", "facebook", "model", carData.Model, "error", saveErr)
				}
			}
		}

		// Gemini FOMO Analysis
		randomDelay(100*time.Millisecond, 300*time.Millisecond)
		analysis, err := AnalyzeDeal(ctx, p.ai, carData, carfaxValue, ad.Price)
		if err != nil {
			slog.Error("FOMO analysis failed", "processor", "facebook", "title", ad.Title, "error", err)
			continue
		}

		// Fan out deal to subscribers
		if analysis.IsDeal {
			p.fanOutDeal(ctx, group.subs, ad, analysis, carfaxValue)
		}
	}
}

func (p *Processor) fanOutDeal(ctx context.Context, subs []models.Subscription, ad models.ScrapedAd, analysis *models.FacebookDealAnalysis, carfaxValue float64) {
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

	if err := p.notifier.SendFacebookDeal(ctx, analysis.Title, ad.URL, analysis.Summary, ad.Price, carfaxValue, matchingSubs); err != nil {
		slog.Error("Failed to send facebook deal", "processor", "facebook", "title", analysis.Title, "error", err)
	} else {
		slog.Info("DEAL POSTED", "processor", "facebook", "title", analysis.Title, "subscribers", len(matchingSubs))
	}
}

func randomDelay(min, max time.Duration) {
	d := min + time.Duration(rand.Int63n(int64(max-min)))
	time.Sleep(d)
}
