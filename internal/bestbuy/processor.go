package bestbuy

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	// batchSize is the number of items to send to Gemini tier-1 screening at once.
	batchSize         = 10
	bestBuyMaxRecords = 10000
)

// Store abstracts Firestore operations for the Best Buy processor.
type Store interface {
	GetActiveBestBuySellers(ctx context.Context) ([]Seller, error)
	SeedBestBuySellers(ctx context.Context) (bool, error)
	BestBuyProductExists(ctx context.Context, sku, source string) (bool, error)
	SaveBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for Best Buy products.
type Analyzer interface {
	ScreenBestBuyBatch(ctx context.Context, products []Product) ([]BatchScreenResult, error)
	AnalyzeBestBuyProduct(ctx context.Context, product Product) (*AnalyzeResult, error)
}

// Notifier abstracts Discord notifications for Best Buy deals.
type Notifier interface {
	SendBestBuyDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
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

			exists, err := p.store.BestBuyProductExists(ctx, product.SKU, product.Source)
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

			item := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Name,
				DiscountPct: computeDiscount(product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
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
		sellers      int
		fetched      int
		newItems     int
		tier1Passed  int
		tier2Passed  int
		warmHot      int
		notified     int
		notifyErrors int
		exitReason   string
	}

	defer func() {
		logger.Info("Best Buy pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.sellers,
			"fetched", stats.fetched,
			"new_items", stats.newItems,
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
		stats.exitReason = "no_subscriptions"
		logger.Info("No Best Buy subscriptions found")
		return nil
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
	for _, product := range allProducts {
		if ctx.Err() != nil {
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		}

		exists, err := p.store.BestBuyProductExists(ctx, product.SKU, product.Source)
		if err != nil {
			logger.Warn("Failed to check product existence",
				"sku", product.SKU,
				"source", product.Source,
				"error", err,
			)
			continue
		}
		if exists {
			continue
		}

		// Wrap product URL with affiliate prefix before any processing
		if p.affiliatePrefix != "" && product.URL != "" {
			product.URL = p.affiliatePrefix + url.QueryEscape(product.URL)
		}

		newProducts = append(newProducts, product)
	}
	stats.newItems = len(newProducts)

	if len(newProducts) == 0 {
		stats.exitReason = "no_new_items"
		// Still prune
		if err := p.store.PruneBestBuyProducts(ctx, 30, bestBuyMaxRecords); err != nil {
			logger.Warn("Failed to prune old products", "error", err)
		}
		return nil
	}

	// 5. Save and notify every new configured-seller listing for v1.
	for _, product := range newProducts {
		item := AnalyzedProduct{
			Product:     product,
			CleanTitle:  product.Name,
			IsWarm:      true,
			DiscountPct: computeDiscount(product),
			ProcessedAt: time.Now(),
			LastSeen:    time.Now(),
		}
		if err := p.store.SaveBestBuyProduct(ctx, item); err != nil {
			logger.Error("Failed to save Best Buy new listing",
				"sku", item.SKU,
				"title", item.Name,
				"error", err,
			)
			continue
		}
		stats.warmHot++

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

	// 6. Prune old records
	if err := p.store.PruneBestBuyProducts(ctx, 30, bestBuyMaxRecords); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.exitReason = "success"
	return nil
}

// analyzeNewItems runs the tiered AI analysis pipeline on new products.
// Returns analyzed products that are warm or hot, and the total tier-1 pass count.
func (p *Processor) analyzeNewItems(ctx context.Context, products []Product, logger *slog.Logger) ([]AnalyzedProduct, int) {
	var warmHotItems []AnalyzedProduct
	totalTier1 := 0

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
				return warmHotItems, totalTier1
			case <-time.After(2 * time.Second):
			}
		}

		end := i + batchSize
		if end > len(products) {
			end = len(products)
		}
		batch := products[i:end]

		batchResults, t1Count, err := p.processBatch(ctx, batch, logger)
		warmHotItems = append(warmHotItems, batchResults...)
		totalTier1 += t1Count
		if err != nil {
			logger.Warn("Stopping batch analysis early, AI models exhausted", "processed", i+len(batch), "total", len(products))
			// Save remaining products without analysis
			for j := i + len(batch); j < len(products); j++ {
				analyzed := AnalyzedProduct{
					Product:     products[j],
					CleanTitle:  products[j].Name,
					DiscountPct: computeDiscount(products[j]),
					ProcessedAt: time.Now(),
					LastSeen:    time.Now(),
				}
				if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
					logger.Error("Failed to save unanalyzed product", "sku", products[j].SKU, "error", saveErr)
				}
			}
			break
		}
	}

	return warmHotItems, totalTier1
}

