package bestbuy

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const (
	defaultBestBuySoldCompCacheTTL   = 24 * time.Hour
	defaultBestBuySoldCompQueryDelay = 3 * time.Second
	defaultBestBuySoldCompMaxPerRun  = 10
	bestBuySoldCompMinPrice          = 100.0
	bestBuySoldCompStrongMinMatches  = 2
	bestBuySoldCompWeakMinMatches    = 3
	bestBuySoldCompExampleLimit      = 5

	soldCompMarketWarm = "warm"
	soldCompMarketHot  = "hot"

	bestBuyCandidateWarmMinGapPct = 15.0
	bestBuyCandidateHotMinGapPct  = 35.0
	ebaySoldWarmMinGapPct         = 15.0
	ebaySoldHotMinGapPct          = 30.0
	ebaySoldLocalMedianMultiplier = 0.85
)

type SoldCompListing struct {
	Title string  `docstore:"title" json:"title"`
	Price float64 `docstore:"price" json:"price"`
}

type SoldCompSnapshot struct {
	Key       string            `docstore:"key,omitempty"`
	Query     string            `docstore:"query"`
	Backend   string            `docstore:"backend,omitempty"`
	Verdict   string            `docstore:"verdict"`
	Error     string            `docstore:"error,omitempty"`
	Count     int               `docstore:"count,omitempty"`
	Median    float64           `docstore:"median,omitempty"`
	P25       float64           `docstore:"p25,omitempty"`
	GapAmount float64           `docstore:"gapAmount,omitempty"`
	GapPct    float64           `docstore:"gapPct,omitempty"`
	CheckedAt time.Time         `docstore:"checkedAt"`
	Examples  []SoldCompListing `docstore:"examples,omitempty"`
}

type SoldCompSnapshotStore interface {
	GetBestBuySoldCompSnapshot(ctx context.Context, key string) (SoldCompSnapshot, bool, error)
	SaveBestBuySoldCompSnapshot(ctx context.Context, key string, snapshot SoldCompSnapshot) error
}

type BestBuySoldCompEnricher interface {
	BeginRun()
	EnrichProducts(ctx context.Context, products []Product, now time.Time, logger *slog.Logger) ([]Product, error)
}

type SoldCompEnricherOptions struct {
	Enabled     bool
	Store       SoldCompSnapshotStore
	Backends    []string
	CacheTTL    time.Duration
	MaxPerRun   int
	QueryDelay  time.Duration
	PaidEnabled bool
	PaidAttempt func(context.Context) error
	BeforeRun   func()
	Timeout     time.Duration
	Sleep       contextSleeper
	FetchHTML   func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult
}

type EbaySoldCompsEnricher struct {
	enabled     bool
	store       SoldCompSnapshotStore
	backends    []string
	cacheTTL    time.Duration
	maxPerRun   int
	paidEnabled bool
	paidAttempt func(context.Context) error
	beforeRun   func()
	timeout     time.Duration
	limiter     *soldFetchLimiter
	fetchHTML   func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult
	mu          sync.Mutex
	queriesThis int
}

type contextSleeper func(context.Context, time.Duration) error

type soldFetchLimiter struct {
	delay   time.Duration
	sleep   contextSleeper
	mu      sync.Mutex
	fetches int
}

func ebaySoldBackends(configured []string, paidEnabled bool) []string {
	backends := compactStrings(configured)
	if len(backends) == 0 {
		backends = []string{scrapebackend.BackendHTTP, scrapebackend.BackendExternalStealth, scrapebackend.BackendCamoufox, scrapebackend.BackendAICrawler}
	}
	seen := make(map[string]bool, len(backends))
	ordered := make([]string, 0, len(backends))
	var paid []string
	for _, backend := range backends {
		if seen[backend] {
			continue
		}
		seen[backend] = true
		if backend == scrapebackend.BackendPaidTrial {
			if !paidEnabled {
				continue
			}
			paid = append(paid, backend)
			continue
		}
		ordered = append(ordered, backend)
	}
	return append(ordered, paid...)
}

