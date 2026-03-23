package memoryexpress

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	// batchSize is the number of items to send to Gemini tier-1 screening at once.
	batchSize = 10
)

// Store abstracts Firestore operations for the Memory Express processor.
type Store interface {
	MemExpressProductExists(ctx context.Context, sku, storeCode string) (bool, error)
	SaveMemExpressProduct(ctx context.Context, product AnalyzedProduct) error
	PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for clearance products.
type Analyzer interface {
	ScreenMemExpressBatch(ctx context.Context, products []Product) ([]BatchScreenResult, error)
	AnalyzeMemExpressProduct(ctx context.Context, product Product) (*AnalyzeResult, error)
}

// Notifier abstracts Discord notifications for clearance deals.
type Notifier interface {
	SendMemExpressDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
}

// Processor handles the Memory Express clearance processing pipeline.
type Processor struct {
	store    Store
	analyzer Analyzer
	notifier Notifier
	mu       sync.Mutex
}

// NewProcessor creates a new Memory Express clearance processor.
func NewProcessor(store Store, analyzer Analyzer, notifier Notifier) *Processor {
	return &Processor{
		store:    store,
		analyzer: analyzer,
		notifier: notifier,
	}
}

// ProcessMemExpressDeals runs the full clearance processing pipeline.
func (p *Processor) ProcessMemExpressDeals(ctx context.Context) error {
	if !p.mu.TryLock() {
		slog.Info("ProcessMemExpressDeals: already in progress, skipping", "processor", "memoryexpress")
		return nil
	}
	defer p.mu.Unlock()

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "memoryexpress", "runID", runID)

	var stats struct {
		stores       int
		scraped      int
		newItems     int
		tier1Passed  int
		tier2Passed  int
		warmHot      int
		notified     int
		notifyErrors int
		exitReason   string
	}

	defer func() {
		logger.Info("Memory Express pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"stores", stats.stores,
			"scraped", stats.scraped,
			"new_items", stats.newItems,
			"tier1_passed", stats.tier1Passed,
			"tier2_passed", stats.tier2Passed,
			"warm_hot", stats.warmHot,
			"notified", stats.notified,
			"notify_errors", stats.notifyErrors,
			"exit_reason", stats.exitReason,
		)
	}()

	// 1. Get all subscriptions and filter for memoryexpress
	allSubs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		stats.exitReason = "subscription_load_error"
		return fmt.Errorf("failed to get subscriptions: %w", err)
	}

	var meSubs []models.Subscription
	for _, sub := range allSubs {
		if sub.IsMemoryExpress() {
			meSubs = append(meSubs, sub)
		}
	}

	if len(meSubs) == 0 {
		stats.exitReason = "no_subscriptions"
		logger.Info("No Memory Express subscriptions found")
		return nil
	}

	// 2. Group subscriptions by store code
	storeSubsMap := make(map[string][]models.Subscription)
	for _, sub := range meSubs {
		code := sub.StoreCode
		if code == "" {
			continue
		}
		storeSubsMap[code] = append(storeSubsMap[code], sub)
	}
	stats.stores = len(storeSubsMap)

	// 3. For each unique store, scrape and process
	for storeCode, storeSubs := range storeSubsMap {
		if ctx.Err() != nil {
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		}

		products, err := Scrape(ctx, storeCode)
		if err != nil {
			logger.Error("Failed to scrape clearance page",
				"store", storeCode,
				"error", err,
			)
			continue
		}
		stats.scraped += len(products)

		// Add a small delay between stores to be respectful
		if len(storeSubsMap) > 1 {
			select {
			case <-ctx.Done():
				stats.exitReason = "context_cancelled"
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}

		// 4. Dedup — collect new products
		var newProducts []Product
		for _, product := range products {
			if ctx.Err() != nil {
				stats.exitReason = "context_cancelled"
				return ctx.Err()
			}

			exists, err := p.store.MemExpressProductExists(ctx, product.SKU, product.StoreCode)
			if err != nil {
				logger.Warn("Failed to check product existence",
					"sku", product.SKU,
					"store", product.StoreCode,
					"error", err,
				)
				continue
			}
			if exists {
				continue
			}
			newProducts = append(newProducts, product)
		}
		stats.newItems += len(newProducts)

		if len(newProducts) == 0 {
			continue
		}

		// 5. Tiered AI analysis on new products
		warmHotItems, t1Passed := p.analyzeNewItems(ctx, newProducts, storeSubs, logger)
		stats.tier1Passed += t1Passed
		stats.tier2Passed += len(warmHotItems)

		// 6. Save and notify
		for _, item := range warmHotItems {
			stats.warmHot++

			eligibleSubs := filterEligibleSubs(item, storeSubs)
			if len(eligibleSubs) == 0 {
				continue
			}

			if p.notifier != nil {
				if err := p.notifier.SendMemExpressDeal(ctx, item, eligibleSubs); err != nil {
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
	}

	// 7. Prune old records
	if err := p.store.PruneMemExpressProducts(ctx, 30, 500); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.exitReason = "success"
	return nil
}

// analyzeNewItems runs the tiered AI analysis pipeline on new products.
// Returns analyzed products that are warm or hot, and the total tier-1 pass count.
func (p *Processor) analyzeNewItems(ctx context.Context, products []Product, storeSubs []models.Subscription, logger *slog.Logger) ([]AnalyzedProduct, int) {
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
					CleanTitle:  products[j].Title,
					ProcessedAt: time.Now(),
					LastSeen:    time.Now(),
				}
				if saveErr := p.store.SaveMemExpressProduct(ctx, analyzed); saveErr != nil {
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
				CleanTitle:  product.Title,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if err := p.store.SaveMemExpressProduct(ctx, analyzed); err != nil {
				logger.Error("Failed to save product", "sku", product.SKU, "error", err)
			}
		}
		return results, 0, nil
	}

	// Tier 1: Batch screening
	screenResults, err := p.analyzer.ScreenMemExpressBatch(ctx, batch)
	if err != nil {
		if strings.Contains(err.Error(), "all model tiers exhausted") {
			logger.Warn("Tier-1 batch screening skipped, AI quota exhausted", "batch_size", len(batch))
			// Save all without analysis
			for _, product := range batch {
				analyzed := AnalyzedProduct{
					Product:     product,
					CleanTitle:  product.Title,
					ProcessedAt: time.Now(),
					LastSeen:    time.Now(),
				}
				if saveErr := p.store.SaveMemExpressProduct(ctx, analyzed); saveErr != nil {
					logger.Error("Failed to save product", "sku", product.SKU, "error", saveErr)
				}
			}
			return results, 0, err
		}
		logger.Error("Tier-1 batch screening failed", "batch_size", len(batch), "error", err)
		// Save all without analysis on screening failure
		for _, product := range batch {
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Title,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveMemExpressProduct(ctx, analyzed); saveErr != nil {
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
			// Save with screen title if available
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Title,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if ok && screen.CleanTitle != "" {
				analyzed.CleanTitle = screen.CleanTitle
			}
			if err := p.store.SaveMemExpressProduct(ctx, analyzed); err != nil {
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

		analyzeResult, err := p.analyzer.AnalyzeMemExpressProduct(ctx, candidate.product)
		if err != nil {
			logger.Warn("Tier-2 verification failed",
				"sku", candidate.product.SKU,
				"title", candidate.product.Title,
				"error", err,
			)
			// Save with tier-1 title
			analyzed := AnalyzedProduct{
				Product:     candidate.product,
				CleanTitle:  candidate.screenResult.CleanTitle,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveMemExpressProduct(ctx, analyzed); saveErr != nil {
				logger.Error("Failed to save product", "sku", candidate.product.SKU, "error", saveErr)
			}
			continue
		}

		if analyzeResult == nil {
			logger.Info("Item failed tier-2 verification (nil result)",
				"sku", candidate.product.SKU,
				"title", candidate.product.Title,
			)
			analyzed := AnalyzedProduct{
				Product:     candidate.product,
				CleanTitle:  candidate.screenResult.CleanTitle,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}
			if saveErr := p.store.SaveMemExpressProduct(ctx, analyzed); saveErr != nil {
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
			ProcessedAt: time.Now(),
			LastSeen:    time.Now(),
		}

		if err := p.store.SaveMemExpressProduct(ctx, analyzed); err != nil {
			logger.Error("Failed to save analyzed product", "sku", candidate.product.SKU, "error", err)
			continue
		}

		if !analyzed.IsWarm && !analyzed.IsLavaHot {
			logger.Info("Item failed tier-2 verification (not warm/hot)",
				"sku", candidate.product.SKU,
				"title", candidate.product.Title,
				"clean_title", analyzeResult.CleanTitle,
			)
			continue
		}

		logger.Info("Deal passed both tiers",
			"sku", candidate.product.SKU,
			"clean_title", analyzed.CleanTitle,
			"is_warm", analyzed.IsWarm,
			"is_lava_hot", analyzed.IsLavaHot,
			"discount_pct", candidate.product.DiscountPct,
			"store", candidate.product.StoreName,
		)

		results = append(results, analyzed)
	}

	return results, len(tier1Passed), nil
}

// filterEligibleSubs returns subscriptions that should receive this deal based on their filter.
func filterEligibleSubs(product AnalyzedProduct, subs []models.Subscription) []models.Subscription {
	var eligible []models.Subscription
	for _, sub := range subs {
		if isMemExpressEligible(product, sub) {
			eligible = append(eligible, sub)
		}
	}
	return eligible
}

// isMemExpressEligible checks whether a deal should be sent to a given subscription.
func isMemExpressEligible(product AnalyzedProduct, sub models.Subscription) bool {
	switch sub.DealType {
	case "me_warm_hot":
		return product.IsWarm || product.IsLavaHot
	case "me_hot":
		return product.IsLavaHot
	default:
		// Unknown filter — only send hot deals to be safe
		if strings.HasPrefix(sub.DealType, "me_") {
			return product.IsLavaHot
		}
		return false
	}
}
