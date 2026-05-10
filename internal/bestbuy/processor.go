package bestbuy

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/productmonitor"
)

const (
	// batchSize is the number of items to send to Gemini tier-1 screening at once.
	batchSize              = 10
	bestBuyMaxRecords      = 10000
	priceDropMinPct        = 20
	priceDropMinAmount     = 50
	priceComparisonEpsilon = 0.005
)

// Store abstracts persistence operations for the Best Buy processor.
type Store interface {
	GetActiveBestBuySellers(ctx context.Context) ([]Seller, error)
	SeedBestBuySellers(ctx context.Context) (bool, error)
	GetBestBuyProduct(ctx context.Context, sku, source string) (AnalyzedProduct, bool, error)
	SaveBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	RefreshBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for Best Buy products.
type Analyzer interface {
	ScreenBestBuyBatch(ctx context.Context, products []Product) ([]BatchScreenResult, error)
	AnalyzeBestBuyBatch(ctx context.Context, products []Product) ([]BatchAnalyzeResult, error)
	AnalyzeBestBuyProduct(ctx context.Context, product Product) (*AnalyzeResult, error)
}

// Notifier abstracts Discord notifications for Best Buy deals.
type Notifier interface {
	SendBestBuyDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
}

type priceDropEvaluation struct {
	Candidate bool
	Amount    float64
	Pct       float64
}

type priceDropCandidate struct {
	State AnalyzedProduct
}

// BaselineStats summarizes a no-notification Best Buy inventory prime.
type BaselineStats struct {
	Sellers     int    `json:"sellers"`
	Fetched     int    `json:"fetched"`
	Existing    int    `json:"existing"`
	Saved       int    `json:"saved"`
	FetchErrors int    `json:"fetchErrors"`
	ExitReason  string `json:"exitReason"`
}

// Processor handles the Best Buy deal processing pipeline.
type Processor struct {
	store           Store
	client          *Client
	analyzer        Analyzer
	notifier        Notifier
	affiliatePrefix string
	mu              sync.Mutex
}

// NewProcessor creates a new Best Buy deal processor.
func NewProcessor(store Store, client *Client, analyzer Analyzer, notifier Notifier, affiliatePrefix string) *Processor {
	return &Processor{
		store:           store,
		client:          client,
		analyzer:        analyzer,
		notifier:        notifier,
		affiliatePrefix: affiliatePrefix,
	}
}

// PrimeBaseline saves the current configured-seller inventory without sending
// Discord notifications. Run this before enabling Best Buy subscriptions or the
// scheduler so historical seller inventory does not blast a channel on first run.
func (p *Processor) PrimeBaseline(ctx context.Context) (BaselineStats, error) {
	if !p.mu.TryLock() {
		slog.Info("PrimeBestBuyBaseline: already in progress, skipping", "processor", "bestbuy")
		return BaselineStats{ExitReason: "already_running"}, nil
	}
	defer p.mu.Unlock()

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "bestbuy", "runID", runID, "mode", "baseline")
	stats := BaselineStats{}
	defer func() {
		logger.Info("Best Buy baseline prime complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.Sellers,
			"fetched", stats.Fetched,
			"existing", stats.Existing,
			"saved", stats.Saved,
			"fetch_errors", stats.FetchErrors,
			"exit_reason", stats.ExitReason,
		)
	}()

	if seeded, err := p.store.SeedBestBuySellers(ctx); err != nil {
		logger.Warn("Failed to seed Best Buy sellers", "error", err)
	} else if seeded {
		logger.Info("Seeded Best Buy sellers with defaults")
	}

	sellers, err := p.store.GetActiveBestBuySellers(ctx)
	if err != nil {
		stats.ExitReason = "seller_load_error"
		return stats, fmt.Errorf("failed to get active Best Buy sellers: %w", err)
	}
	stats.Sellers = len(sellers)
	if len(sellers) == 0 {
		stats.ExitReason = "no_active_sellers"
		return stats, nil
	}

	for i, seller := range sellers {
		if ctx.Err() != nil {
			stats.ExitReason = "context_cancelled"
			return stats, ctx.Err()
		}

		products, err := p.client.FetchSellerProducts(ctx, seller)
		if err != nil {
			stats.FetchErrors++
			logger.Error("Failed to fetch seller products",
				"seller", seller.Name,
				"sellerID", seller.ID,
				"error", err,
			)
			continue
		}
		stats.Fetched += len(products)

		for _, product := range products {
			if ctx.Err() != nil {
				stats.ExitReason = "context_cancelled"
				return stats, ctx.Err()
			}

			_, exists, err := p.store.GetBestBuyProduct(ctx, product.SKU, product.Source)
			if err != nil {
				if ctx.Err() != nil {
					stats.ExitReason = "context_cancelled"
					return stats, ctx.Err()
				}
				logger.Warn("Failed to check product existence",
					"sku", product.SKU,
					"source", product.Source,
					"error", err,
				)
				continue
			}
			if exists {
				stats.Existing++
				continue
			}

			if p.affiliatePrefix != "" && product.URL != "" {
				product.URL = p.affiliatePrefix + url.QueryEscape(product.URL)
			}

			item := bestBuyFallback(product)
			if err := p.store.SaveBestBuyProduct(ctx, item); err != nil {
				if ctx.Err() != nil {
					stats.ExitReason = "context_cancelled"
					return stats, ctx.Err()
				}
				logger.Error("Failed to save Best Buy baseline listing",
					"sku", item.SKU,
					"title", item.Name,
					"error", err,
				)
				continue
			}
			stats.Saved++
		}

		if i < len(sellers)-1 {
			select {
			case <-ctx.Done():
				stats.ExitReason = "context_cancelled"
				return stats, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	if err := p.store.PruneBestBuyProducts(ctx, 30, bestBuyMaxRecords); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.ExitReason = "success"
	return stats, nil
}

// ProcessBestBuyDeals runs the full Best Buy deal processing pipeline.
func (p *Processor) ProcessBestBuyDeals(ctx context.Context) error {
	if !p.mu.TryLock() {
		slog.Info("ProcessBestBuyDeals: already in progress, skipping", "processor", "bestbuy")
		return nil
	}
	defer p.mu.Unlock()

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "bestbuy", "runID", runID)

	var stats struct {
		sellers             int
		fetched             int
		newItems            int
		tier1Passed         int
		tier2Passed         int
		warmHot             int
		priceDropCandidates int
		priceDropAnalyzed   int
		priceDropNotified   int
		notified            int
		notifyErrors        int
		exitReason          string
	}

	defer func() {
		logger.Info("Best Buy pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.sellers,
			"fetched", stats.fetched,
			"new_items", stats.newItems,
			"price_drop_candidates", stats.priceDropCandidates,
			"price_drop_analyzed", stats.priceDropAnalyzed,
			"price_drop_notified", stats.priceDropNotified,
			"tier1_passed", stats.tier1Passed,
			"tier2_passed", stats.tier2Passed,
			"warm_hot", stats.warmHot,
			"notified", stats.notified,
			"notify_errors", stats.notifyErrors,
			"exit_reason", stats.exitReason,
		)
	}()

	// 1. Get subscriptions filtered for bestbuy
	allSubs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		stats.exitReason = "subscription_load_error"
		return fmt.Errorf("failed to get subscriptions: %w", err)
	}

	var bbSubs []models.Subscription
	for _, sub := range allSubs {
		if sub.IsBestBuy() {
			bbSubs = append(bbSubs, sub)
		}
	}

	if len(bbSubs) == 0 {
		logger.Info("No Best Buy subscriptions found; polling will refresh inventory without notifications")
	}

	if seeded, err := p.store.SeedBestBuySellers(ctx); err != nil {
		logger.Warn("Failed to seed Best Buy sellers", "error", err)
	} else if seeded {
		logger.Info("Seeded Best Buy sellers with defaults")
	}

	sellers, err := p.store.GetActiveBestBuySellers(ctx)
	if err != nil {
		stats.exitReason = "seller_load_error"
		return fmt.Errorf("failed to get active Best Buy sellers: %w", err)
	}
	if len(sellers) == 0 {
		stats.exitReason = "no_active_sellers"
		logger.Info("No active Best Buy sellers configured")
		return nil
	}

	// 2. Fetch products from all configured sellers.
	var allProducts []Product
	stats.sellers = len(sellers)

	for i, seller := range sellers {
		if ctx.Err() != nil {
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		}

		products, err := p.client.FetchSellerProducts(ctx, seller)
		if err != nil {
			logger.Error("Failed to fetch seller products",
				"seller", seller.Name,
				"sellerID", seller.ID,
				"error", err,
			)
			continue
		}
		logger.Info("Fetched seller products",
			"seller", seller.Name,
			"count", len(products),
		)
		allProducts = append(allProducts, products...)

		// Delay between sellers
		if i < len(sellers)-1 {
			select {
			case <-ctx.Done():
				stats.exitReason = "context_cancelled"
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}

	stats.fetched = len(allProducts)

	if len(allProducts) == 0 {
		stats.exitReason = "no_products"
		return nil
	}

	// 4. Dedup — collect new products
	var newProducts []Product
	var priceDropCandidates []priceDropCandidate
	hasPriceDropSubs := hasBestBuyPriceDropSubscriptions(bbSubs)
	for _, product := range allProducts {
		if ctx.Err() != nil {
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		}

		// Wrap product URL with affiliate prefix before saving or refreshing.
		if p.affiliatePrefix != "" && product.URL != "" {
			product.URL = p.affiliatePrefix + url.QueryEscape(product.URL)
		}

		existing, exists, err := p.store.GetBestBuyProduct(ctx, product.SKU, product.Source)
		if err != nil {
			logger.Warn("Failed to check product existence",
				"sku", product.SKU,
				"source", product.Source,
				"error", err,
			)
			continue
		}
		if exists {
			refreshed, eval := refreshExistingBestBuyProduct(existing, product, time.Now())
			if eval.Candidate {
				stats.priceDropCandidates++
				if hasPriceDropSubs {
					refreshed.AlertKind = AlertKindPriceDrop
					refreshed.PriceDropAmount = eval.Amount
					refreshed.PriceDropPct = eval.Pct
					priceDropCandidates = append(priceDropCandidates, priceDropCandidate{State: refreshed})
					continue
				}
			}
			if err := p.store.RefreshBestBuyProduct(ctx, refreshed); err != nil {
				logger.Warn("Failed to refresh existing Best Buy listing",
					"sku", product.SKU,
					"source", product.Source,
					"error", err,
				)
			}
			continue
		}

		newProducts = append(newProducts, product)
	}
	stats.newItems = len(newProducts)

	if len(newProducts) == 0 && len(priceDropCandidates) == 0 {
		stats.exitReason = "no_new_items"
		// Still prune
		if err := p.store.PruneBestBuyProducts(ctx, 30, bestBuyMaxRecords); err != nil {
			logger.Warn("Failed to prune old products", "error", err)
		}
		return nil
	}

	// 5. AI-label each new item, save it, then notify according to subscription tier.
	analyzedItems, tier1Passed, tier2Passed := p.analyzeNewItems(ctx, newProducts, logger)
	stats.tier1Passed += tier1Passed
	stats.tier2Passed += tier2Passed
	for _, item := range analyzedItems {
		if item.IsWarm || item.IsLavaHot {
			stats.warmHot++
		}

		eligibleSubs := filterEligibleSubs(item, bbSubs)
		if len(eligibleSubs) == 0 {
			continue
		}

		if p.notifier != nil {
			if err := p.notifier.SendBestBuyDeal(ctx, item, eligibleSubs); err != nil {
				logger.Error("Failed to send deal notification",
					"sku", item.SKU,
					"title", item.CleanTitle,
					"error", err,
				)
				stats.notifyErrors++
			} else {
				stats.notified++
			}
		}
	}

	analyzedDrops, dropTier1, dropTier2 := p.analyzePriceDropItems(ctx, priceDropCandidates, logger)
	stats.tier1Passed += dropTier1
	stats.tier2Passed += dropTier2
	stats.priceDropAnalyzed = len(analyzedDrops)
	for _, item := range analyzedDrops {
		if item.IsWarm || item.IsLavaHot {
			stats.warmHot++
		}

		eligibleSubs := filterPriceDropEligibleSubs(item, bbSubs)
		if len(eligibleSubs) == 0 {
			continue
		}

		if p.notifier != nil {
			if err := p.notifier.SendBestBuyDeal(ctx, item, eligibleSubs); err != nil {
				logger.Error("Failed to send price-drop notification",
					"sku", item.SKU,
					"title", item.CleanTitle,
					"error", err,
				)
				stats.notifyErrors++
				continue
			}
		}

		now := time.Now()
		currentPrice := effectivePrice(item.Product)
		item.LastPriceDropAlertPrice = currentPrice
		item.LastPriceDropAlertAt = now
		item.LastPriceDropAlertKey = priceDropAlertKey(item.SKU, item.Source, currentPrice)
		item.AlertKind = AlertKindPriceDrop
		item.PriceDropAmount = item.InitialEffectivePrice - currentPrice
		if item.InitialEffectivePrice > 0 {
			item.PriceDropPct = item.PriceDropAmount / item.InitialEffectivePrice * 100
		}
		if err := p.store.SaveBestBuyProduct(ctx, item); err != nil {
			logger.Warn("Failed to persist Best Buy price-drop alert state",
				"sku", item.SKU,
				"source", item.Source,
				"error", err,
			)
		}
		stats.priceDropNotified++
		stats.notified++
	}

	// 6. Prune old records
	if err := p.store.PruneBestBuyProducts(ctx, 30, bestBuyMaxRecords); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.exitReason = "success"
	return nil
}

// analyzeNewItems runs the tiered AI analysis pipeline on new products.
// It returns every saved listing so bb_new can post all new inventory with
// labels, while bb_warm_hot and bb_hot can filter on the same AI fields.
func (p *Processor) analyzeNewItems(ctx context.Context, products []Product, logger *slog.Logger) ([]AnalyzedProduct, int, int) {
	var analyzedItems []AnalyzedProduct
	totalTier1 := 0
	totalTier2 := 0

	for i := 0; i < len(products); i += batchSize {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping batch analysis", "processed", i, "total", len(products))
			break
		}

		// Inter-batch delay
		if i > 0 {
			select {
			case <-ctx.Done():
				logger.Warn("Context cancelled during inter-batch delay", "processed", i, "total", len(products))
				return analyzedItems, totalTier1, totalTier2
			case <-time.After(2 * time.Second):
			}
		}

		end := i + batchSize
		if end > len(products) {
			end = len(products)
		}
		batch := products[i:end]

		batchResults, t1Count, t2Count, err := p.processBatch(ctx, batch, logger)
		analyzedItems = append(analyzedItems, batchResults...)
		totalTier1 += t1Count
		totalTier2 += t2Count
		if err != nil {
			logger.Warn("Stopping batch analysis early, AI models exhausted", "processed", i+len(batch), "total", len(products))
			// Save remaining products without analysis
			for j := i + len(batch); j < len(products); j++ {
				analyzed := bestBuyFallback(products[j])
				if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
					logger.Error("Failed to save unanalyzed product", "sku", products[j].SKU, "error", saveErr)
				}
				analyzedItems = append(analyzedItems, analyzed)
			}
			break
		}
	}

	return analyzedItems, totalTier1, totalTier2
}

func (p *Processor) analyzePriceDropItems(ctx context.Context, candidates []priceDropCandidate, logger *slog.Logger) ([]AnalyzedProduct, int, int) {
	var analyzedItems []AnalyzedProduct
	totalTier1 := 0
	totalTier2 := 0

	for i := 0; i < len(candidates); i += batchSize {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping Best Buy price-drop analysis", "processed", i, "total", len(candidates))
			break
		}
		if i > 0 {
			select {
			case <-ctx.Done():
				logger.Warn("Context cancelled during price-drop inter-batch delay", "processed", i, "total", len(candidates))
				return analyzedItems, totalTier1, totalTier2
			case <-time.After(2 * time.Second):
			}
		}

		end := i + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		batch := candidates[i:end]
		result, err := p.processPriceDropBatch(ctx, batch, logger)
		analyzedItems = append(analyzedItems, result.Items...)
		totalTier1 += result.Tier1Passed
		totalTier2 += result.Tier2Passed
		if err != nil {
			logger.Warn("Stopping Best Buy price-drop analysis early, AI models exhausted", "processed", end, "total", len(candidates))
			break
		}
	}

	return analyzedItems, totalTier1, totalTier2
}

