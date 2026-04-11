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
	// priceDropMinPercent is the minimum percentage drop to trigger a notification.
	priceDropMinPercent = 20.0
	// priceDropMinDollars is the minimum dollar drop to trigger a notification.
	priceDropMinDollars = 50.0
)

// EbayStore abstracts the storage operations for the eBay processor.
type EbayStore interface {
	GetActiveEbaySellers(ctx context.Context) ([]EbaySeller, error)
	SeedEbaySellers(ctx context.Context) (bool, error)
	GetEbayPollState(ctx context.Context) (*EbayPollState, error)
	UpdateEbayPollState(ctx context.Context, state EbayPollState) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
	GetTrackedEbayItems(ctx context.Context) (map[string]TrackedItem, error)
	UpsertTrackedEbayItem(ctx context.Context, item TrackedItem) error
	DeleteTrackedEbayItems(ctx context.Context, itemIDs []string) error
}

// EbayNotifier abstracts the Discord notification layer for eBay deals.
type EbayNotifier interface {
	SendEbayDeal(ctx context.Context, item EbayItem, subs []models.Subscription) (map[string]string, error)
}

// Processor handles the eBay price-drop monitoring pipeline.
type Processor struct {
	store    EbayStore
	client   *Client
	notifier EbayNotifier
	mu       sync.Mutex
}

// NewProcessor creates a new eBay price-drop processor.
func NewProcessor(store EbayStore, client *Client, notifier EbayNotifier) *Processor {
	return &Processor{
		store:    store,
		client:   client,
		notifier: notifier,
	}
}

