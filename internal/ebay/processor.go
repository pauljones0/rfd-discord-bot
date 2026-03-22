package ebay

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
	// ebayBatchSize is the number of items to send to Gemini tier-1 screening at once.
	ebayBatchSize = 10
)

// EbayStore abstracts the storage operations for the eBay processor.
type EbayStore interface {
	GetActiveEbaySellers(ctx context.Context) ([]EbaySeller, error)
	SeedEbaySellers(ctx context.Context) (bool, error)
	GetEbayPollState(ctx context.Context) (*EbayPollState, error)
	UpdateEbayPollState(ctx context.Context, state EbayPollState) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// EbayAnalyzer abstracts the AI analysis for eBay deals.
type EbayAnalyzer interface {
	ScreenEbayBatch(ctx context.Context, items []BrowseAPIItem) ([]EbayBatchScreenResult, error)
	VerifyEbayDeal(ctx context.Context, item BrowseAPIItem, screenTitle string) (*EbayVerifyResult, error)
}

// EbayNotifier abstracts the Discord notification layer for eBay deals.
type EbayNotifier interface {
	SendEbayDeal(ctx context.Context, item EbayItem, subs []models.Subscription) (map[string]string, error)
}

// Processor handles the eBay deal processing pipeline.
type Processor struct {
	store    EbayStore
	client   *Client
	analyzer EbayAnalyzer
	notifier EbayNotifier
	mu       sync.Mutex
}

// NewProcessor creates a new eBay deal processor.
func NewProcessor(store EbayStore, client *Client, analyzer EbayAnalyzer, notifier EbayNotifier) *Processor {
	return &Processor{
		store:    store,
		client:   client,
		analyzer: analyzer,
		notifier: notifier,
	}
}

// ProcessEbayDeals runs the full eBay deal processing pipeline.
func (p *Processor) ProcessEbayDeals(ctx context.Context) error {
	if p.client == nil {
		slog.Info("eBay client not configured, skipping eBay processing", "processor", "ebay")
		return nil
	}

	if !p.mu.TryLock() {
		slog.Info("ProcessEbayDeals: already in progress, skipping", "processor", "ebay")
		return nil
	}
	defer p.mu.Unlock()

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "ebay", "runID", runID)

	// Pipeline stats for end-of-run summary
	var stats struct {
		sellers      int
		fetched      int
		tier1Passed  int
		tier2Passed  int
		notified     int
		notifyErrors int
		exitReason   string
	}

	defer func() {
		logger.Info("eBay pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.sellers,
			"fetched", stats.fetched,
			"tier1_passed", stats.tier1Passed,
			"tier2_passed", stats.tier2Passed,
			"notified", stats.notified,
			"notify_errors", stats.notifyErrors,
			"exit_reason", stats.exitReason,
		)
	}()

	// 1. Seed sellers if needed
	seeded, err := p.store.SeedEbaySellers(ctx)
	if err != nil {
		logger.Warn("Failed to seed eBay sellers", "error", err)
	} else if seeded {
		logger.Info("Seeded eBay sellers with defaults")
	}

	// 2. Get active sellers
	sellers, err := p.store.GetActiveEbaySellers(ctx)
	if err != nil {
		stats.exitReason = "seller_load_error"
		return fmt.Errorf("failed to get active eBay sellers: %w", err)
	}
	if len(sellers) == 0 {
		stats.exitReason = "no_active_sellers"
		logger.Info("No active eBay sellers configured")
		return nil
	}
	stats.sellers = len(sellers)

	// 3. Determine time window — only fetch items listed since last successful poll
	var sinceTime time.Time
	pollState, err := p.store.GetEbayPollState(ctx)
	if err != nil {
		logger.Warn("Failed to get eBay poll state, fetching all listings", "error", err)
	} else if pollState != nil && !pollState.LastPollTime.IsZero() && pollState.LastError == "" {
		sinceTime = pollState.LastPollTime
		logger.Info("Fetching items listed since last poll", "since", sinceTime.Format(time.RFC3339))
	}

	// 4. Fetch listings from eBay API
	apiItems, err := p.client.SearchSellerListings(ctx, sellers, sinceTime)
	if err != nil {
		stats.exitReason = "api_fetch_error"
		if stateErr := p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime: time.Now(),
			LastError:    err.Error(),
		}); stateErr != nil {
			logger.Warn("Failed to update eBay poll state", "error", stateErr)
		}
		return fmt.Errorf("failed to fetch eBay listings: %w", err)
	}
	stats.fetched = len(apiItems)
	logger.Info("Fetched eBay listings", "total_items", len(apiItems))

	if len(apiItems) == 0 {
		stats.exitReason = "no_new_items"
		if stateErr := p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime:  time.Now(),
			LastPollItems: 0,
		}); stateErr != nil {
			logger.Warn("Failed to update eBay poll state", "error", stateErr)
		}
		return nil
	}

	// 5. Tiered AI Analysis
	warmHotItems, t1Passed := p.analyzeNewItems(ctx, apiItems, logger)
	stats.tier1Passed = t1Passed
	stats.tier2Passed = len(warmHotItems)

	if len(warmHotItems) == 0 {
		stats.exitReason = "no_warm_hot"
		if stateErr := p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime:  time.Now(),
			LastPollItems: len(apiItems),
		}); stateErr != nil {
			logger.Warn("Failed to update eBay poll state", "error", stateErr)
		}
		return nil
	}

	// 6. Send to Discord
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		logger.Error("Failed to get subscriptions for eBay notifications", "error", err)
	}

	for i := range warmHotItems {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping Discord notifications")
			break
		}

		item := &warmHotItems[i]

		var eligibleSubs []models.Subscription
		for _, sub := range subs {
			if isEbayEligible(*item, sub) {
				eligibleSubs = append(eligibleSubs, sub)
			}
		}

		if len(eligibleSubs) > 0 && p.notifier != nil {
			if _, err := p.notifier.SendEbayDeal(ctx, *item, eligibleSubs); err != nil {
				logger.Error("Failed to send eBay deal to Discord", "item", item.Title, "error", err)
				stats.notifyErrors++
			} else {
				stats.notified++
			}
		}
	}

	// 7. Update poll state
	if stateErr := p.store.UpdateEbayPollState(ctx, EbayPollState{
		LastPollTime:  time.Now(),
		LastPollItems: len(apiItems),
	}); stateErr != nil {
		logger.Warn("Failed to update eBay poll state", "error", stateErr)
	}

	stats.exitReason = "success"
	return nil
}