// processBatch handles a single batch through both AI tiers.
func (p *Processor) processBatch(ctx context.Context, batch []Product, logger *slog.Logger) ([]AnalyzedProduct, int, int, error) {
	cfg := productmonitor.Config[Product, BatchScreenResult, BatchAnalyzeResult, AnalyzedProduct]{
		ProductKey:  func(product Product) string { return product.SKU },
		ScreenKey:   func(screen BatchScreenResult) string { return screen.SKU },
		AnalyzeKey:  func(result BatchAnalyzeResult) string { return result.SKU },
		IsTopDeal:   func(screen BatchScreenResult) bool { return screen.IsTopDeal },
		IsWarmHot:   func(product AnalyzedProduct) bool { return product.IsWarm || product.IsLavaHot },
		ReturnSaved: func(AnalyzedProduct) bool { return true },
		Fallback:    bestBuyFallback,
		FromScreen:  bestBuyFromScreen,
		FromAnalysis: func(product Product, screen BatchScreenResult, result BatchAnalyzeResult) AnalyzedProduct {
			return bestBuyFromAnalysis(product, screen, result)
		},
		Save: p.store.SaveBestBuyProduct,
	}
	if p.analyzer != nil {
		cfg.ScreenBatch = p.analyzer.ScreenBestBuyBatch
		cfg.AnalyzeBatch = p.analyzer.AnalyzeBestBuyBatch
		cfg.AnalyzeOne = func(ctx context.Context, product Product) (*BatchAnalyzeResult, error) {
			result, err := p.analyzer.AnalyzeBestBuyProduct(ctx, product)
			if err != nil || result == nil {
				return nil, err
			}
			return &BatchAnalyzeResult{
				SKU:        product.SKU,
				CleanTitle: result.CleanTitle,
				IsWarm:     result.IsWarm,
				IsLavaHot:  result.IsLavaHot,
				Summary:    result.Summary,
			}, nil
		}
	}

	result, err := productmonitor.ProcessBatch(ctx, batch, cfg, logger)
	for _, item := range result.Items {
		if item.IsWarm || item.IsLavaHot {
			logger.Info("Best Buy deal labeled warm/hot",
				"sku", item.SKU,
				"clean_title", item.CleanTitle,
				"is_warm", item.IsWarm,
				"is_lava_hot", item.IsLavaHot,
				"discount_pct", item.DiscountPct,
				"seller", item.SellerName,
				"source", item.Source,
			)
		}
	}
	return result.Items, result.Tier1Passed, result.Tier2Passed, err
}

