package bestbuy

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const (
	ebaySoldVerdictPass       = "pass"
	ebaySoldVerdictFail       = "fail"
	ebaySoldVerdictNoComps    = "no_comps"
	ebaySoldVerdictFetchError = "fetch_error"

	defaultEbaySoldCacheTTL       = 24 * time.Hour
	defaultEbaySoldMinComps       = 3
	defaultEbaySoldWarmMinGapPct  = ebaySoldWarmMinGapPct
	defaultEbaySoldWarmMinGapDols = 0.0
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
	QueryDelay    time.Duration
	Sleep         contextSleeper
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
	limiter       *soldFetchLimiter
}

type EbaySoldVerification struct {
	Pass              bool
	Verdict           string
	Query             string
	Backend           string
	ComparableCount   int
	MedianPrice       float64
	P25Price          float64
	GapPct            float64
	GapAmount         float64
	MarketLabel       string
	LocalResalePrice  float64
	LocalMarginPct    float64
	LocalMarginAmount float64
	CheckedAt         time.Time
	AlertKey          string
	Error             string
	Comparables       []ComputeExternalComparable
}

func NewEbaySoldVerifier(opts EbaySoldVerifierOptions) *EbaySoldVerifier {
	backends := ebaySoldBackends(opts.Backends, opts.PaidEnabled)
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
	queryDelay := opts.QueryDelay
	if queryDelay <= 0 {
		queryDelay = defaultBestBuySoldCompQueryDelay
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
		limiter:       newSoldFetchLimiter(queryDelay, opts.Sleep),
	}
}