// analyzeNewItems runs the tiered AI analysis pipeline on new items.
// Returns items that passed both tiers (warm or hot) and the total tier-1 pass count.
func (p *Processor) analyzeNewItems(ctx context.Context, items []BrowseAPIItem, logger *slog.Logger) ([]EbayItem, int) {
	var warmHotItems []EbayItem
	totalTier1 := 0

	for i := 0; i < len(items); i += ebayBatchSize {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping batch analysis", "processed", i, "total", len(items))
			break
		}

		// Add inter-batch delay to reduce Gemini API rate limit pressure.
		// Skip delay before the first batch.
		if i > 0 {
			select {
			case <-ctx.Done():
				logger.Warn("Context cancelled during inter-batch delay", "processed", i, "total", len(items))
				return warmHotItems, totalTier1
			case <-time.After(2 * time.Second):
			}
		}

		end := i + ebayBatchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		batchResults, t1Count, err := p.processBatch(ctx, batch, logger)
		warmHotItems = append(warmHotItems, batchResults...)
		totalTier1 += t1Count
		if err != nil {
			logger.Warn("Stopping batch analysis early, AI models exhausted", "processed", i+len(batch), "total", len(items))
			break
		}
	}

	return warmHotItems, totalTier1
}

// processBatch handles a single batch through both AI tiers.
// Returns warm/hot items, the count of items that passed tier 1, and any error.
// A non-nil error signals the caller to stop processing further batches (e.g. all model tiers exhausted).
func (p *Processor) processBatch(ctx context.Context, batch []BrowseAPIItem, logger *slog.Logger) ([]EbayItem, int, error) {
	var results []EbayItem

	if p.analyzer == nil {
		logger.Warn("AI analyzer not available, skipping eBay batch analysis")
		return results, 0, nil
	}

	// Tier 1: Batch screening
	screenResults, err := p.analyzer.ScreenEbayBatch(ctx, batch)
	if err != nil {
		// Known exhaustion state — log at Warn to reduce noise since this is expected until cooldown/reset.
		if strings.Contains(err.Error(), "all model tiers exhausted") {
			logger.Warn("Tier-1 eBay batch screening skipped, AI quota exhausted", "batch_size", len(batch))
			return results, 0, err
		}
		logger.Error("Tier-1 eBay batch screening failed", "batch_size", len(batch), "error", err)
		return results, 0, nil
	}

	screenMap := make(map[string]EbayBatchScreenResult)
	for _, r := range screenResults {
		screenMap[r.ItemID] = r
	}

	var tier1Passed []struct {
		apiItem      BrowseAPIItem
		screenResult EbayBatchScreenResult
	}

	for _, item := range batch {
		id := ExtractItemID(item.ItemID)
		screen, ok := screenMap[id]
		if !ok || !screen.IsTopDeal {
			continue
		}
		tier1Passed = append(tier1Passed, struct {
			apiItem      BrowseAPIItem
			screenResult EbayBatchScreenResult
		}{item, screen})
	}

	logger.Info("Tier-1 screening results",
		"batch_size", len(batch),
		"passed_tier1", len(tier1Passed),
	)

	// Tier 2: Individual verification with grounded search
	for _, candidate := range tier1Passed {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping tier-2 verification")
			break
		}

		verifyResult, err := p.analyzer.VerifyEbayDeal(ctx, candidate.apiItem, candidate.screenResult.CleanTitle)
		if err != nil {
			logger.Warn("Tier-2 eBay deal verification failed",
				"item", candidate.apiItem.Title,
				"error", err,
			)
			continue
		}

		if verifyResult == nil {
			logger.Info("Item failed tier-2 verification (nil result)",
				"item", candidate.apiItem.Title,
			)
			continue
		}
		if !verifyResult.IsWarm && !verifyResult.IsLavaHot {
			logger.Info("Item failed tier-2 verification (not warm/hot)",
				"item", candidate.apiItem.Title,
				"clean_title", verifyResult.CleanTitle,
			)
			continue
		}

		item := apiItemToEbayItem(candidate.apiItem, verifyResult)
		results = append(results, item)

		logger.Info("eBay deal passed both tiers",
			"item_id", item.ItemID,
			"clean_title", item.CleanTitle,
			"is_warm", item.IsWarm,
			"is_lava_hot", item.IsLavaHot,
			"price", item.Price,
			"seller", item.Seller,
		)
	}

	return results, len(tier1Passed), nil
}

