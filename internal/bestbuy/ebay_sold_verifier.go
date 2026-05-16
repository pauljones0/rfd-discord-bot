package bestbuy

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const (
	ebaySoldVerdictPass       = "pass"
	ebaySoldVerdictFail       = "fail"
	ebaySoldVerdictNoComps    = "no_comps"
	ebaySoldVerdictFetchError = "fetch_error"

	defaultEbaySoldCacheTTL       = 24 * time.Hour
	defaultEbaySoldMinComps       = 3
	defaultEbaySoldWarmMinGapPct  = 25.0
	defaultEbaySoldWarmMinGapDols = 100.0
)

type ComputeSoldVerifier interface {
	BeginRun()
	Verify(ctx context.Context, observation ComputeObservation, prior ComputeObservation, now time.Time, logger *slog.Logger) EbaySoldVerification
}

type EbaySoldVerifierOptions struct {
	Enabled       bool
	Backends      []string
	CacheTTL      time.Duration
	PaidEnabled   bool
	PaidAttempt   func(context.Context) error
	BeforeRun     func()
	MinComps      int
	MinGapPct     float64
	MinGapDollars float64
	Timeout       time.Duration
}

type EbaySoldVerifier struct {
	enabled       bool
	backends      []string
	cacheTTL      time.Duration
	paidEnabled   bool
	paidAttempt   func(context.Context) error
	beforeRun     func()
	minComps      int
	minGapPct     float64
	minGapDollars float64
	timeout       time.Duration
}

type EbaySoldVerification struct {
	Pass            bool
	Verdict         string
	Query           string
	Backend         string
	ComparableCount int
	MedianPrice     float64
	P25Price        float64
	GapPct          float64
	GapAmount       float64
	CheckedAt       time.Time
	AlertKey        string
	Error           string
}

type ebaySoldListing struct {
	Title string
	Price float64
}

func NewEbaySoldVerifier(opts EbaySoldVerifierOptions) *EbaySoldVerifier {
	backends := compactStrings(opts.Backends)
	if len(backends) == 0 {
		backends = []string{
			scrapebackend.BackendHTTP,
			scrapebackend.BackendExternalStealth,
			scrapebackend.BackendCamoufox,
			scrapebackend.BackendAICrawler,
		}
	}
	cacheTTL := opts.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultEbaySoldCacheTTL
	}
	minComps := opts.MinComps
	if minComps <= 0 {
		minComps = defaultEbaySoldMinComps
	}
	minGapPct := opts.MinGapPct
	if minGapPct <= 0 {
		minGapPct = defaultEbaySoldWarmMinGapPct
	}
	minGapDollars := opts.MinGapDollars
	if minGapDollars <= 0 {
		minGapDollars = defaultEbaySoldWarmMinGapDols
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	return &EbaySoldVerifier{
		enabled:       opts.Enabled,
		backends:      backends,
		cacheTTL:      cacheTTL,
		paidEnabled:   opts.PaidEnabled,
		paidAttempt:   opts.PaidAttempt,
		beforeRun:     opts.BeforeRun,
		minComps:      minComps,
		minGapPct:     minGapPct,
		minGapDollars: minGapDollars,
		timeout:       timeout,
	}
}

func (v *EbaySoldVerifier) BeginRun() {
	if v != nil && v.beforeRun != nil {
		v.beforeRun()
	}
}