func (p *Processor) processPriceDropBatch(ctx context.Context, batch []priceDropCandidate, logger *slog.Logger) (productmonitor.Result[AnalyzedProduct], error) {
	cfg := productmonitor.Config[priceDropCandidate, BatchScreenResult, BatchAnalyzeResult, AnalyzedProduct]{
		ProductKey:  func(candidate priceDropCandidate) string { return candidate.State.SKU },
		ScreenKey:   func(screen BatchScreenResult) string { return screen.SKU },
		AnalyzeKey:  func(result BatchAnalyzeResult) string { return result.SKU },
		IsTopDeal:   func(screen BatchScreenResult) bool { return screen.IsTopDeal },
		IsWarmHot:   func(product AnalyzedProduct) bool { return product.IsWarm || product.IsLavaHot },
		ReturnSaved: func(AnalyzedProduct) bool { return true },
		Fallback:    priceDropFallback,
		FromScreen:  priceDropFromScreen,
		FromAnalysis: func(candidate priceDropCandidate, screen BatchScreenResult, result BatchAnalyzeResult) AnalyzedProduct {
			return priceDropFromAnalysis(candidate, screen, result)
		},
		Save: p.store.SaveBestBuyProduct,
	}
	if p.analyzer != nil {
		cfg.ScreenBatch = func(ctx context.Context, candidates []priceDropCandidate) ([]BatchScreenResult, error) {
			return p.analyzer.ScreenBestBuyBatch(ctx, priceDropCandidateProducts(candidates))
		}
		cfg.AnalyzeBatch = func(ctx context.Context, candidates []priceDropCandidate) ([]BatchAnalyzeResult, error) {
			return p.analyzer.AnalyzeBestBuyBatch(ctx, priceDropCandidateProducts(candidates))
		}
		cfg.AnalyzeOne = func(ctx context.Context, candidate priceDropCandidate) (*BatchAnalyzeResult, error) {
			result, err := p.analyzer.AnalyzeBestBuyProduct(ctx, candidate.State.Product)
			if err != nil || result == nil {
				return nil, err
			}
			return &BatchAnalyzeResult{
				SKU:        candidate.State.SKU,
				CleanTitle: result.CleanTitle,
				IsWarm:     result.IsWarm,
				IsLavaHot:  result.IsLavaHot,
				Summary:    result.Summary,
			}, nil
		}
	}
	result, err := productmonitor.ProcessBatch(ctx, batch, cfg, logger)
	for _, item := range result.Items {
		if item.IsWarm || item.IsLavaHot {
			logger.Info("Best Buy price drop labeled warm/hot",
				"sku", item.SKU,
				"clean_title", item.CleanTitle,
				"is_warm", item.IsWarm,
				"is_lava_hot", item.IsLavaHot,
				"discount_pct", item.DiscountPct,
				"price_drop_pct", item.PriceDropPct,
				"seller", item.SellerName,
				"source", item.Source,
			)
		}
	}
	return result, err
}

