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

// Store abstracts Firestore operations for the Best Buy processor.
type Store interface {
	BestBuyProductExists(ctx context.Context, sku, source string) (bool, error)
	SaveBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// Analyzer abstracts Gemini AI analysis for Best Buy products.
type Analyzer interface {
	AnalyzeBestBuyProduct(ctx context.Context, product Product) (*AnalyzeResult, error)
}

// Notifier abstracts Discord notifications for Best Buy deals.
type Notifier interface {
	SendBestBuyDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
}

// Processor handles the Best Buy deal processing pipeline.
type Processor struct {
	store    Store
	client   *Client
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
		analyzed     int
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
			"analyzed", stats.analyzed,
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

	// 2. Fetch products from all sellers
	var allProducts []Product
	stats.sellers = len(DefaultSellers)

	for _, seller := range DefaultSellers {
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
		select {
		case <-ctx.Done():
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	// 3. Fetch Geek Squad open-box products
	if ctx.Err() == nil {
		openBoxProducts, err := p.client.FetchOpenBoxProducts(ctx)
		if err != nil {
			logger.Error("Failed to fetch open-box products", "error", err)
		} else {
			logger.Info("Fetched open-box products", "count", len(openBoxProducts))
			allProducts = append(allProducts, openBoxProducts...)
		}
	}

	stats.fetched = len(allProducts)

	if len(allProducts) == 0 {
		stats.exitReason = "no_products"
		return nil
	}

	// 4. Dedup, analyze, save, notify
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
		stats.newItems++

		// Compute discount
		discountPct := 0.0
		if product.RegularPrice > 0 && product.SalePrice > 0 && product.SalePrice < product.RegularPrice {
			discountPct = (product.RegularPrice - product.SalePrice) / product.RegularPrice * 100
		}

		// Wrap product URL with affiliate prefix
		if p.affiliatePrefix != "" && product.URL != "" {
			product.URL = p.affiliatePrefix + url.QueryEscape(product.URL)
		}

		analyzed := AnalyzedProduct{
			Product:     product,
			CleanTitle:  product.Name,
			DiscountPct: discountPct,
			ProcessedAt: time.Now(),
			LastSeen:    time.Now(),
		}

		// 5. AI analysis
		if p.analyzer != nil {
			result, err := p.analyzer.AnalyzeBestBuyProduct(ctx, product)
			if err != nil {
				if strings.Contains(err.Error(), "all model tiers exhausted") {
					logger.Warn("AI quota exhausted, saving remaining products without analysis")
					// Save without analysis and continue
				} else {
					logger.Warn("AI analysis failed, using defaults",
						"sku", product.SKU,
						"name", product.Name,
						"error", err,
					)
				}
			} else if result != nil {
				analyzed.CleanTitle = result.CleanTitle
				analyzed.IsWarm = result.IsWarm
				analyzed.IsLavaHot = result.IsLavaHot
				analyzed.Summary = result.Summary
				stats.analyzed++
			}
		}

		// 6. Save to Firestore
		if err := p.store.SaveBestBuyProduct(ctx, analyzed); err != nil {
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

		eligibleSubs := filterEligibleSubs(analyzed, bbSubs)
		if len(eligibleSubs) == 0 {
			continue
		}

		if p.notifier != nil {
			if err := p.notifier.SendBestBuyDeal(ctx, analyzed, eligibleSubs); err != nil {
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

	// 8. Prune old records
	if err := p.store.PruneBestBuyProducts(ctx, 30, 1000); err != nil {
		logger.Warn("Failed to prune old products", "error", err)
	}

	stats.exitReason = "success"
	return nil
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
	case "bb_warm_hot":
		return product.IsWarm || product.IsLavaHot
	case "bb_hot":
		return product.IsLavaHot
	default:
		if strings.HasPrefix(sub.DealType, "bb_") {
			return product.IsLavaHot
		}
		return false
	}
}
