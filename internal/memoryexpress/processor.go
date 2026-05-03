package memoryexpress

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/productmonitor"
)

const (
	// batchSize is the number of items to send to Gemini tier-1 screening at once.
	batchSize = 10
)

// Store abstracts persistence operations for the Memory Express processor.
type Store interface {
	GetExistingMemExpressProductIDs(ctx context.Context, products []Product) (map[string]struct{}, error)
	SaveMemExpressProduct(ctx context.Context, product AnalyzedProduct) error
	RefreshMemExpressProductLastSeen(ctx context.Context, product Product) error
	PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetMemExpressSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for clearance products.
type Analyzer interface {
	ScreenMemExpressBatch(ctx context.Context, products []Product) ([]BatchScreenResult, error)
	AnalyzeMemExpressBatch(ctx context.Context, products []Product) ([]BatchAnalyzeResult, error)
	AnalyzeMemExpressProduct(ctx context.Context, product Product) (*AnalyzeResult, error)
}

// Notifier abstracts Discord notifications for clearance deals.
type Notifier interface {
	SendMemExpressDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
}

// Processor handles the Memory Express clearance processing pipeline.
type Processor struct {
	store     Store
	analyzer  Analyzer
	notifier  Notifier
	scrape    func(context.Context, string) ([]Product, error)
	beforeRun func()
	mu        sync.Mutex
}

// ProcessorOption customizes a Memory Express processor.
type ProcessorOption func(*Processor)

// WithScrapeFunc replaces the default scrape implementation.
func WithScrapeFunc(scrape func(context.Context, string) ([]Product, error)) ProcessorOption {
	return func(p *Processor) {
		if scrape != nil {
			p.scrape = scrape
		}
	}
}

func WithBeforeRun(fn func()) ProcessorOption {
	return func(p *Processor) {
		p.beforeRun = fn
	}
}

// NewProcessor creates a new Memory Express clearance processor.
func NewProcessor(store Store, analyzer Analyzer, notifier Notifier, opts ...ProcessorOption) *Processor {
	p := &Processor{
		store:    store,
		analyzer: analyzer,
		notifier: notifier,
		scrape:   Scrape,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
}

// ProcessMemExpressDeals runs the full clearance processing pipeline.
func (p *Processor) ProcessMemExpressDeals(ctx context.Context) error {
	if !p.mu.TryLock() {
		slog.Info("ProcessMemExpressDeals: already in progress, skipping", "processor", "memoryexpress")
		return nil
	}
	defer p.mu.Unlock()
	if p.beforeRun != nil {
		p.beforeRun()
	}

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "memoryexpress", "runID", runID)

	var stats struct {
		stores       int
		scraped      int
		scrapeErrors int
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
			"scrape_errors", stats.scrapeErrors,
			"new_items", stats.newItems,
			"tier1_passed", stats.tier1Passed,
			"tier2_passed", stats.tier2Passed,
			"warm_hot", stats.warmHot,
			"notified", stats.notified,
			"notify_errors", stats.notifyErrors,
			"exit_reason", stats.exitReason,
		)
	}()

	// 1. Load only Memory Express subscriptions
	meSubs, err := p.store.GetMemExpressSubscriptions(ctx)
	if err != nil {
		stats.exitReason = "subscription_load_error"
		return fmt.Errorf("failed to get subscriptions: %w", err)
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

		products, err := p.scrape(ctx, storeCode)
		if err != nil {
			stats.scrapeErrors++
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
		existing, err := p.store.GetExistingMemExpressProductIDs(ctx, products)
		if err != nil {
			logger.Warn("Failed to batch check product existence",
				"store", storeCode,
				"error", err,
			)
			existing = nil
		}

		var newProducts []Product
		for _, product := range products {
			if ctx.Err() != nil {
				stats.exitReason = "context_cancelled"
				return ctx.Err()
			}

			if _, exists := existing[DocID(product.SKU, product.StoreCode)]; exists {
				if err := p.store.RefreshMemExpressProductLastSeen(ctx, product); err != nil {
					logger.Warn("Failed to refresh existing Memory Express listing",
						"store", storeCode,
						"sku", product.SKU,
						"error", err,
					)
				}
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

	if stats.scrapeErrors == len(storeSubsMap) {
		stats.exitReason = "all_store_scrapes_failed"
		return fmt.Errorf("failed to scrape all subscribed Memory Express stores")
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
	cfg := productmonitor.Config[Product, BatchScreenResult, BatchAnalyzeResult, AnalyzedProduct]{
		ProductKey: func(product Product) string { return product.SKU },
		ScreenKey:  func(screen BatchScreenResult) string { return screen.SKU },
		AnalyzeKey: func(result BatchAnalyzeResult) string { return result.SKU },
		IsTopDeal:  func(screen BatchScreenResult) bool { return screen.IsTopDeal },
		IsWarmHot:  func(product AnalyzedProduct) bool { return product.IsWarm || product.IsLavaHot },
		Tier1Limit: Tier1SelectionLimit,
		ReturnSaved: func(product AnalyzedProduct) bool {
			return product.IsWarm || product.IsLavaHot
		},
		Fallback:     memExpressFallback,
		FromScreen:   memExpressFromScreen,
		FromAnalysis: memExpressFromAnalysis,
		Save:         p.store.SaveMemExpressProduct,
	}
	if p.analyzer != nil {
		cfg.ScreenBatch = p.analyzer.ScreenMemExpressBatch
		cfg.AnalyzeBatch = p.analyzer.AnalyzeMemExpressBatch
		cfg.AnalyzeOne = func(ctx context.Context, product Product) (*BatchAnalyzeResult, error) {
			result, err := p.analyzer.AnalyzeMemExpressProduct(ctx, product)
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
		logger.Info("Memory Express deal labeled warm/hot",
			"sku", item.SKU,
			"clean_title", item.CleanTitle,
			"is_warm", item.IsWarm,
			"is_lava_hot", item.IsLavaHot,
			"discount_pct", item.DiscountPct,
			"store", item.StoreName,
		)
	}
	return result.Items, result.Tier1Passed, err
}

func memExpressFallback(product Product) AnalyzedProduct {
	return AnalyzedProduct{
		Product:     product,
		CleanTitle:  product.Title,
		ProcessedAt: time.Now(),
		LastSeen:    time.Now(),
	}
}

func memExpressFromScreen(product Product, screen BatchScreenResult) AnalyzedProduct {
	analyzed := memExpressFallback(product)
	if screen.CleanTitle != "" {
		analyzed.CleanTitle = screen.CleanTitle
	}
	return analyzed
}

func memExpressFromAnalysis(product Product, screen BatchScreenResult, result BatchAnalyzeResult) AnalyzedProduct {
	analyzed := memExpressFromScreen(product, screen)
	if result.CleanTitle != "" {
		analyzed.CleanTitle = result.CleanTitle
	}
	analyzed.IsWarm = result.IsWarm
	analyzed.IsLavaHot = result.IsLavaHot
	analyzed.Summary = result.Summary
	return analyzed
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
	return dealtypes.MemoryExpressEligible(sub.DealType, product.IsWarm, product.IsLavaHot)
}