func NewSoldCompEnricher(opts SoldCompEnricherOptions) *EbaySoldCompsEnricher {
	backends := ebaySoldBackends(opts.Backends, opts.PaidEnabled)
	cacheTTL := opts.CacheTTL
	if cacheTTL <= 0 {
		cacheTTL = defaultBestBuySoldCompCacheTTL
	}
	maxPerRun := opts.MaxPerRun
	if maxPerRun <= 0 {
		maxPerRun = defaultBestBuySoldCompMaxPerRun
	}
	queryDelay := opts.QueryDelay
	if queryDelay <= 0 {
		queryDelay = defaultBestBuySoldCompQueryDelay
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	fetchHTML := opts.FetchHTML
	if fetchHTML == nil {
		fetchHTML = scrapebackend.FetchHTML
	}
	return &EbaySoldCompsEnricher{
		enabled:     opts.Enabled,
		store:       opts.Store,
		backends:    backends,
		cacheTTL:    cacheTTL,
		maxPerRun:   maxPerRun,
		paidEnabled: opts.PaidEnabled,
		paidAttempt: opts.PaidAttempt,
		beforeRun:   opts.BeforeRun,
		timeout:     timeout,
		limiter:     newSoldFetchLimiter(queryDelay, opts.Sleep),
		fetchHTML:   fetchHTML,
	}
}

func newSoldFetchLimiter(delay time.Duration, sleep contextSleeper) *soldFetchLimiter {
	if sleep == nil {
		sleep = sleepContext
	}
	return &soldFetchLimiter{delay: delay, sleep: sleep}
}

func (l *soldFetchLimiter) BeginRun() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.fetches = 0
	l.mu.Unlock()
}