func bestBuyFallback(product Product) AnalyzedProduct {
	now := time.Now()
	price := effectivePrice(product)
	return AnalyzedProduct{
		Product:                  product,
		CleanTitle:               product.Name,
		DiscountPct:              computeDiscount(product),
		ProcessedAt:              now,
		LastSeen:                 now,
		InitialRegularPrice:      product.RegularPrice,
		InitialSalePrice:         product.SalePrice,
		InitialEffectivePrice:    price,
		PreviousRegularPrice:     product.RegularPrice,
		PreviousSalePrice:        product.SalePrice,
		PreviousEffectivePrice:   price,
		LowestSeenEffectivePrice: price,
	}
}

func bestBuyFromScreen(product Product, screen BatchScreenResult) AnalyzedProduct {
	analyzed := bestBuyFallback(product)
	if screen.CleanTitle != "" {
		analyzed.CleanTitle = screen.CleanTitle
	}
	return analyzed
}

func bestBuyFromAnalysis(product Product, screen BatchScreenResult, result BatchAnalyzeResult) AnalyzedProduct {
	analyzed := bestBuyFromScreen(product, screen)
	analyzed.CleanTitle = firstNonEmpty(result.CleanTitle, analyzed.CleanTitle, product.Name)
	analyzed.IsWarm = result.IsWarm
	analyzed.IsLavaHot = result.IsLavaHot
	analyzed.Summary = result.Summary
	return analyzed
}