func (v *EbaySoldVerifier) BeginRun() {
	if v == nil {
		return
	}
	if v.beforeRun != nil {
		v.beforeRun()
	}
	if v.limiter != nil {
		v.limiter.BeginRun()
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

	queries := buildEbaySoldQueries(observation)
	verification := EbaySoldVerification{
		CheckedAt: now,
		AlertKey:  alertKey,
	}
	if len(queries) == 0 {
		verification.Verdict = ebaySoldVerdictNoComps
		verification.Error = "empty sold-search query"
		return ebaySoldVerificationWithFailOpenPass(verification)
	}

	var failures []string
	var bestVerification EbaySoldVerification
	attempts := scrapebackend.NewAttemptCounter()
	exactQuery := buildEbaySoldQueryWithRAM(observation, true)
	for _, query := range queries {
		searchURL := ebay.SoldSearchURL(query)
		for _, backend := range v.backends {
			attempts.RecordAttempt(backend)
			if err := v.limiter.BeforeFetch(ctx); err != nil {
				attempts.RecordError(backend)
				verification.Verdict = ebaySoldVerdictFetchError
				verification.Error = err.Error()
				verification = ebaySoldVerificationWithFailOpenPass(verification)
				logEbaySoldVerificationBackendSummary(logger, observation, verification, attempts)
				return verification
			}
			result := scrapebackend.FetchHTML(ctx, scrapebackend.FetchOptions{
				Backend:             backend,
				URL:                 searchURL,
				Timeout:             v.timeout,
				ExternalCommand:     ebaySoldExternalCommand(),
				ExternalCommandArgs: ebaySoldExternalCommandArgs(),
				CamoufoxCommand:     ebaySoldCamoufoxCommand(),
				CamoufoxCommandArgs: ebaySoldCamoufoxCommandArgs(),
				AICrawlerCommand:    ebaySoldAICrawlerCommand(),
				AICrawlerArgs:       ebaySoldAICrawlerCommandArgs(),
				PaidCommand:         ebaySoldPaidCommand(),
				PaidCommandArgs:     ebaySoldPaidCommandArgs(),
				PaidEnabled:         v.paidEnabled,
				PaidAttempt:         v.paidAttempt,
			})
			if issue := attempts.RecordFetchResult(backend, result); issue != "" {
				failures = append(failures, fmt.Sprintf("%s/%s: %s", backend, query, issue))
				if logger != nil {
					logger.Warn("Best Buy compute eBay sold backend failed",
						"sku", observation.SKU,
						"query", query,
						"backend", backend,
						"status", result.StatusCode,
						"block_signal", result.BlockSignal,
						"error", result.Error,
						"duration_ms", result.Duration.Milliseconds(),
					)
				}
				continue
			}

			listings, err := ebay.ParseSoldListings(result.HTML)
			if err != nil {
				attempts.RecordParseError(backend)
				failures = append(failures, fmt.Sprintf("%s/%s: parse: %s", backend, query, err))
				if logger != nil {
					logger.Warn("Best Buy compute eBay sold parse failed",
						"sku", observation.SKU,
						"query", query,
						"backend", backend,
						"error", err,
					)
				}
				continue
			}
			verification = scoreEbaySoldVerification(observation, listings, v.minComps, v.minGapPct, v.minGapDollars)
			verification.Query = query
			verification.Backend = backend
			verification.CheckedAt = now
			verification.AlertKey = alertKey
			attempts.RecordVerdict(backend, verification.Verdict)
			for i := range verification.Comparables {
				verification.Comparables[i].Query = query
				verification.Comparables[i].Backend = backend
				verification.Comparables[i].ObservedAt = now
			}
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
			if verification.Pass || (verification.Verdict != ebaySoldVerdictNoComps && strings.EqualFold(query, exactQuery)) {
				logEbaySoldVerificationBackendSummary(logger, observation, verification, attempts)
				return verification
			}
			if verification.ComparableCount > bestVerification.ComparableCount || bestVerification.Query == "" {
				bestVerification = verification
			}
		}
	}

	if bestVerification.Query != "" {
		bestVerification = ebaySoldVerificationWithFailOpenPass(bestVerification)
		logEbaySoldVerificationBackendSummary(logger, observation, bestVerification, attempts)
		return bestVerification
	}
	verification.Verdict = ebaySoldVerdictFetchError
	verification.Error = soldFetchFailureSummary(failures)
	verification = ebaySoldVerificationWithFailOpenPass(verification)
	logEbaySoldVerificationBackendSummary(logger, observation, verification, attempts)
	return verification
}

func logEbaySoldVerificationBackendSummary(logger *slog.Logger, observation ComputeObservation, verification EbaySoldVerification, attempts *scrapebackend.AttemptCounter) {
	if logger == nil || attempts == nil || attempts.TotalAttempts() == 0 {
		return
	}
	attrs := []any{
		"sku", observation.SKU,
		"query", verification.Query,
		"backend", verification.Backend,
		"verdict", verification.Verdict,
		"sold_comps", verification.ComparableCount,
		"error", verification.Error,
	}
	attrs = append(attrs, attempts.Attrs()...)
	logger.Info("Best Buy compute eBay sold backend summary", attrs...)
}

func (v *EbaySoldVerifier) cached(prior ComputeObservation, alertKey string, now time.Time) (EbaySoldVerification, bool) {
	if prior.EbaySoldAlertKey == "" || prior.EbaySoldAlertKey != alertKey || prior.EbaySoldCheckedAt.IsZero() {
		return EbaySoldVerification{}, false
	}
	if v.cacheTTL <= 0 || now.Sub(prior.EbaySoldCheckedAt) > v.cacheTTL {
		return EbaySoldVerification{}, false
	}
	market := soldCompMarketVerdictFromSummary(effectiveProductPrice(prior.Product), prior.EbaySoldComparableCount, prior.EbaySoldMedianPrice, prior.EbaySoldP25Price, v.minComps)
	verification := EbaySoldVerification{
		Pass:              ebaySoldVerificationPassForVerdict(prior.EbaySoldVerdict),
		Verdict:           prior.EbaySoldVerdict,
		Query:             prior.EbaySoldQuery,
		Backend:           prior.EbaySoldBackend,
		ComparableCount:   prior.EbaySoldComparableCount,
		MedianPrice:       prior.EbaySoldMedianPrice,
		P25Price:          prior.EbaySoldP25Price,
		GapPct:            prior.EbaySoldGapPct,
		GapAmount:         prior.EbaySoldGapAmount,
		MarketLabel:       market.Label,
		LocalResalePrice:  market.LocalResalePrice,
		LocalMarginPct:    market.LocalMarginPct,
		LocalMarginAmount: market.LocalMarginAmount,
		CheckedAt:         prior.EbaySoldCheckedAt,
		AlertKey:          prior.EbaySoldAlertKey,
		Error:             prior.EbaySoldError,
		Comparables:       prior.EbaySoldComparables,
	}
	return verification, true
}

func ebaySoldVerificationWithFailOpenPass(verification EbaySoldVerification) EbaySoldVerification {
	verification.Pass = ebaySoldVerificationPassForVerdict(verification.Verdict)
	return verification
}

func ebaySoldVerificationPassForVerdict(verdict string) bool {
	switch verdict {
	case ebaySoldVerdictFail:
		return false
	default:
		return true
	}
}

func ebaySoldVerificationConfirmsMarket(verification EbaySoldVerification) bool {
	return verification.Pass &&
		verification.Verdict == ebaySoldVerdictPass &&
		verification.ComparableCount > 0 &&
		verification.MedianPrice > 0
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
	observation.EbaySoldComparables = verification.Comparables
}

func buildEbaySoldQuery(observation ComputeObservation) string {
	queries := buildEbaySoldQueries(observation)
	if len(queries) == 0 {
		return ""
	}
	return queries[0]
}

func buildEbaySoldQueries(observation ComputeObservation) []string {
	spec := observation.Spec
	exact := buildEbaySoldQueryWithRAM(observation, true)
	relaxed := buildEbaySoldQueryWithRAM(observation, false)
	var queries []string
	if isExtremeComputeSpec(spec, effectiveProductPrice(observation.Product)) {
		queries = appendUniqueQuery(queries, relaxed)
		queries = appendUniqueQuery(queries, exact)
	} else {
		queries = appendUniqueQuery(queries, exact)
		queries = appendUniqueQuery(queries, relaxed)
	}
	return queries
}

func appendUniqueQuery(queries []string, query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return queries
	}
	for _, existing := range queries {
		if strings.EqualFold(existing, query) {
			return queries
		}
	}
	return append(queries, query)
}