// apiItemToEbayItem converts a Browse API item + verification result into an EbayItem for notification.
func apiItemToEbayItem(apiItem BrowseAPIItem, verify *EbayVerifyResult) EbayItem {
	item := EbayItem{
		ItemID:     ExtractItemID(apiItem.ItemID),
		Title:      apiItem.Title,
		CleanTitle: verify.CleanTitle,
		ItemURL:    apiItem.ItemWebURL,
		Condition:  apiItem.Condition,
		IsWarm:     verify.IsWarm,
		IsLavaHot:  verify.IsLavaHot,
	}

	if apiItem.Price != nil {
		item.Price = apiItem.Price.Value
		item.Currency = apiItem.Price.Currency
	}

	if apiItem.Image != nil {
		item.ImageURL = apiItem.Image.ImageURL
	}

	if apiItem.Seller != nil {
		item.Seller = apiItem.Seller.Username
	}

	return item
}

// isEbayEligible checks whether an eBay deal should be sent to a given subscription.
func isEbayEligible(item EbayItem, sub models.Subscription) bool {
	switch sub.DealType {
	case "ebay_warm_hot":
		return item.IsWarm || item.IsLavaHot
	case "ebay_hot":
		return item.IsLavaHot
	case "warm_hot_all":
		return item.IsWarm || item.IsLavaHot
	case "hot_all":
		return item.IsLavaHot
	default:
		return false
	}
}