func priceDropCandidateProducts(candidates []priceDropCandidate) []Product {
	products := make([]Product, 0, len(candidates))
	for _, candidate := range candidates {
		products = append(products, candidate.State.Product)
	}
	return products
}

func priceDropFallback(candidate priceDropCandidate) AnalyzedProduct {
	analyzed := candidate.State
	analyzed.AlertKind = AlertKindPriceDrop
	analyzed.ProcessedAt = time.Now()
	if analyzed.CleanTitle == "" {
		analyzed.CleanTitle = analyzed.Name
	}
	return analyzed
}

func priceDropFromScreen(candidate priceDropCandidate, screen BatchScreenResult) AnalyzedProduct {
	analyzed := priceDropFallback(candidate)
	if screen.CleanTitle != "" {
		analyzed.CleanTitle = screen.CleanTitle
	}
	return analyzed
}

func priceDropFromAnalysis(candidate priceDropCandidate, screen BatchScreenResult, result BatchAnalyzeResult) AnalyzedProduct {
	analyzed := priceDropFromScreen(candidate, screen)
	analyzed.CleanTitle = firstNonEmpty(result.CleanTitle, analyzed.CleanTitle, candidate.State.Name)
	analyzed.IsWarm = result.IsWarm
	analyzed.IsLavaHot = result.IsLavaHot
	analyzed.Summary = result.Summary
	return analyzed
}