func buildEbaySoldQueryWithRAM(observation ComputeObservation, includeRAM bool) string {
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
	if includeRAM && spec.RAMGB > 0 {
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

func scoreEbaySoldVerification(observation ComputeObservation, listings []ebay.SoldListing, minComps int, minGapPct, minGapDollars float64) EbaySoldVerification {
	currentPrice := effectiveProductPrice(observation.Product)
	verification := EbaySoldVerification{Verdict: ebaySoldVerdictNoComps}
	if currentPrice <= 0 {
		verification.Error = "missing current price"
		return verification
	}
	var prices []float64
	var comparables []ComputeExternalComparable
	for _, listing := range listings {
		if ebay.SoldListingMatches(observation, listing) {
			prices = append(prices, listing.Price)
			comparables = append(comparables, ebaySoldExternalComparable(observation, listing))
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
	market := soldCompMarketVerdictFromSummary(currentPrice, len(prices), median, p25, minComps)
	verification.ComparableCount = len(prices)
	verification.MedianPrice = median
	verification.P25Price = p25
	verification.GapAmount = gap
	verification.GapPct = gapPct
	verification.MarketLabel = market.Label
	verification.LocalResalePrice = market.LocalResalePrice
	verification.LocalMarginPct = market.LocalMarginPct
	verification.LocalMarginAmount = market.LocalMarginAmount
	verification.Comparables = comparableLimitExternal(comparables, 20)
	if market.Label != "" {
		verification.Pass = true
		verification.Verdict = ebaySoldVerdictPass
	} else {
		verification.Verdict = ebaySoldVerdictFail
		verification.Error = market.Reason
		if verification.Error == "" {
			verification.Error = fmt.Sprintf("sold comps gap %.1f%%/$%.0f below threshold %.1f%%/$%.0f", gapPct, gap, minGapPct, minGapDollars)
		}
	}
	return verification
}

func ebaySoldExternalComparable(observation ComputeObservation, listing ebay.SoldListing) ComputeExternalComparable {
	cleanTitle := cleanSoldQueryTitle(listing.Title)
	spec := ParseComputeSpec(Product{Name: listing.Title, SalePrice: listing.Price, Source: "ebay-sold"})
	return ComputeExternalComparable{
		Title:      listing.Title,
		CleanTitle: cleanTitle,
		Price:      listing.Price,
		Source:     "ebay-sold",
		Query:      buildEbaySoldQuery(observation),
		Spec:       spec,
	}
}

func comparableLimitExternal(comparables []ComputeExternalComparable, limit int) []ComputeExternalComparable {
	if len(comparables) <= limit || limit <= 0 {
		return comparables
	}
	return append([]ComputeExternalComparable(nil), comparables[:limit]...)
}

func ebay.SoldListingMatches(observation ComputeObservation, listing ebay.SoldListing) bool {
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
			if ratio < minimumComparableRAMRatio(spec) || ratio > 1.5 {
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

func ebaySoldExternalCommandArgs() []string {
	return scrapebackend.CommandArgsFromEnv("EBAY_SOLD_EXTERNAL_STEALTH_COMMAND_ARGS", "EBAY_COUPON_EXTERNAL_STEALTH_COMMAND_ARGS", "SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS")
}

func ebaySoldCamoufoxCommandArgs() []string {
	return scrapebackend.CommandArgsFromEnv("EBAY_SOLD_CAMOUFOX_COMMAND_ARGS", "EBAY_COUPON_CAMOUFOX_COMMAND_ARGS", "SCRAPELAB_CAMOUFOX_COMMAND_ARGS")
}

func ebaySoldAICrawlerCommandArgs() []string {
	return scrapebackend.CommandArgsFromEnv("EBAY_SOLD_AI_CRAWLER_COMMAND_ARGS", "EBAY_COUPON_AI_CRAWLER_COMMAND_ARGS", "SCRAPELAB_AI_CRAWLER_COMMAND_ARGS")
}

func ebaySoldPaidCommandArgs() []string {
	return scrapebackend.CommandArgsFromEnv("EBAY_SOLD_PAID_TRIAL_COMMAND_ARGS", "EBAY_COUPON_PAID_TRIAL_COMMAND_ARGS", "SCRAPELAB_PAID_TRIAL_COMMAND_ARGS")
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}
