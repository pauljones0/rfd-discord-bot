package ebay

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	// ebayBatchSize is the number of items to send to Gemini tier-1 screening at once.
	ebayBatchSize = 10

	// maxStoredEbayItems caps the ebay_items collection size.
	maxStoredEbayItems = 500
)

// EbayStore abstracts the storage operations for the eBay processor.
type EbayStore interface {
	GetActiveEbaySellers(ctx context.Context) ([]EbaySeller, error)
	SeedEbaySellers(ctx context.Context) (bool, error)
	GetEbayItemsByIDs(ctx context.Context, itemIDs []string) (map[string]*EbayItem, error)
	BatchWriteEbayItems(ctx context.Context, items []EbayItem) error
	TrimOldEbayItems(ctx context.Context, maxItems int) error
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
		slog.Info("eBay client not configured, skipping eBay processing")
		return nil
	}

	if !p.mu.TryLock() {
		slog.Info("ProcessEbayDeals: already in progress, skipping")
		return nil
	}
	defer p.mu.Unlock()

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("runID", runID, "source", "ebay")

	// Pipeline stats for end-of-run summary
	var stats struct {
		sellers      int
		fetched      int
		newItems     int
		tier1Passed  int
		tier2Passed  int
		notified     int
		notifyErrors int
		storeErrors  int
		exitReason   string
	}

	defer func() {
		logger.Info("eBay pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.sellers,
			"fetched", stats.fetched,
			"new_items", stats.newItems,
			"tier1_passed", stats.tier1Passed,
			"tier2_passed", stats.tier2Passed,
			"notified", stats.notified,
			"notify_errors", stats.notifyErrors,
			"store_errors", stats.storeErrors,
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

	logger.Info("Loaded active eBay sellers", "count", len(sellers))

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
		pollErr := err.Error()
		p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime: time.Now(),
			LastError:    pollErr,
		})
		return fmt.Errorf("failed to fetch eBay listings: %w", err)
	}
	stats.fetched = len(apiItems)
	logger.Info("Fetched eBay listings", "total_items", len(apiItems))

	// 4. Diff against Firestore to find new items
	itemIDs := make([]string, len(apiItems))
	apiItemMap := make(map[string]BrowseAPIItem, len(apiItems))
	for i, item := range apiItems {
		id := ExtractItemID(item.ItemID)
		itemIDs[i] = id
		apiItemMap[id] = item
	}

	existingItems, err := p.store.GetEbayItemsByIDs(ctx, itemIDs)
	if err != nil {
		logger.Warn("Failed to check existing eBay items", "error", err)
		existingItems = make(map[string]*EbayItem)
	}

	var newAPIItems []BrowseAPIItem
	for _, item := range apiItems {
		id := ExtractItemID(item.ItemID)
		if _, exists := existingItems[id]; !exists {
			newAPIItems = append(newAPIItems, item)
		}
	}
	stats.newItems = len(newAPIItems)
	logger.Info("New eBay items to analyze", "new", len(newAPIItems), "existing", len(existingItems))

	if len(newAPIItems) == 0 {
		stats.exitReason = "no_new_items"
		p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime:  time.Now(),
			LastPollItems: len(apiItems),
		})
		return nil
	}

	// 5. Tiered AI Analysis
	warmHotItems, t1Passed := p.analyzeNewItems(ctx, newAPIItems, logger)
	stats.tier1Passed = t1Passed
	stats.tier2Passed = len(warmHotItems)
	logger.Info("AI analysis complete", "warm_hot_count", len(warmHotItems))

	if len(warmHotItems) == 0 {
		stats.exitReason = "no_warm_hot"
		p.store.UpdateEbayPollState(ctx, EbayPollState{
			LastPollTime:  time.Now(),
			LastPollItems: len(apiItems),
		})
		return nil
	}

	// 6. Send to Discord
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		logger.Error("Failed to get subscriptions for eBay notifications", "error", err)
	}

	for i := range warmHotItems {
		item := &warmHotItems[i]

		// Filter to eBay-eligible subscriptions
		var eligibleSubs []models.Subscription
		for _, sub := range subs {
			if isEbayEligible(*item, sub) {
				eligibleSubs = append(eligibleSubs, sub)
			}
		}

		if len(eligibleSubs) > 0 && p.notifier != nil {
			msgIDs, err := p.notifier.SendEbayDeal(ctx, *item, eligibleSubs)
			if err != nil {
				logger.Error("Failed to send eBay deal to Discord", "item", item.Title, "error", err)
				stats.notifyErrors++
			} else {
				item.DiscordMessageIDs = msgIDs
				stats.notified++
			}
		}
	}

	// 7. Persist warm/hot items to Firestore
	if err := p.store.BatchWriteEbayItems(ctx, warmHotItems); err != nil {
		logger.Error("Failed to batch write eBay items", "error", err)
		stats.storeErrors++
	} else {
		logger.Info("Persisted warm/hot eBay items", "count", len(warmHotItems))
	}

	// 8. Trim old items
	if err := p.store.TrimOldEbayItems(ctx, maxStoredEbayItems); err != nil {
		logger.Warn("Failed to trim old eBay items", "error", err)
	}

	// 9. Update poll state
	p.store.UpdateEbayPollState(ctx, EbayPollState{
		LastPollTime:  time.Now(),
		LastPollItems: len(apiItems),
	})

	stats.exitReason = "success"
	return nil
}