// processBatch handles a single batch through both AI tiers.
func (p *Processor) processBatch(ctx context.Context, batch []Product, logger *slog.Logger) ([]AnalyzedProduct, int, error) {
	var results []AnalyzedProduct

	if p.analyzer == nil {
		logger.Warn("AI analyzer not available, saving products without analysis")
		for _, product := range batch {
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Name,
				DiscountPct: computeDiscount(product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if err := p.store.SaveBestBuyProduct(ctx, analyzed); err != nil {
				logger.Error("Failed to save product", "sku", product.SKU, "error", err)
			}
		}
		return results, 0, nil
	}

	// Tier 1: Batch screening
	screenResults, err := p.analyzer.ScreenBestBuyBatch(ctx, batch)
	if err != nil {
		if strings.Contains(err.Error(), "all model tiers exhausted") {
			logger.Warn("Tier-1 batch screening skipped, AI quota exhausted", "batch_size", len(batch))
			for _, product := range batch {
				analyzed := AnalyzedProduct{
					Product:     product,
					CleanTitle:  product.Name,
					DiscountPct: computeDiscount(product),
					ProcessedAt: time.Now(),
					LastSeen:    time.Now(),
				}
				if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
					logger.Error("Failed to save product", "sku", product.SKU, "error", saveErr)
				}
			}
			return results, 0, err
		}
		logger.Error("Tier-1 batch screening failed", "batch_size", len(batch), "error", err)
		for _, product := range batch {
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Name,
				DiscountPct: computeDiscount(product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
				logger.Error("Failed to save product", "sku", product.SKU, "error", saveErr)
			}
		}
		return results, 0, nil
	}

	// Build screen map by SKU
	screenMap := make(map[string]BatchScreenResult)
	for _, r := range screenResults {
		screenMap[r.SKU] = r
	}

	type tier1Candidate struct {
		product      Product
		screenResult BatchScreenResult
	}
	var tier1Passed []tier1Candidate

	// Save non-top-deal products and collect tier-1 candidates
	for _, product := range batch {
		screen, ok := screenMap[product.SKU]
		if !ok || !screen.IsTopDeal {
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Name,
				DiscountPct: computeDiscount(product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if ok && screen.CleanTitle != "" {
				analyzed.CleanTitle = screen.CleanTitle
			}
			if err := p.store.SaveBestBuyProduct(ctx, analyzed); err != nil {
				logger.Error("Failed to save product", "sku", product.SKU, "error", err)
			}
			continue
		}
		tier1Passed = append(tier1Passed, tier1Candidate{product, screen})
	}

	logger.Info("Tier-1 screening results",
		"batch_size", len(batch),
		"passed_tier1", len(tier1Passed),
	)

	// Tier 2: Individual verification for tier-1 candidates
	for _, candidate := range tier1Passed {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping tier-2 verification")
			break
		}

		analyzeResult, err := p.analyzer.AnalyzeBestBuyProduct(ctx, candidate.product)
		if err != nil {
			logger.Warn("Tier-2 verification failed",
				"sku", candidate.product.SKU,
				"name", candidate.product.Name,
				"error", err,
			)
			analyzed := AnalyzedProduct{
				Product:     candidate.product,
				CleanTitle:  candidate.screenResult.CleanTitle,
				DiscountPct: computeDiscount(candidate.product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
				logger.Error("Failed to save product", "sku", candidate.product.SKU, "error", saveErr)
			}
			continue
		}

		if analyzeResult == nil {
			logger.Info("Item failed tier-2 verification (nil result)",
				"sku", candidate.product.SKU,
				"name", candidate.product.Name,
			)
			analyzed := AnalyzedProduct{
				Product:     candidate.product,
				CleanTitle:  candidate.screenResult.CleanTitle,
				DiscountPct: computeDiscount(candidate.product),
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveBestBuyProduct(ctx, analyzed); saveErr != nil {
				logger.Error("Failed to save product", "sku", candidate.product.SKU, "error", saveErr)
			}
			continue
		}

		analyzed := AnalyzedProduct{
			Product:     candidate.product,
			CleanTitle:  analyzeResult.CleanTitle,
			IsWarm:      analyzeResult.IsWarm,
			IsLavaHot:   analyzeResult.IsLavaHot,
			Summary:     analyzeResult.Summary,
			DiscountPct: computeDiscount(candidate.product),
			ProcessedAt: time.Now(),
			LastSeen:    time.Now(),
		}

		if err := p.store.SaveBestBuyProduct(ctx, analyzed); err != nil {
			logger.Error("Failed to save analyzed product", "sku", candidate.product.SKU, "error", err)
			continue
		}

		if !analyzed.IsWarm && !analyzed.IsLavaHot {
			logger.Info("Item failed tier-2 verification (not warm/hot)",
				"sku", candidate.product.SKU,
				"name", candidate.product.Name,
				"clean_title", analyzeResult.CleanTitle,
			)
			continue
		}

		logger.Info("Deal passed both tiers",
			"sku", candidate.product.SKU,
			"clean_title", analyzed.CleanTitle,
			"is_warm", analyzed.IsWarm,
			"is_lava_hot", analyzed.IsLavaHot,
			"discount_pct", analyzed.DiscountPct,
			"seller", candidate.product.SellerName,
			"source", candidate.product.Source,
		)

		results = append(results, analyzed)
	}

	return results, len(tier1Passed), nil
}

// computeDiscount calculates the discount percentage for a product.
func computeDiscount(p Product) float64 {
	if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
		return (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
	}
	return 0
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

// isBestBuyEligible checks whether a deal should be sent to a given subscription.
func isBestBuyEligible(product AnalyzedProduct, sub models.Subscription) bool {
	switch sub.DealType {
	case "bb_new", "bb_warm_hot", "bb_hot":
		return true
	default:
		if strings.HasPrefix(sub.DealType, "bb_") {
			return true
		}
		return false
	}
}