func (v *EbaySoldVerifier) Verify(ctx context.Context, observation ComputeObservation, prior ComputeObservation, now time.Time, logger *slog.Logger) EbaySoldVerification {
	alertKey := computeAlertKey(observation)
	if v == nil || !v.enabled {
		return EbaySoldVerification{Pass: true, Verdict: "disabled", AlertKey: alertKey, CheckedAt: now}
	}
	if cached, ok := v.cached(prior, alertKey, now); ok {
		return cached
	}

	query := buildEbaySoldQuery(observation)
	verification := EbaySoldVerification{
		Query:     query,
		CheckedAt: now,
		AlertKey:  alertKey,
	}
	if query == "" {
		verification.Verdict = ebaySoldVerdictNoComps
		verification.Error = "empty sold-search query"
		return verification
	}

	searchURL := ebaySoldSearchURL(query)
	var failures []string
	for _, backend := range v.backends {
		result := scrapebackend.FetchHTML(ctx, scrapebackend.FetchOptions{
			Backend:          backend,
			URL:              searchURL,
			Timeout:          v.timeout,
			ExternalCommand:  ebaySoldExternalCommand(),
			CamoufoxCommand:  ebaySoldCamoufoxCommand(),
			AICrawlerCommand: ebaySoldAICrawlerCommand(),
			PaidCommand:      ebaySoldPaidCommand(),
			PaidEnabled:      v.paidEnabled,
			PaidAttempt:      v.paidAttempt,
		})
		if result.Error != "" || result.BlockSignal != "" {
			failures = append(failures, fmt.Sprintf("%s: %s %s", backend, result.Error, result.BlockSignal))
			continue
		}

		listings, err := ParseEbaySoldListings(result.HTML)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: parse: %s", backend, err))
			continue
		}
		verification = scoreEbaySoldVerification(observation, listings, v.minComps, v.minGapPct, v.minGapDollars)
		verification.Query = query
		verification.Backend = backend
		verification.CheckedAt = now
		verification.AlertKey = alertKey
		if logger != nil {
			logger.Info("Best Buy compute eBay sold verification complete",
				"sku", observation.SKU,
				"query", query,
				"backend", backend,
				"verdict", verification.Verdict,
				"sold_comps", verification.ComparableCount,
				"sold_median", verification.MedianPrice,
				"sold_gap_pct", verification.GapPct,
			)
		}
		return verification
	}

	verification.Verdict = ebaySoldVerdictFetchError
	verification.Error = strings.Join(failures, "; ")
	return verification
}

func (v *EbaySoldVerifier) cached(prior ComputeObservation, alertKey string, now time.Time) (EbaySoldVerification, bool) {
	if prior.EbaySoldAlertKey == "" || prior.EbaySoldAlertKey != alertKey || prior.EbaySoldCheckedAt.IsZero() {
		return EbaySoldVerification{}, false
	}
	if v.cacheTTL <= 0 || now.Sub(prior.EbaySoldCheckedAt) > v.cacheTTL {
		return EbaySoldVerification{}, false
	}
	verification := EbaySoldVerification{
		Pass:            prior.EbaySoldVerdict == ebaySoldVerdictPass,
		Verdict:         prior.EbaySoldVerdict,
		Query:           prior.EbaySoldQuery,
		Backend:         prior.EbaySoldBackend,
		ComparableCount: prior.EbaySoldComparableCount,
		MedianPrice:     prior.EbaySoldMedianPrice,
		P25Price:        prior.EbaySoldP25Price,
		GapPct:          prior.EbaySoldGapPct,
		GapAmount:       prior.EbaySoldGapAmount,
		CheckedAt:       prior.EbaySoldCheckedAt,
		AlertKey:        prior.EbaySoldAlertKey,
		Error:           prior.EbaySoldError,
	}
	return verification, true
}

func applyEbaySoldVerification(observation *ComputeObservation, verification EbaySoldVerification) {
	if observation == nil {
		return
	}
	observation.EbaySoldQuery = verification.Query
	observation.EbaySoldBackend = verification.Backend
	observation.EbaySoldComparableCount = verification.ComparableCount
	observation.EbaySoldMedianPrice = verification.MedianPrice
	observation.EbaySoldP25Price = verification.P25Price
	observation.EbaySoldGapPct = verification.GapPct
	observation.EbaySoldGapAmount = verification.GapAmount
	observation.EbaySoldVerdict = verification.Verdict
	observation.EbaySoldCheckedAt = verification.CheckedAt
	observation.EbaySoldAlertKey = verification.AlertKey
	observation.EbaySoldError = verification.Error
}

func buildEbaySoldQuery(observation ComputeObservation) string {
	spec := observation.Spec
	parts := make([]string, 0, 8)
	switch spec.Family {
	case "dell_precision":
		parts = append(parts, "Dell", "Precision")
	case "hp_z":
		parts = append(parts, "HP", "Z")
	case "hpe_proliant":
		parts = append(parts, "ProLiant")
	case "dell_poweredge":
		parts = append(parts, "PowerEdge")
	case "lenovo_thinkstation":
		parts = append(parts, "ThinkStation")
	default:
		if spec.Brand != "" {
			parts = append(parts, spec.Brand)
		}
	}
	if spec.Model != "" {
		parts = append(parts, spec.Model)
	}
	if spec.CPUModel != "" && spec.CPUModel != "xeon" {
		parts = append(parts, spec.CPUModel)
	}
	if spec.RAMGB > 0 {
		parts = append(parts, fmt.Sprintf("%.0fGB RAM", spec.RAMGB))
	}
	if spec.GPU != "" {
		parts = append(parts, spec.GPU)
	}
	query := strings.Join(compactStrings(parts), " ")
	if len(strings.Fields(query)) < 2 {
		query = cleanSoldQueryTitle(observation.Name)
	}
	return strings.TrimSpace(query)
}