// analyzeNewItems runs the tiered AI analysis pipeline on new items.
// Returns items that passed both tiers (warm or hot) and the total tier-1 pass count.
func (p *Processor) analyzeNewItems(ctx context.Context, items []BrowseAPIItem, logger *slog.Logger) ([]EbayItem, int) {
	var warmHotItems []EbayItem
	totalTier1 := 0

	// Process in batches of ebayBatchSize
	for i := 0; i < len(items); i += ebayBatchSize {
		end := i + ebayBatchSize
		if end > len(items) {
			end = len(items)
		}
		batch := items[i:end]

		batchResults, t1Count := p.processBatch(ctx, batch, logger)
		warmHotItems = append(warmHotItems, batchResults...)
		totalTier1 += t1Count
	}

	return warmHotItems, totalTier1
}

// processBatch handles a single batch through both AI tiers.
// Returns warm/hot items and the count of items that passed tier 1.
func (p *Processor) processBatch(ctx context.Context, batch []BrowseAPIItem, logger *slog.Logger) ([]EbayItem, int) {
	var results []EbayItem

	if p.analyzer == nil {
		logger.Warn("AI analyzer not available, skipping eBay batch analysis")
		return results, 0
	}

	// Tier 1: Batch screening
	screenResults, err := p.analyzer.ScreenEbayBatch(ctx, batch)
	if err != nil {
		logger.Error("Tier-1 eBay batch screening failed", "batch_size", len(batch), "error", err)
		return results, 0
	}

	// Build lookup of screen results by item ID
	screenMap := make(map[string]EbayBatchScreenResult)
	for _, r := range screenResults {
		screenMap[r.ItemID] = r
	}

	// Collect items that passed tier 1
	var tier1Passed []struct {
		apiItem    BrowseAPIItem
		screenResult EbayBatchScreenResult
	}

	for _, item := range batch {
		id := ExtractItemID(item.ItemID)
		screen, ok := screenMap[id]
		if !ok || !screen.IsTopDeal {
			continue
		}
		tier1Passed = append(tier1Passed, struct {
			apiItem    BrowseAPIItem
			screenResult EbayBatchScreenResult
		}{item, screen})
	}

	logger.Info("Tier-1 screening results",
		"batch_size", len(batch),
		"passed_tier1", len(tier1Passed),
	)

	// Tier 2: Individual verification with grounded search
	for _, candidate := range tier1Passed {
		verifyResult, err := p.analyzer.VerifyEbayDeal(ctx, candidate.apiItem, candidate.screenResult.CleanTitle)
		if err != nil {
			logger.Warn("Tier-2 eBay deal verification failed",
				"item", candidate.apiItem.Title,
				"error", err,
			)
			continue
		}

		if verifyResult == nil || (!verifyResult.IsWarm && !verifyResult.IsLavaHot) {
			logger.Info("Item failed tier-2 verification (not warm/hot)",
				"item", candidate.apiItem.Title,
				"clean_title", verifyResult.CleanTitle,
			)
			continue
		}

		// Convert to EbayItem for storage
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

	return results, len(tier1Passed)
}

// apiItemToEbayItem converts a Browse API item + verification result into a storable EbayItem.
func apiItemToEbayItem(apiItem BrowseAPIItem, verify *EbayVerifyResult) EbayItem {
	now := time.Now()
	itemID := ExtractItemID(apiItem.ItemID)

	item := EbayItem{
		ItemID:      itemID,
		Title:       apiItem.Title,
		CleanTitle:  verify.CleanTitle,
		ItemURL:     apiItem.ItemWebURL,
		Seller:      "",
		Condition:   apiItem.Condition,
		CategoryID:  apiItem.CategoryID,
		IsWarm:      verify.IsWarm,
		IsLavaHot:   verify.IsLavaHot,
		FirstSeenAt: now,
		LastCheckedAt: now,
		LastUpdated: now,
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

	// Parse listing date
	if apiItem.ItemCreationDate != "" {
		if t, err := time.Parse(time.RFC3339, apiItem.ItemCreationDate); err == nil {
			item.ListingDate = t
		} else {
			// Try alternative format
			if t, err := time.Parse("2006-01-02T15:04:05.000Z", apiItem.ItemCreationDate); err == nil {
				item.ListingDate = t
			} else {
				item.ListingDate = now
			}
		}
	}

	return item
}

// isEbayEligible checks whether an eBay deal should be sent to a given subscription.
func isEbayEligible(item EbayItem, sub models.Subscription) bool {
	switch sub.DealType {
	// eBay-specific subscriptions
	case "ebay_warm_hot":
		return item.IsWarm || item.IsLavaHot
	case "ebay_hot":
		return item.IsLavaHot

	// Cross-source subscriptions (receive both RFD and eBay)
	case "warm_hot_all":
		return item.IsWarm || item.IsLavaHot
	case "hot_all":
		return item.IsLavaHot

	// RFD-only subscriptions — eBay deals should NOT go here
	case "rfd_all", "rfd_tech", "rfd_warm_hot", "rfd_warm_hot_tech",
		"rfd_hot", "rfd_hot_tech":
		return false

	default:
		return false
	}
}