// computeDiscount calculates the discount percentage for a product.
func computeDiscount(p Product) float64 {
	if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
		return (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
	}
	return 0
}

func effectivePrice(p Product) float64 {
	if p.SalePrice > 0 {
		return p.SalePrice
	}
	return p.RegularPrice
}

func refreshExistingBestBuyProduct(existing AnalyzedProduct, current Product, now time.Time) (AnalyzedProduct, priceDropEvaluation) {
	existing = ensureBestBuyPriceState(existing)
	eval := evaluateBestBuyPriceDrop(existing, current)

	lastRegular := existing.RegularPrice
	lastSale := existing.SalePrice
	lastEffective := effectivePrice(existing.Product)
	currentEffective := effectivePrice(current)

	refreshed := existing
	refreshed.Product = current
	refreshed.LastSeen = now
	refreshed.DiscountPct = computeDiscount(current)
	refreshed.PreviousRegularPrice = lastRegular
	refreshed.PreviousSalePrice = lastSale
	refreshed.PreviousEffectivePrice = lastEffective
	if currentEffective > 0 && (refreshed.LowestSeenEffectivePrice <= 0 || currentEffective < refreshed.LowestSeenEffectivePrice) {
		refreshed.LowestSeenEffectivePrice = currentEffective
	}
	if eval.Candidate {
		refreshed.LastPriceDropDetectedAt = now
		refreshed.PriceDropAmount = eval.Amount
		refreshed.PriceDropPct = eval.Pct
	}
	return refreshed, eval
}