// ProcessEbayDeals runs the eBay price-drop monitoring pipeline.
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

	var stats struct {
		sellers    int
		fetched    int
		newItems   int
		priceDrops int
		removed    int
		exitReason string
	}

	defer func() {
		logger.Info("eBay pipeline run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"sellers", stats.sellers,
			"fetched", stats.fetched,
			"new_items", stats.newItems,
			"price_drops", stats.priceDrops,
			"removed", stats.removed,
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

	// 3. Fetch all current listings (no sinceTime filter — we need the full set)
	apiItems, err := p.client.SearchSellerListings(ctx, sellers, time.Time{})
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

	// 4. Load existing tracked items from Firestore
	tracked, err := p.store.GetTrackedEbayItems(ctx)
	if err != nil {
		stats.exitReason = "tracked_load_error"
		return fmt.Errorf("failed to load tracked eBay items: %w", err)
	}

	// 5. Process each fetched item — detect price drops or add new items
	now := time.Now()
	currentIDs := make(map[string]bool, len(apiItems))
	var priceDropItems []EbayItem

	for _, apiItem := range apiItems {
		itemID := ExtractItemID(apiItem.ItemID)
		currentIDs[itemID] = true

		newPrice := parsePrice(apiItem.Price)
		if newPrice <= 0 {
			continue
		}

		existing, exists := tracked[itemID]
		if !exists {
			// New item — track it, no notification
			stats.newItems++
			item := TrackedItem{
				ItemID:      itemID,
				Title:       apiItem.Title,
				Price:       newPrice,
				Currency:    currencyOrDefault(apiItem.Price),
				Seller:      sellerUsername(apiItem.Seller),
				Condition:   apiItem.Condition,
				ItemURL:     apiItem.ItemWebURL,
				ImageURL:    imageURL(apiItem.Image),
				FirstSeenAt: now,
				LastSeenAt:  now,
			}
			if err := p.store.UpsertTrackedEbayItem(ctx, item); err != nil {
				logger.Warn("Failed to track new eBay item", "itemID", itemID, "error", err)
			}
			continue
		}

		// Existing item — check for price drop
		oldPrice := existing.Price
		dollarDrop := oldPrice - newPrice
		percentDrop := (dollarDrop / oldPrice) * 100

		if dollarDrop >= priceDropMinDollars && percentDrop >= priceDropMinPercent {
			stats.priceDrops++
			logger.Info("Price drop detected",
				"itemID", itemID,
				"title", apiItem.Title,
				"old_price", oldPrice,
				"new_price", newPrice,
				"drop_pct", fmt.Sprintf("%.1f%%", percentDrop),
				"drop_dollars", fmt.Sprintf("$%.2f", dollarDrop),
			)
			priceDropItems = append(priceDropItems, EbayItem{
				ItemID:    itemID,
				Title:     apiItem.Title,
				Price:     fmt.Sprintf("%.2f", newPrice),
				Currency:  currencyOrDefault(apiItem.Price),
				ItemURL:   apiItem.ItemWebURL,
				ImageURL:  imageURL(apiItem.Image),
				Seller:    sellerUsername(apiItem.Seller),
				Condition: apiItem.Condition,
			})
		}

		// Update tracked price and lastSeenAt
		existing.Price = newPrice
		existing.LastSeenAt = now
		existing.Title = apiItem.Title
		existing.ItemURL = apiItem.ItemWebURL
		existing.ImageURL = imageURL(apiItem.Image)
		existing.Condition = apiItem.Condition
		if err := p.store.UpsertTrackedEbayItem(ctx, existing); err != nil {
			logger.Warn("Failed to update tracked eBay item", "itemID", itemID, "error", err)
		}
	}

	// 6. Remove items no longer in API results (sold/ended)
	var removedIDs []string
	for itemID := range tracked {
		if !currentIDs[itemID] {
			removedIDs = append(removedIDs, itemID)
		}
	}
	if len(removedIDs) > 0 {
		stats.removed = len(removedIDs)
		logger.Info("Removing stale eBay items", "count", len(removedIDs))
		if err := p.store.DeleteTrackedEbayItems(ctx, removedIDs); err != nil {
			logger.Warn("Failed to delete stale eBay items", "error", err)
		}
	}

	// 7. Notify Discord for price drops
	if len(priceDropItems) > 0 {
		subs, err := p.store.GetAllSubscriptions(ctx)
		if err != nil {
			logger.Error("Failed to get subscriptions for eBay notifications", "error", err)
		}

		for i := range priceDropItems {
			if ctx.Err() != nil {
				logger.Warn("Context cancelled, stopping Discord notifications")
				break
			}

			item := &priceDropItems[i]
			var eligibleSubs []models.Subscription
			for _, sub := range subs {
				if isEbayEligible(sub) {
					eligibleSubs = append(eligibleSubs, sub)
				}
			}

			if len(eligibleSubs) > 0 && p.notifier != nil {
				if _, err := p.notifier.SendEbayDeal(ctx, *item, eligibleSubs); err != nil {
					logger.Error("Failed to send eBay price drop to Discord", "item", item.Title, "error", err)
				}
			}
		}
	}

	// 8. Update poll state
	if stateErr := p.store.UpdateEbayPollState(ctx, EbayPollState{
		LastPollTime:  time.Now(),
		LastPollItems: len(apiItems),
	}); stateErr != nil {
		logger.Warn("Failed to update eBay poll state", "error", stateErr)
	}

	stats.exitReason = "success"
	return nil
}

// isEbayEligible checks whether a price drop should be sent to a given subscription.
func isEbayEligible(sub models.Subscription) bool {
	switch sub.DealType {
	case "ebay_price_drop", "ebay_warm_hot", "ebay_hot", "warm_hot_all", "hot_all":
		return true
	default:
		return false
	}
}

// parsePrice extracts a float64 price from a Browse API Price object.
func parsePrice(p *Price) float64 {
	if p == nil || p.Value == "" {
		return 0
	}
	var f float64
	if _, err := fmt.Sscanf(p.Value, "%f", &f); err != nil {
		return 0
	}
	return f
}

// currencyOrDefault returns the currency from a Price, defaulting to "CAD".
func currencyOrDefault(p *Price) string {
	if p == nil || p.Currency == "" {
		return "CAD"
	}
	return p.Currency
}

// sellerUsername extracts the username from a SellerInfo, or returns "Unknown".
func sellerUsername(s *SellerInfo) string {
	if s == nil {
		return "Unknown"
	}
	return s.Username
}

// imageURL extracts the image URL from an Image, or returns empty string.
func imageURL(img *Image) string {
	if img == nil {
		return ""
	}
	return img.ImageURL
}