func cleanSoldQueryTitle(title string) string {
	title = html.UnescapeString(title)
	title = regexp.MustCompile(`(?i)\b(refurbished|excellent|good|fair|open box|brand new|renewed|windows|win\s*1[01]\s*pro|warranty)\b`).ReplaceAllString(title, " ")
	title = regexp.MustCompile(`[^\pL\pN]+`).ReplaceAllString(title, " ")
	words := strings.Fields(title)
	if len(words) > 12 {
		words = words[:12]
	}
	return strings.Join(words, " ")
}

func ebaySoldSearchURL(query string) string {
	values := url.Values{}
	values.Set("_nkw", query)
	values.Set("LH_Sold", "1")
	values.Set("LH_Complete", "1")
	values.Set("LH_BIN", "1")
	values.Set("_sop", "13")
	values.Set("rt", "nc")
	return "https://www.ebay.ca/sch/i.html?" + values.Encode()
}

func ParseEbaySoldListings(pageHTML string) ([]ebaySoldListing, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return nil, err
	}
	var listings []ebaySoldListing
	doc.Find("li.s-item").Each(func(_ int, sel *goquery.Selection) {
		title := strings.TrimSpace(sel.Find(".s-item__title").First().Text())
		title = strings.TrimSpace(strings.TrimPrefix(title, "New Listing"))
		if title == "" || strings.Contains(strings.ToLower(title), "shop on ebay") {
			return
		}
		priceText := strings.TrimSpace(sel.Find(".s-item__price").First().Text())
		price, ok := parseEbaySoldPrice(priceText)
		if !ok || price <= 0 {
			return
		}
		listings = append(listings, ebaySoldListing{Title: title, Price: price})
	})
	return listings, nil
}

