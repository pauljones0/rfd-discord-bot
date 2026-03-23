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

// Store abstracts Firestore operations for the Memory Express processor.
type Store interface {
	MemExpressProductExists(ctx context.Context, sku, storeCode string) (bool, error)
	SaveMemExpressProduct(ctx context.Context, product AnalyzedProduct) error
	PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for clearance products.
type Analyzer interface {
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
		analyzed     int
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
			"analyzed", stats.analyzed,
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

		// 4. Check each product for dedup
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
			stats.newItems++

			// 5. AI analysis
			analyzed := AnalyzedProduct{
				Product:     product,
				CleanTitle:  product.Title,
				ProcessedAt: time.Now(),
				LastSeen:    time.Now(),
			}

			if p.analyzer != nil {
				result, err := p.analyzer.AnalyzeMemExpressProduct(ctx, product)
				if err != nil {
					logger.Warn("AI analysis failed, using defaults",
						"sku", product.SKU,
						"title", product.Title,
						"error", err,
					)
				} else if result != nil {
					analyzed.CleanTitle = result.CleanTitle
					analyzed.IsWarm = result.IsWarm
					analyzed.IsLavaHot = result.IsLavaHot
					analyzed.Summary = result.Summary
					stats.analyzed++
				}
			}

			// 6. Save to Firestore
			if err := p.store.SaveMemExpressProduct(ctx, analyzed); err != nil {
				logger.Error("Failed to save product",
					"sku", product.SKU,
					"error", err,
				)
				continue
			}

			// 7. Notify if warm or hot
			if !analyzed.IsWarm && !analyzed.IsLavaHot {
				continue
			}
			stats.warmHot++

			eligibleSubs := filterEligibleSubs(analyzed, storeSubs)
			if len(eligibleSubs) == 0 {
				continue
			}

			if p.notifier != nil {
				if err := p.notifier.SendMemExpressDeal(ctx, analyzed, eligibleSubs); err != nil {
					logger.Error("Failed to send deal notification",
						"sku", product.SKU,
						"title", analyzed.CleanTitle,
						"error", err,
					)
					stats.notifyErrors++
				} else {
					stats.notified++
				}
			}
		}
	}

	// 8. Prune old records
	if err := p.store.PruneMemExpressProducts(ctx, 30, 500); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.exitReason = "success"
	return nil
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
