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

	comparableWarmMinCount          = 2
	comparableWarmMinPct            = 20.0
	comparableWarmMinAmount         = 50.0
	comparableLavaHotMinPct         = 40.0
	comparableLavaHotMinAmount      = 100.0
	singleComparableWarmMinPct      = 30.0
	singleComparableWarmMinAmount   = 75.0
	comparableSmallSetFloorMaxCount = 2
	comparableHighSpreadMinPct      = 50.0
)

// Store abstracts persistence operations for the Best Buy processor.
type Store interface {
	GetActiveBestBuySellers(ctx context.Context) ([]Seller, error)
	SeedBestBuySellers(ctx context.Context) (bool, error)
	GetBestBuyProduct(ctx context.Context, sku, source string) (AnalyzedProduct, bool, error)
	SaveBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	RefreshBestBuyProduct(ctx context.Context, product AnalyzedProduct) error
	PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error
	GetBestBuySoldCompSnapshot(ctx context.Context, key string) (SoldCompSnapshot, bool, error)
	SaveBestBuySoldCompSnapshot(ctx context.Context, key string, snapshot SoldCompSnapshot) error
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
	store            Store
	client           *Client
	analyzer         Analyzer
	notifier         Notifier
	affiliatePrefix  string
	soldCompEnricher BestBuySoldCompEnricher
	mu               sync.Mutex
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

func (p *Processor) SetSoldCompEnricher(enricher BestBuySoldCompEnricher) {
	p.soldCompEnricher = enricher
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
			if reason := rejectReasonFromIndexedState(product, time.Now()); reason != "" {
				logger.Info("Skipping stale Best Buy baseline listing",
					"sku", product.SKU,
					"seller", product.SellerName,
					"reason", reason,
				)
				continue
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
	if p.soldCompEnricher != nil {
		p.soldCompEnricher.BeginRun()
	}

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
		filteredOutOfStock  int
		filteredNotVisible  int
		filteredExpired     int
		validationSkipped   int
		comparablesEnriched int
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
			"filtered_out_of_stock", stats.filteredOutOfStock,
			"filtered_not_visible", stats.filteredNotVisible,
			"filtered_expired", stats.filteredExpired,
			"validation_skipped", stats.validationSkipped,
			"comparables_enriched", stats.comparablesEnriched,
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
	now := time.Now()
	for _, product := range allProducts {
		if ctx.Err() != nil {
			stats.exitReason = "context_cancelled"
			return ctx.Err()
		}
		switch rejectReasonFromIndexedState(product, now) {
		case "out_of_stock":
			stats.filteredOutOfStock++
			continue
		case "not_visible":
			stats.filteredNotVisible++
			continue
		case "search_expired":
			stats.filteredExpired++
			continue
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
	preparedNew, prepNewStats := p.prepareBestBuyProductsForAI(ctx, newProducts, logger)
	newProducts = preparedNew
	stats.validationSkipped += prepNewStats.ValidationSkipped
	stats.comparablesEnriched += prepNewStats.ComparablesEnriched

	preparedDrops, prepDropStats := p.prepareBestBuyPriceDropsForAI(ctx, priceDropCandidates, logger)
	priceDropCandidates = preparedDrops
	stats.validationSkipped += prepDropStats.ValidationSkipped
	stats.comparablesEnriched += prepDropStats.ComparablesEnriched

	analyzedItems, tier1Passed, tier2Passed := p.analyzeNewItems(ctx, newProducts, len(bbSubs) > 0, logger)
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

		validated, ok := p.validateBestBuyNotification(ctx, item, logger)
		if !ok {
			stats.validationSkipped++
			continue
		}
		item = validated
		if err := p.store.SaveBestBuyProduct(ctx, item); err != nil {
			logger.Warn("Failed to persist Best Buy final validation state",
				"sku", item.SKU,
				"source", item.Source,
				"error", err,
			)
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

	analyzedDrops, dropTier1, dropTier2 := p.analyzePriceDropItems(ctx, priceDropCandidates, len(bbSubs) > 0, logger)
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

		validated, ok := p.validateBestBuyNotification(ctx, item, logger)
		if !ok {
			stats.validationSkipped++
			continue
		}
		item = validated

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

type bestBuyPrepStats struct {
	ValidationSkipped   int
	ComparablesEnriched int
}

func (p *Processor) prepareBestBuyProductsForAI(ctx context.Context, products []Product, logger *slog.Logger) ([]Product, bestBuyPrepStats) {
	var stats bestBuyPrepStats
	if len(products) == 0 || p.client == nil || !p.client.canValidateSellerOffers() {
		return products, stats
	}

	prepared := make([]Product, 0, len(products))
	for _, product := range products {
		if ctx.Err() != nil {
			return prepared, stats
		}
		validation, err := p.client.ValidateSellerOffer(ctx, product, time.Now())
		if err != nil {
			stats.ValidationSkipped++
			logger.Warn("Best Buy candidate validation failed",
				"sku", product.SKU,
				"seller", product.SellerName,
				"sellerID", product.SellerID,
				"error", err,
			)
			continue
		}
		if !validation.Valid {
			stats.ValidationSkipped++
			logger.Info("Best Buy candidate skipped after validation",
				"sku", product.SKU,
				"seller", product.SellerName,
				"sellerID", product.SellerID,
				"reason", validation.Reason,
			)
			continue
		}

		enriched, err := p.client.EnrichComparables(ctx, validation.Product, time.Now())
		if err != nil {
			logger.Warn("Best Buy comparable enrichment failed",
				"sku", validation.Product.SKU,
				"seller", validation.Product.SellerName,
				"sellerID", validation.Product.SellerID,
				"error", err,
			)
			prepared = append(prepared, validation.Product)
			continue
		}
		if enriched.ComparableCount > 0 {
			stats.ComparablesEnriched++
		}
		prepared = append(prepared, enriched)
	}
	return prepared, stats
}

func (p *Processor) prepareBestBuyPriceDropsForAI(ctx context.Context, candidates []priceDropCandidate, logger *slog.Logger) ([]priceDropCandidate, bestBuyPrepStats) {
	var stats bestBuyPrepStats
	if len(candidates) == 0 || p.client == nil || !p.client.canValidateSellerOffers() {
		return candidates, stats
	}

	prepared := make([]priceDropCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return prepared, stats
		}
		validation, err := p.client.ValidateSellerOffer(ctx, candidate.State.Product, time.Now())
		if err != nil {
			stats.ValidationSkipped++
			logger.Warn("Best Buy price-drop validation failed",
				"sku", candidate.State.SKU,
				"seller", candidate.State.SellerName,
				"sellerID", candidate.State.SellerID,
				"error", err,
			)
			continue
		}
		if !validation.Valid {
			stats.ValidationSkipped++
			logger.Info("Best Buy price-drop candidate skipped after validation",
				"sku", candidate.State.SKU,
				"seller", candidate.State.SellerName,
				"sellerID", candidate.State.SellerID,
				"reason", validation.Reason,
			)
			continue
		}

		candidate.State.Product = mergeSellerProduct(candidate.State.Product, validation.Product)
		if current := effectivePrice(candidate.State.Product); current > 0 && candidate.State.InitialEffectivePrice > 0 {
			candidate.State.PriceDropAmount = candidate.State.InitialEffectivePrice - current
			candidate.State.PriceDropPct = candidate.State.PriceDropAmount / candidate.State.InitialEffectivePrice * 100
		}

		enriched, err := p.client.EnrichComparables(ctx, candidate.State.Product, time.Now())
		if err != nil {
			logger.Warn("Best Buy price-drop comparable enrichment failed",
				"sku", candidate.State.SKU,
				"seller", candidate.State.SellerName,
				"sellerID", candidate.State.SellerID,
				"error", err,
			)
		} else {
			if enriched.ComparableCount > 0 {
				stats.ComparablesEnriched++
			}
			candidate.State.Product = enriched
		}
		prepared = append(prepared, candidate)
	}
	return prepared, stats
}

func (p *Processor) validateBestBuyNotification(ctx context.Context, item AnalyzedProduct, logger *slog.Logger) (AnalyzedProduct, bool) {
	if p.client == nil || !p.client.canValidateSellerOffers() {
		return item, true
	}
	validation, err := p.client.ValidateSellerOffer(ctx, item.Product, time.Now())
	if err != nil {
		logger.Warn("Best Buy final notification validation failed",
			"sku", item.SKU,
			"seller", item.SellerName,
			"sellerID", item.SellerID,
			"error", err,
		)
		return item, false
	}
	if !validation.Valid {
		logger.Info("Best Buy notification skipped after final validation",
			"sku", item.SKU,
			"seller", item.SellerName,
			"sellerID", item.SellerID,
			"reason", validation.Reason,
		)
		item.Product = mergeSellerProduct(item.Product, validation.Product)
		item.LastSeen = time.Now()
		if saveErr := p.store.SaveBestBuyProduct(ctx, item); saveErr != nil {
			logger.Warn("Failed to persist Best Buy validation skip state",
				"sku", item.SKU,
				"source", item.Source,
				"error", saveErr,
			)
		}
		return item, false
	}

	item.Product = mergeSellerProduct(item.Product, validation.Product)
	item.DiscountPct = computeDiscount(item.Product)
	if item.AlertKind == AlertKindPriceDrop {
		current := effectivePrice(item.Product)
		if current > 0 {
			item.PriceDropAmount = item.InitialEffectivePrice - current
			if item.InitialEffectivePrice > 0 {
				item.PriceDropPct = item.PriceDropAmount / item.InitialEffectivePrice * 100
			}
		}
	}
	return item, true
}

// analyzeNewItems runs the tiered AI analysis pipeline on new products.
// It returns every saved listing so bb_new can post all new inventory with
// labels, while bb_warm_hot and bb_hot can filter on the same AI fields.
func (p *Processor) analyzeNewItems(ctx context.Context, products []Product, enrichSoldComps bool, logger *slog.Logger) ([]AnalyzedProduct, int, int) {
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

		batchResults, t1Count, t2Count, err := p.processBatch(ctx, batch, enrichSoldComps, logger)
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

func (p *Processor) analyzePriceDropItems(ctx context.Context, candidates []priceDropCandidate, enrichSoldComps bool, logger *slog.Logger) ([]AnalyzedProduct, int, int) {
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
		result, err := p.processPriceDropBatch(ctx, batch, enrichSoldComps, logger)
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
func (p *Processor) processBatch(ctx context.Context, batch []Product, enrichSoldComps bool, logger *slog.Logger) ([]AnalyzedProduct, int, int, error) {
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
	if enrichSoldComps && p.soldCompEnricher != nil {
		cfg.BeforeAnalyze = func(ctx context.Context, products []Product) ([]Product, error) {
			return p.soldCompEnricher.EnrichProducts(ctx, products, time.Now(), logger)
		}
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
			examplesStr := ""
			if len(item.SoldCompExamples) > 0 {
				var parts []string
				for i, ex := range item.SoldCompExamples {
					if i >= 3 {
						break
					}
					parts = append(parts, fmt.Sprintf("%q: $%.2f", ex.Title, ex.Price))
				}
				examplesStr = strings.Join(parts, ", ")
			}

			logger.Info("Best Buy deal labeled warm/hot",
				"sku", item.SKU,
				"clean_title", item.CleanTitle,
				"is_warm", item.IsWarm,
				"is_lava_hot", item.IsLavaHot,
				"discount_pct", item.DiscountPct,
				"seller", item.SellerName,
				"source", item.Source,
				"sold_comp_summary", item.SoldCompSummary,
				"sold_comp_examples", examplesStr,
			)
		}
	}
	return result.Items, result.Tier1Passed, result.Tier2Passed, err
}

func (p *Processor) processPriceDropBatch(ctx context.Context, batch []priceDropCandidate, enrichSoldComps bool, logger *slog.Logger) (productmonitor.Result[AnalyzedProduct], error) {
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
	if enrichSoldComps && p.soldCompEnricher != nil {
		cfg.BeforeAnalyze = func(ctx context.Context, candidates []priceDropCandidate) ([]priceDropCandidate, error) {
			products, err := p.soldCompEnricher.EnrichProducts(ctx, priceDropCandidateProducts(candidates), time.Now(), logger)
			if err != nil {
				return candidates, err
			}
			if len(products) != len(candidates) {
				return candidates, fmt.Errorf("sold comp enrichment returned %d products for %d candidates", len(products), len(candidates))
			}
			out := append([]priceDropCandidate(nil), candidates...)
			for i := range out {
				out[i].State.Product = products[i]
			}
			return out, nil
		}
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
			examplesStr := ""
			if len(item.SoldCompExamples) > 0 {
				var parts []string
				for i, ex := range item.SoldCompExamples {
					if i >= 3 {
						break
					}
					parts = append(parts, fmt.Sprintf("%q: $%.2f", ex.Title, ex.Price))
				}
				examplesStr = strings.Join(parts, ", ")
			}

			logger.Info("Best Buy price drop labeled warm/hot",
				"sku", item.SKU,
				"clean_title", item.CleanTitle,
				"is_warm", item.IsWarm,
				"is_lava_hot", item.IsLavaHot,
				"discount_pct", item.DiscountPct,
				"price_drop_pct", item.PriceDropPct,
				"seller", item.SellerName,
				"source", item.Source,
				"sold_comp_summary", item.SoldCompSummary,
				"sold_comp_examples", examplesStr,
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
	return applySoldCompMarketGuard(applyBestBuyComparableGuard(analyzed))
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
	return applySoldCompMarketGuard(applyBestBuyComparableGuard(analyzed))
}

func applyBestBuyComparableGuard(product AnalyzedProduct) AnalyzedProduct {
	if product.ComparableCount <= 0 || product.ComparableMedianPrice <= 0 {
		return product
	}

	wasWarmHot := product.IsWarm || product.IsLavaHot
	warmOK := bestBuyComparableWarmEligible(product.Product)
	lavaOK := bestBuyComparableLavaHotEligible(product.Product)
	if product.IsLavaHot && !lavaOK {
		product.IsLavaHot = false
		if warmOK {
			product.IsWarm = true
			product.Summary = compactBestBuySummary(product.Summary, "Hot label downgraded by comps")
		}
	}
	if product.IsWarm && !warmOK {
		product.IsWarm = false
		product.IsLavaHot = false
	}
	if wasWarmHot && !product.IsWarm && !product.IsLavaHot {
		product.Summary = compactBestBuySummary(product.Summary, "Not enough below comps")
	}
	return product
}

func applySoldCompMarketGuard(product AnalyzedProduct) AnalyzedProduct {
	if product.SoldCompCount <= 0 || product.SoldCompMedianPrice <= 0 {
		return product
	}
	verdict := soldCompMarketVerdictFromSummary(
		effectivePrice(product.Product),
		product.SoldCompCount,
		product.SoldCompMedianPrice,
		product.SoldCompP25Price,
		bestBuySoldCompMinMatches(product.Product),
	)
	if !verdict.EnoughEvidence {
		return product
	}

	switch verdict.Label {
	case soldCompMarketHot:
		if !product.IsLavaHot {
			product.Summary = compactBestBuySummary(product.Summary, "Hot verified by eBay sold comps")
		}
		product.IsWarm = true
		product.IsLavaHot = true
	case soldCompMarketWarm:
		if product.IsLavaHot {
			product.Summary = compactBestBuySummary(product.Summary, "Hot label downgraded by eBay sold comps")
		} else if !product.IsWarm {
			product.Summary = compactBestBuySummary(product.Summary, "Warm verified by eBay sold comps")
		}
		product.IsWarm = true
		product.IsLavaHot = false
	default:
		if product.IsWarm || product.IsLavaHot {
			product.Summary = compactBestBuySummary(product.Summary, "Not enough below eBay sold comps")
		}
		product.IsWarm = false
		product.IsLavaHot = false
	}
	return product
}

func bestBuyComparableWarmEligible(product Product) bool {
	current := effectivePrice(product)
	if current <= 0 || product.ComparableMedianPrice <= 0 || current >= product.ComparableMedianPrice-priceComparisonEpsilon {
		return false
	}
	if !bestBuyComparableFloorEligible(product) {
		return false
	}
	savings := product.ComparableMedianPrice - current
	if product.ComparableCount < comparableWarmMinCount {
		return product.ComparableDiscountPct >= singleComparableWarmMinPct && savings >= singleComparableWarmMinAmount
	}
	return product.ComparableDiscountPct >= comparableWarmMinPct && savings >= comparableWarmMinAmount
}

func bestBuyComparableLavaHotEligible(product Product) bool {
	current := effectivePrice(product)
	if current <= 0 || product.ComparableMedianPrice <= 0 || current >= product.ComparableMedianPrice-priceComparisonEpsilon {
		return false
	}
	if !bestBuyComparableFloorEligible(product) {
		return false
	}
	savings := product.ComparableMedianPrice - current
	return product.ComparableDiscountPct >= comparableLavaHotMinPct && savings >= comparableLavaHotMinAmount
}

func bestBuyComparableFloorEligible(product Product) bool {
	if !bestBuyComparableFloorRequired(product) {
		return true
	}
	floor := bestBuyComparableFloorPrice(product)
	if floor <= 0 {
		return true
	}
	return effectivePrice(product) <= floor-priceComparisonEpsilon
}

func bestBuyComparableFloorRequired(product Product) bool {
	if product.ComparableCount <= 0 {
		return false
	}
	if product.ComparableCount <= comparableSmallSetFloorMaxCount {
		return true
	}
	if product.ComparableMedianPrice <= 0 || product.ComparableLowestPrice <= 0 {
		return false
	}
	spreadPct := (product.ComparableMedianPrice - product.ComparableLowestPrice) / product.ComparableMedianPrice * 100
	return spreadPct >= comparableHighSpreadMinPct
}

func bestBuyComparableFloorPrice(product Product) float64 {
	if product.ComparableCount <= comparableSmallSetFloorMaxCount || product.ComparableP25Price <= 0 {
		return product.ComparableLowestPrice
	}
	return product.ComparableP25Price
}

func compactBestBuySummary(existing, note string) string {
	existing = firstNonEmpty(existing, note)
	if existing == note {
		return existing
	}
	summary := existing + "; " + note
	if len(summary) > 100 {
		return summary[:97] + "..."
	}
	return summary
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