func parseEbaySoldPrice(text string) (float64, bool) {
	text = html.UnescapeString(strings.TrimSpace(text))
	lower := strings.ToLower(text)
	if strings.Contains(lower, " to ") || strings.Contains(lower, "shipping") {
		return 0, false
	}
	if strings.Contains(lower, "us $") || strings.Contains(lower, "eur") || strings.Contains(lower, "gbp") {
		return 0, false
	}
	match := regexp.MustCompile(`(?i)(?:c\s*)?\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`).FindStringSubmatch(text)
	if len(match) < 2 {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	return value, err == nil
}

func scoreEbaySoldVerification(observation ComputeObservation, listings []ebaySoldListing, minComps int, minGapPct, minGapDollars float64) EbaySoldVerification {
	currentPrice := effectiveProductPrice(observation.Product)
	verification := EbaySoldVerification{Verdict: ebaySoldVerdictNoComps}
	if currentPrice <= 0 {
		verification.Error = "missing current price"
		return verification
	}
	var prices []float64
	for _, listing := range listings {
		if ebaySoldListingMatches(observation, listing) {
			prices = append(prices, listing.Price)
		}
	}
	if len(prices) < minComps {
		verification.ComparableCount = len(prices)
		verification.Error = fmt.Sprintf("not enough matching sold comps: %d/%d", len(prices), minComps)
		return verification
	}
	sort.Float64s(prices)
	median := percentileSorted(prices, 0.50)
	p25 := percentileSorted(prices, 0.25)
	gap := median - currentPrice
	gapPct := 0.0
	if median > 0 {
		gapPct = gap / median * 100
	}
	verification.ComparableCount = len(prices)
	verification.MedianPrice = median
	verification.P25Price = p25
	verification.GapAmount = gap
	verification.GapPct = gapPct
	if gap >= minGapDollars && gapPct >= minGapPct {
		verification.Pass = true
		verification.Verdict = ebaySoldVerdictPass
	} else {
		verification.Verdict = ebaySoldVerdictFail
		verification.Error = fmt.Sprintf("sold comps gap %.1f%%/$%.0f below threshold %.1f%%/$%.0f", gapPct, gap, minGapPct, minGapDollars)
	}
	return verification
}

func ebaySoldListingMatches(observation ComputeObservation, listing ebaySoldListing) bool {
	title := strings.ToLower(listing.Title)
	if rejectComputeReason(title) != "" {
		return false
	}
	spec := observation.Spec
	if spec.Model != "" && !strings.Contains(normalizeModel(title), normalizeModel(spec.Model)) {
		return false
	}
	if spec.Family != "" && !familyTitleMatch(spec.Family, spec.Model, title) {
		return false
	}
	if spec.RAMGB > 0 {
		if ram := ramGBFromText(title); ram > 0 {
			ratio := ram / spec.RAMGB
			if ratio < 0.75 || ratio > 1.5 {
				return false
			}
		}
	}
	if spec.GPU != "" {
		listingGPU := gpuFromText(title)
		if listingGPU == "" || !sameGPU(spec.GPU, listingGPU) {
			return false
		}
	}
	if spec.CPUModel != "" && spec.CPUModel != "xeon" {
		if cpu := cpuModelFromText(title); cpu != "" && !similarCPUClass(spec.CPUModel, cpu) && normalizeModel(cpu) != normalizeModel(spec.CPUModel) {
			return false
		}
	}
	overlap := tokenOverlap(cleanSoldQueryTitle(observation.Name), listing.Title)
	return overlap >= 0.35
}

func familyTitleMatch(family, model, title string) bool {
	switch family {
	case "dell_precision":
		return strings.Contains(title, "precision")
	case "hp_z":
		return strings.Contains(title, "hp z") || (model != "" && strings.Contains(title, "z"+normalizeModel(model)))
	case "hpe_proliant":
		return strings.Contains(title, "proliant")
	case "dell_poweredge":
		return strings.Contains(title, "poweredge")
	case "lenovo_thinkstation":
		return strings.Contains(title, "thinkstation")
	default:
		return true
	}
}

func sameGPU(a, b string) bool {
	return normalizeModel(a) == normalizeModel(b)
}

func tokenOverlap(a, b string) float64 {
	aTokens := meaningfulTokens(a)
	bTokens := meaningfulTokens(b)
	if len(aTokens) == 0 || len(bTokens) == 0 {
		return 0
	}
	intersection := 0
	for token := range aTokens {
		if bTokens[token] {
			intersection++
		}
	}
	return float64(intersection) / math.Min(float64(len(aTokens)), float64(len(bTokens)))
}

func meaningfulTokens(value string) map[string]bool {
	value = strings.ToLower(html.UnescapeString(value))
	value = regexp.MustCompile(`[^\pL\pN]+`).ReplaceAllString(value, " ")
	stop := map[string]bool{
		"the": true, "and": true, "with": true, "for": true, "win": true, "windows": true,
		"refurbished": true, "excellent": true, "good": true, "fair": true, "open": true, "box": true,
		"desktop": true, "laptop": true, "workstation": true, "computer": true,
	}
	out := make(map[string]bool)
	for _, token := range strings.Fields(value) {
		if len(token) < 3 || stop[token] {
			continue
		}
		out[token] = true
	}
	return out
}

func ebaySoldExternalCommand() string {
	return firstNonEmptyEnv("EBAY_SOLD_EXTERNAL_STEALTH_COMMAND", "EBAY_COUPON_EXTERNAL_STEALTH_COMMAND", "SCRAPELAB_EXTERNAL_STEALTH_COMMAND")
}

func ebaySoldCamoufoxCommand() string {
	return firstNonEmptyEnv("EBAY_SOLD_CAMOUFOX_COMMAND", "EBAY_COUPON_CAMOUFOX_COMMAND", "SCRAPELAB_CAMOUFOX_COMMAND")
}

func ebaySoldAICrawlerCommand() string {
	return firstNonEmptyEnv("EBAY_SOLD_AI_CRAWLER_COMMAND", "EBAY_COUPON_AI_CRAWLER_COMMAND", "SCRAPELAB_AI_CRAWLER_COMMAND")
}

func ebaySoldPaidCommand() string {
	return firstNonEmptyEnv("EBAY_SOLD_PAID_TRIAL_COMMAND", "EBAY_COUPON_PAID_TRIAL_COMMAND", "SCRAPELAB_PAID_TRIAL_COMMAND")
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