func ensureBestBuyPriceState(product AnalyzedProduct) AnalyzedProduct {
	current := effectivePrice(product.Product)
	if product.InitialEffectivePrice <= 0 {
		product.InitialRegularPrice = product.RegularPrice
		product.InitialSalePrice = product.SalePrice
		product.InitialEffectivePrice = current
	}
	if product.PreviousEffectivePrice <= 0 {
		product.PreviousRegularPrice = product.RegularPrice
		product.PreviousSalePrice = product.SalePrice
		product.PreviousEffectivePrice = current
	}
	if product.LowestSeenEffectivePrice <= 0 {
		product.LowestSeenEffectivePrice = current
	}
	if product.DiscountPct == 0 {
		product.DiscountPct = computeDiscount(product.Product)
	}
	return product
}

func evaluateBestBuyPriceDrop(existing AnalyzedProduct, current Product) priceDropEvaluation {
	baseline := existing.InitialEffectivePrice
	lastSeen := effectivePrice(existing.Product)
	currentPrice := effectivePrice(current)
	if baseline <= 0 || lastSeen <= 0 || currentPrice <= 0 {
		return priceDropEvaluation{}
	}
	if currentPrice >= lastSeen-priceComparisonEpsilon {
		return priceDropEvaluation{}
	}
	amount := baseline - currentPrice
	pct := amount / baseline * 100
	if amount+priceComparisonEpsilon < priceDropMinAmount || pct+priceComparisonEpsilon < priceDropMinPct {
		return priceDropEvaluation{}
	}
	if existing.LastPriceDropAlertPrice > 0 && currentPrice >= existing.LastPriceDropAlertPrice-priceComparisonEpsilon {
		return priceDropEvaluation{}
	}
	key := priceDropAlertKey(current.SKU, current.Source, currentPrice)
	if existing.LastPriceDropAlertKey == key {
		return priceDropEvaluation{}
	}
	return priceDropEvaluation{
		Candidate: true,
		Amount:    amount,
		Pct:       pct,
	}
}

func priceDropAlertKey(sku, source string, price float64) string {
	return fmt.Sprintf("%s|%s|%.2f", sku, source, price)
}

// filterEligibleSubs returns subscriptions that should receive this deal.
func filterEligibleSubs(product AnalyzedProduct, subs []models.Subscription) []models.Subscription {
	var eligible []models.Subscription
	for _, sub := range subs {
		if isBestBuyEligible(product, sub) {
			eligible = append(eligible, sub)
		}
	}
	return eligible
}

func filterPriceDropEligibleSubs(product AnalyzedProduct, subs []models.Subscription) []models.Subscription {
	var eligible []models.Subscription
	for _, sub := range subs {
		switch sub.DealType {
		case dealtypes.BestBuyWarmHot:
			if product.IsWarm || product.IsLavaHot {
				eligible = append(eligible, sub)
			}
		case dealtypes.BestBuyHot:
			if product.IsLavaHot {
				eligible = append(eligible, sub)
			}
		}
	}
	return eligible
}

func hasBestBuyPriceDropSubscriptions(subs []models.Subscription) bool {
	for _, sub := range subs {
		if sub.DealType == dealtypes.BestBuyWarmHot || sub.DealType == dealtypes.BestBuyHot {
			return true
		}
	}
	return false
}

// isBestBuyEligible checks whether a deal should be sent to a given subscription.
func isBestBuyEligible(product AnalyzedProduct, sub models.Subscription) bool {
	return dealtypes.BestBuyEligible(sub.DealType, product.IsWarm, product.IsLavaHot)
}