func (l *soldFetchLimiter) BeforeFetch(ctx context.Context) error {
	if l == nil || l.delay <= 0 {
		return nil
	}
	l.mu.Lock()
	shouldDelay := l.fetches > 0
	l.fetches++
	l.mu.Unlock()
	if !shouldDelay {
		return nil
	}
	return l.sleep(ctx, l.delay)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (e *EbaySoldCompsEnricher) BeginRun() {
	if e == nil {
		return
	}
	if e.beforeRun != nil {
		e.beforeRun()
	}
	if e.limiter != nil {
		e.limiter.BeginRun()
	}
	e.mu.Lock()
	e.queriesThis = 0
	e.mu.Unlock()
}

func (e *EbaySoldCompsEnricher) EnrichProducts(ctx context.Context, products []Product, now time.Time, logger *slog.Logger) ([]Product, error) {
	if e == nil || !e.enabled || len(products) == 0 {
		return products, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	out := append([]Product(nil), products...)
	candidates := make([]soldCompCandidate, 0, len(out))
	for i := range out {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		product := out[i]
		query := buildBestBuySoldCompQuery(product)
		if !eligibleForBestBuySoldComps(product, query) {
			continue
		}
		key := soldCompCacheKey(query)
		if key == "" {
			continue
		}
		if snapshot, cached := e.cachedSnapshot(ctx, key, now, logger); cached {
			applySoldCompSnapshot(&out[i], snapshot)
			continue
		}
		score, ok := bestBuySoldCompCandidateScore(product, query)
		if !ok {
			continue
		}
		candidates = append(candidates, soldCompCandidate{Index: i, Product: product, Query: query, Key: key, Score: score})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return soldCompCandidateLess(candidates[i], candidates[j])
	})

	for i, candidate := range candidates {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if !e.reserveQuerySlot() {
			logger.Info("Best Buy eBay sold comp cap reached", "max_per_run", e.maxPerRun, "remaining_uncached_candidates", len(candidates)-i)
			break
		}
		snapshot := e.fetchSnapshot(ctx, candidate.Product, candidate.Query, candidate.Key, now, logger)
		e.saveSnapshot(ctx, candidate.Key, snapshot, logger)
		applySoldCompSnapshot(&out[candidate.Index], snapshot)
	}
	return out, nil
}

type soldCompCandidate struct {
	Index   int
	Product Product
	Query   string
	Key     string
	Score   soldCompCandidateScore
}

type soldCompCandidateScore struct {
	MarketLabel      string
	Score            float64
	GapPct           float64
	GapAmount        float64
	ComparableCount  int
	AtOrBelowP25     bool
	HighValueCompute bool
	Confidence       int
}

func bestBuySoldCompCandidateScore(product Product, query string) (soldCompCandidateScore, bool) {
	current := effectiveProductPrice(product)
	if current <= 0 {
		return soldCompCandidateScore{}, false
	}
	spec := ParseComputeSpec(product)
	score := soldCompCandidateScore{
		ComparableCount:  product.ComparableCount,
		HighValueCompute: spec.IsCompute && (isExtremeComputeSpec(spec, current) || current >= 500),
		Confidence:       soldCompMatchConfidence(product, query),
	}
	score.Score = float64(score.Confidence) * 10
	if score.HighValueCompute {
		score.Score += 75
	}

	if product.ComparableCount > 0 && product.ComparableMedianPrice > 0 {
		gap := product.ComparableMedianPrice - current
		gapPct := percentGap(gap, product.ComparableMedianPrice)
		score.GapAmount = gap
		score.GapPct = gapPct
		score.MarketLabel = bestBuyPreEbayMarketLabel(product)
		if score.MarketLabel == "" {
			return score, false
		}
		score.Score += math.Max(gap, 0)/10 + math.Max(gapPct, 0)*2 + float64(product.ComparableCount)*4
		if product.ComparableLowestPrice > 0 && current <= product.ComparableLowestPrice+priceComparisonEpsilon {
			score.AtOrBelowP25 = true
			score.Score += 35
		}
		if score.MarketLabel == soldCompMarketHot {
			score.Score += 100
		} else if score.MarketLabel == soldCompMarketWarm {
			score.Score += 50
		}
		return score, true
	}

	// Tier-1 AI already selected this item. If Best Buy has no active comps, keep it
	// eligible but lower-priority so eBay can still settle sparse-market items.
	score.Score += math.Min(current/25, 40)
	return score, true
}

func soldCompCandidateLess(a, b soldCompCandidate) bool {
	if marketRank(a.Score.MarketLabel) != marketRank(b.Score.MarketLabel) {
		return marketRank(a.Score.MarketLabel) > marketRank(b.Score.MarketLabel)
	}
	if a.Score.Score != b.Score.Score {
		return a.Score.Score > b.Score.Score
	}
	if a.Score.GapAmount != b.Score.GapAmount {
		return a.Score.GapAmount > b.Score.GapAmount
	}
	if a.Score.ComparableCount != b.Score.ComparableCount {
		return a.Score.ComparableCount > b.Score.ComparableCount
	}
	if a.Score.Confidence != b.Score.Confidence {
		return a.Score.Confidence > b.Score.Confidence
	}
	return a.Index < b.Index
}

func marketRank(label string) int {
	switch label {
	case soldCompMarketHot:
		return 2
	case soldCompMarketWarm:
		return 1
	default:
		return 0
	}
}

func bestBuyPreEbayMarketLabel(product Product) string {
	current := effectiveProductPrice(product)
	if current <= 0 || product.ComparableMedianPrice <= 0 || current >= product.ComparableMedianPrice-priceComparisonEpsilon {
		return ""
	}
	gap := product.ComparableMedianPrice - current
	gapPct := percentGap(gap, product.ComparableMedianPrice)
	if gapPct >= bestBuyCandidateHotMinGapPct && gap >= marketHotMinGapAmount(product.ComparableMedianPrice) {
		return soldCompMarketHot
	}
	if gapPct >= bestBuyCandidateWarmMinGapPct && gap >= marketWarmMinGapAmount(product.ComparableMedianPrice) {
		return soldCompMarketWarm
	}
	return ""
}

func soldCompMatchConfidence(product Product, query string) int {
	spec := ParseComputeSpec(product)
	if spec.IsCompute && spec.Model != "" {
		return 3
	}
	if product.BrandName != "" && normalizeSoldCompModel(product.ModelNumber) != "" {
		return 3
	}
	if normalizeSoldCompModel(product.ModelNumber) != "" {
		return 2
	}
	if product.BrandName != "" || len(meaningfulTokens(query)) >= 3 {
		return 2
	}
	return 1
}

func (e *EbaySoldCompsEnricher) reserveQuerySlot() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.maxPerRun > 0 && e.queriesThis >= e.maxPerRun {
		return false
	}
	e.queriesThis++
	return true
}

func (e *EbaySoldCompsEnricher) cachedSnapshot(ctx context.Context, key string, now time.Time, logger *slog.Logger) (SoldCompSnapshot, bool) {
	if e.store == nil || key == "" {
		return SoldCompSnapshot{}, false
	}
	snapshot, ok, err := e.store.GetBestBuySoldCompSnapshot(ctx, key)
	if err != nil {
		logger.Warn("Best Buy eBay sold comp cache read failed", "key", key, "error", err)
		return SoldCompSnapshot{}, false
	}
	if !ok || snapshot.CheckedAt.IsZero() || e.cacheTTL <= 0 || now.Sub(snapshot.CheckedAt) > e.cacheTTL {
		return SoldCompSnapshot{}, false
	}
	return snapshot, true
}

func (e *EbaySoldCompsEnricher) saveSnapshot(ctx context.Context, key string, snapshot SoldCompSnapshot, logger *slog.Logger) {
	if e.store == nil || key == "" {
		return
	}
	if err := e.store.SaveBestBuySoldCompSnapshot(ctx, key, snapshot); err != nil {
		logger.Warn("Best Buy eBay sold comp cache write failed", "key", key, "error", err)
	}
}

func (e *EbaySoldCompsEnricher) fetchSnapshot(ctx context.Context, product Product, query, key string, now time.Time, logger *slog.Logger) SoldCompSnapshot {
	searchURL := ebay.SoldSearchURL(query)
	var failures []string
	best := SoldCompSnapshot{Key: key, Query: query, Verdict: ebaySoldVerdictNoComps, CheckedAt: now}
	attempts := scrapebackend.NewAttemptCounter()
	for _, backend := range e.backends {
		attempts.RecordAttempt(backend)
		if err := e.limiter.BeforeFetch(ctx); err != nil {
			attempts.RecordError(backend)
			snapshot := SoldCompSnapshot{Key: key, Query: query, Verdict: ebaySoldVerdictFetchError, Error: err.Error(), CheckedAt: now}
			logBestBuySoldCompBackendSummary(logger, product, snapshot, attempts)
			return snapshot
		}
		result := e.fetchHTML(ctx, scrapebackend.FetchOptions{
			Backend:             backend,
			URL:                 searchURL,
			Timeout:             e.timeout,
			ExternalCommand:     ebaySoldExternalCommand(),
			ExternalCommandArgs: ebaySoldExternalCommandArgs(),
			CamoufoxCommand:     ebaySoldCamoufoxCommand(),
			CamoufoxCommandArgs: ebaySoldCamoufoxCommandArgs(),
			AICrawlerCommand:    ebaySoldAICrawlerCommand(),
			AICrawlerArgs:       ebaySoldAICrawlerCommandArgs(),
			PaidCommand:         ebaySoldPaidCommand(),
			PaidCommandArgs:     ebaySoldPaidCommandArgs(),
			PaidEnabled:         e.paidEnabled,
			PaidAttempt:         e.paidAttempt,
		})
		if issue := attempts.RecordFetchResult(backend, result); issue != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", backend, issue))
			if logger != nil {
				logger.Warn("Best Buy eBay sold comp backend failed",
					"sku", product.SKU,
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
			failures = append(failures, fmt.Sprintf("%s: parse: %s", backend, err))
			if logger != nil {
				logger.Warn("Best Buy eBay sold comp parse failed",
					"sku", product.SKU,
					"query", query,
					"backend", backend,
					"error", err,
				)
			}
			continue
		}
		snapshot := scoreBestBuySoldComps(product, listings, query, backend, key, now)
		attempts.RecordVerdict(backend, snapshot.Verdict)
		if logger != nil {
			logger.Info("Best Buy eBay sold comp fetch complete", "sku", product.SKU, "query", query, "backend", backend, "verdict", snapshot.Verdict, "sold_comps", snapshot.Count, "sold_median", snapshot.Median)
		}
		if snapshot.Count > 0 {
			logBestBuySoldCompBackendSummary(logger, product, snapshot, attempts)
			return snapshot
		}
		if snapshot.Count > best.Count || best.Backend == "" {
			best = snapshot
		}
	}
	if best.Count > 0 {
		logBestBuySoldCompBackendSummary(logger, product, best, attempts)
		return best
	}
	if len(failures) > 0 {
		snapshot := SoldCompSnapshot{Key: key, Query: query, Verdict: ebaySoldVerdictFetchError, Error: soldFetchFailureSummary(failures), CheckedAt: now}
		logBestBuySoldCompBackendSummary(logger, product, snapshot, attempts)
		return snapshot
	}
	logBestBuySoldCompBackendSummary(logger, product, best, attempts)
	return best
}

func logBestBuySoldCompBackendSummary(logger *slog.Logger, product Product, snapshot SoldCompSnapshot, attempts *scrapebackend.AttemptCounter) {
	if logger == nil || attempts == nil || attempts.TotalAttempts() == 0 {
		return
	}
	attrs := []any{
		"sku", product.SKU,
		"query", snapshot.Query,
		"backend", snapshot.Backend,
		"verdict", snapshot.Verdict,
		"sold_comps", snapshot.Count,
		"error", snapshot.Error,
	}
	attrs = append(attrs, attempts.Attrs()...)
	logger.Info("Best Buy eBay sold comp backend summary", attrs...)
}

func buildBestBuySoldCompQuery(product Product) string {
	if query := buildAppleWatchSoldCompQuery(product); query != "" {
		return query
	}
	spec := ParseComputeSpec(product)
	if spec.IsCompute && spec.RejectReason == "" {
		query := buildEbaySoldQuery(ComputeObservation{Product: product, Spec: spec})
		if strings.TrimSpace(query) != "" {
			return query
		}
	}
	if product.BrandName != "" && product.ModelNumber != "" {
		return strings.TrimSpace(product.BrandName + " " + product.ModelNumber)
	}
	if product.ModelNumber != "" {
		return strings.TrimSpace(product.ModelNumber)
	}
	query := cleanSoldQueryTitle(product.Name)
	if len(meaningfulTokens(query)) >= 2 {
		return query
	}
	return query
}

func buildAppleWatchSoldCompQuery(product Product) string {
	haystack := strings.Join(compactStrings([]string{product.Name, product.CategoryName, product.BrandName, product.ModelNumber, bestBuySpecValue(product, "smartwatchfitnesstrackergrouping")}), " ")
	if !strings.Contains(strings.ToLower(haystack), "apple watch") {
		return ""
	}

	series := appleWatchSeriesFromText(firstNonEmpty(bestBuySpecValue(product, "watchseries"), product.Name, product.CategoryName))
	if series == "" {
		return ""
	}
	connectivity := appleWatchConnectivityFromText(firstNonEmpty(bestBuySpecValue(product, "connectivity"), bestBuySpecValue(product, "watchseries"), product.Name))
	size := appleWatchCaseSizeMM(product)

	parts := []string{"Apple Watch", series}
	if connectivity != "" {
		parts = append(parts, connectivity)
	}
	if size != "" {
		parts = append(parts, size+"mm")
	}
	return strings.Join(parts, " ")
}

func bestBuySpecValue(product Product, keyPart string) string {
	keyPart = strings.ToLower(keyPart)
	for key, value := range product.Specs {
		if strings.Contains(strings.ToLower(key), keyPart) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func appleWatchSeriesFromText(value string) string {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "apple watch se") || regexp.MustCompile(`(?i)\bwatch\s+se\b`).MatchString(value) {
		return "SE"
	}
	if strings.Contains(lower, "ultra") {
		if match := regexp.MustCompile(`(?i)\bultra\s*(\d+)\b`).FindStringSubmatch(value); len(match) > 1 {
			return "Ultra " + match[1]
		}
		return "Ultra"
	}
	match := regexp.MustCompile(`(?i)\bseries\s*(\d+)\b`).FindStringSubmatch(value)
	if len(match) > 1 {
		return "Series " + match[1]
	}
	return ""
}

func appleWatchConnectivityFromText(value string) string {
	lower := strings.ToLower(value)
	if strings.Contains(lower, "cellular") || strings.Contains(lower, "lte") {
		return "GPS Cellular"
	}
	if strings.Contains(lower, "gps") {
		return "GPS"
	}
	return ""
}

func appleWatchCaseSizeMM(product Product) string {
	value := firstNonEmpty(bestBuySpecValue(product, "casediametermm"), product.Name)
	match := regexp.MustCompile(`(?i)\b(38|40|41|42|44|45|49)\s*mm\b`).FindStringSubmatch(value)
	if len(match) > 1 {
		return match[1]
	}
	match = regexp.MustCompile(`^\s*(38|40|41|42|44|45|49)\s*$`).FindStringSubmatch(value)
	if len(match) > 1 {
		return match[1]
	}
	return ""
}

func eligibleForBestBuySoldComps(product Product, query string) bool {
	if strings.TrimSpace(query) == "" {
		return false
	}
	if effectiveProductPrice(product) < bestBuySoldCompMinPrice {
		return false
	}
	return !containsPoorResaleKeyword(product.Name + " " + product.CategoryName)
}

func soldCompCacheKey(query string) string {
	normalized := normalizeSoldCompQuery(query)
	if normalized == "" {
		return ""
	}
	sum := sha1.Sum([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func normalizeSoldCompQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(query))), " ")
}

func scoreBestBuySoldComps(product Product, listings []ebay.SoldListing, query, backend, key string, now time.Time) SoldCompSnapshot {
	snapshot := SoldCompSnapshot{Key: key, Query: query, Backend: backend, Verdict: ebaySoldVerdictNoComps, CheckedAt: now}
	current := effectiveProductPrice(product)
	if current <= 0 {
		snapshot.Error = "missing current price"
		return snapshot
	}
	var prices []float64
	var examples []SoldCompListing
	for _, listing := range listings {
		if !bestBuySoldListingMatches(product, listing) {
			continue
		}
		prices = append(prices, listing.Price)
		examples = append(examples, SoldCompListing{Title: listing.Title, Price: listing.Price})
	}
	snapshot.Count = len(prices)
	minMatches := bestBuySoldCompMinMatches(product)
	if len(prices) < minMatches {
		snapshot.Error = fmt.Sprintf("not enough matching sold comps: %d/%d", len(prices), minMatches)
		return snapshot
	}
	sort.Float64s(prices)
	median := percentileSorted(prices, 0.50)
	p25 := percentileSorted(prices, 0.25)
	verdict := soldCompMarketVerdictFromSummary(current, len(prices), median, p25, minMatches)
	snapshot.Median = median
	snapshot.P25 = p25
	snapshot.GapAmount = verdict.GapAmount
	snapshot.GapPct = verdict.GapPct
	snapshot.Examples = soldCompExampleLimit(examples, bestBuySoldCompExampleLimit)
	if verdict.Label == "" {
		snapshot.Verdict = ebaySoldVerdictFail
		snapshot.Error = verdict.Reason
		return snapshot
	}
	snapshot.Verdict = ebaySoldVerdictPass
	return snapshot
}

func bestBuySoldListingMatches(product Product, listing ebay.SoldListing) bool {
	title := strings.ToLower(listing.Title)
	if containsPoorResaleKeyword(title) {
		return false
	}
	if product.ModelNumber != "" {
		model := normalizeSoldCompModel(product.ModelNumber)
		if len(model) >= 3 {
			return strings.Contains(normalizeSoldCompModel(title), model)
		}
	}
	if product.BrandName != "" && !strings.Contains(title, strings.ToLower(product.BrandName)) {
		return false
	}
	return tokenOverlap(cleanSoldQueryTitle(product.Name), listing.Title) >= 0.35
}

func normalizeSoldCompModel(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func bestBuySoldCompMinMatches(product Product) int {
	spec := ParseComputeSpec(product)
	if spec.IsCompute && spec.Model != "" {
		return bestBuySoldCompStrongMinMatches
	}
	if product.BrandName != "" && len(normalizeSoldCompModel(product.ModelNumber)) >= 3 {
		return bestBuySoldCompStrongMinMatches
	}
	if len(normalizeSoldCompModel(product.ModelNumber)) >= 5 {
		return bestBuySoldCompStrongMinMatches
	}
	return bestBuySoldCompWeakMinMatches
}

type soldCompMarketVerdict struct {
	Label             string
	EnoughEvidence    bool
	Count             int
	Median            float64
	P25               float64
	GapAmount         float64
	GapPct            float64
	LocalResalePrice  float64
	LocalMarginAmount float64
	LocalMarginPct    float64
	Reason            string
}

func soldCompMarketVerdictFromPrices(current float64, prices []float64, minComps int) soldCompMarketVerdict {
	if len(prices) == 0 {
		return soldCompMarketVerdictFromSummary(current, 0, 0, 0, minComps)
	}
	sorted := append([]float64(nil), prices...)
	sort.Float64s(sorted)
	return soldCompMarketVerdictFromSummary(current, len(sorted), percentileSorted(sorted, 0.50), percentileSorted(sorted, 0.25), minComps)
}

func soldCompMarketVerdictFromSummary(current float64, count int, median, p25 float64, minComps int) soldCompMarketVerdict {
	if minComps <= 0 {
		minComps = bestBuySoldCompWeakMinMatches
	}
	verdict := soldCompMarketVerdict{
		Count:  count,
		Median: median,
		P25:    p25,
	}
	if current <= 0 {
		verdict.Reason = "missing current price"
		return verdict
	}
	if count < minComps {
		verdict.Reason = fmt.Sprintf("not enough matching sold comps: %d/%d", count, minComps)
		return verdict
	}
	if median <= 0 {
		verdict.Reason = "missing sold median"
		return verdict
	}

	verdict.EnoughEvidence = true
	verdict.GapAmount = median - current
	verdict.GapPct = percentGap(verdict.GapAmount, median)
	localResale := median * ebaySoldLocalMedianMultiplier
	if p25 > 0 && p25 < localResale {
		localResale = p25
	}
	if localResale <= 0 {
		localResale = median
	}
	verdict.LocalResalePrice = localResale
	verdict.LocalMarginAmount = localResale - current
	verdict.LocalMarginPct = percentGap(verdict.LocalMarginAmount, localResale)

	warmOK := verdict.GapPct >= ebaySoldWarmMinGapPct && verdict.GapAmount >= marketWarmMinGapAmount(median)
	hotOK := verdict.LocalMarginPct >= ebaySoldHotMinGapPct && verdict.LocalMarginAmount >= marketHotMinGapAmount(localResale)
	switch {
	case hotOK:
		verdict.Label = soldCompMarketHot
	case warmOK:
		verdict.Label = soldCompMarketWarm
	default:
		verdict.Reason = fmt.Sprintf("sold comps gap %.1f%%/$%.0f below warm threshold %.1f%%/$%.0f", verdict.GapPct, verdict.GapAmount, ebaySoldWarmMinGapPct, marketWarmMinGapAmount(median))
	}
	return verdict
}

func percentGap(gap, reference float64) float64 {
	if reference <= 0 {
		return 0
	}
	return gap / reference * 100
}

func marketWarmMinGapAmount(reference float64) float64 {
	switch {
	case reference < 300:
		return 40
	case reference < 1000:
		return 75
	default:
		return 150
	}
}

func marketHotMinGapAmount(reference float64) float64 {
	switch {
	case reference < 300:
		return 100
	case reference < 1000:
		return 200
	default:
		return 400
	}
}

func applySoldCompSnapshot(product *Product, snapshot SoldCompSnapshot) {
	if product == nil || snapshot.Count <= 0 || snapshot.Median <= 0 {
		return
	}
	product.SoldCompCount = snapshot.Count
	product.SoldCompMedianPrice = snapshot.Median
	product.SoldCompP25Price = snapshot.P25
	product.SoldCompGapAmount = snapshot.GapAmount
	product.SoldCompGapPct = snapshot.GapPct
	product.SoldCompCheckedAt = snapshot.CheckedAt
	product.SoldCompExamples = soldCompExampleLimit(snapshot.Examples, bestBuySoldCompExampleLimit)
	product.SoldCompSummary = formatSoldCompSummary(snapshot)
}

func formatSoldCompSummary(snapshot SoldCompSnapshot) string {
	if snapshot.Count <= 0 || snapshot.Median <= 0 {
		return ""
	}
	if snapshot.GapAmount >= 0 {
		return fmt.Sprintf("eBay sold comps: $%.2f median / $%.2f p25 across %d sold listings; candidate is %.0f%% ($%.0f) below median.", snapshot.Median, snapshot.P25, snapshot.Count, math.Max(snapshot.GapPct, 0), math.Max(snapshot.GapAmount, 0))
	}
	return fmt.Sprintf("eBay sold comps: $%.2f median / $%.2f p25 across %d sold listings; candidate is %.0f%% ($%.0f) above median.", snapshot.Median, snapshot.P25, snapshot.Count, math.Abs(snapshot.GapPct), math.Abs(snapshot.GapAmount))
}

func soldCompExampleLimit(examples []SoldCompListing, limit int) []SoldCompListing {
	if limit <= 0 || len(examples) <= limit {
		return append([]SoldCompListing(nil), examples...)
	}
	return append([]SoldCompListing(nil), examples[:limit]...)
}

func containsPoorResaleKeyword(value string) bool {
	value = strings.ToLower(value)
	keywords := []string{
		"accessory", "adapter", "battery", "cable", "cartridge", "case", "charger", "cover", "filter", "gift card", "ink", "keyboard", "mouse", "mount", "paper", "protector", "screen protector", "sleeve", "stand", "strap", "toner", "warranty",
	}
	for _, keyword := range keywords {
		if strings.Contains(value, keyword) {
			return true
		}
	}
	return false
}

func soldFetchFailureSummary(failures []string) string {
	const maxFailures = 4
	if len(failures) == 0 {
		return ""
	}
	if len(failures) <= maxFailures {
		return strings.Join(failures, "; ")
	}
	summary := append([]string(nil), failures[:maxFailures]...)
	summary = append(summary, fmt.Sprintf("%d more failures", len(failures)-maxFailures))
	return strings.Join(summary, "; ")
}
