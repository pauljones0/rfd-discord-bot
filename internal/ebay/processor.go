package ebay

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay/couponinfer"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	// priceDropMinPercent is the minimum percentage drop to trigger a notification.
	priceDropMinPercent = 20.0
	// priceDropMinDollars is the minimum dollar drop to trigger a notification.
	priceDropMinDollars            = 50.0
	defaultCouponDiscoveryInterval = 6 * time.Hour
	defaultCouponDiscoveryBudget   = 75 * time.Second
	storeCouponConfidenceThreshold = 0.90
	storeCouponMaxErrorCents       = 2
)

// EbayStore abstracts the storage operations for the eBay processor.
type EbayStore interface {
	GetActiveEbaySellers(ctx context.Context) ([]EbaySeller, error)
	SeedEbaySellers(ctx context.Context) (bool, error)
	GetEbayPollState(ctx context.Context) (*EbayPollState, error)
	UpdateEbayPollState(ctx context.Context, state EbayPollState) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
	GetTrackedEbayItems(ctx context.Context) (map[string]TrackedItem, error)
	BulkUpsertTrackedEbayItems(ctx context.Context, items []TrackedItem) error
	DeleteTrackedEbayItems(ctx context.Context, itemIDs []string) error
	GetEbayStoreCoupons(ctx context.Context, marketplace, seller string) ([]StoreCoupon, error)
	SaveEbayStoreCoupon(ctx context.Context, coupon StoreCoupon) error
	GetEbayCouponObservations(ctx context.Context, marketplace, seller, signature string) ([]CouponObservation, error)
	SaveEbayCouponObservation(ctx context.Context, observation CouponObservation) error
}

// EbayNotifier abstracts the Discord notification layer for eBay deals.
type EbayNotifier interface {
	SendEbayDeal(ctx context.Context, item EbayItem, subs []models.Subscription) (map[string]string, error)
}

type PaidLimiter interface {
	BeginRun()
	BeforeAttempt(ctx context.Context) error
}

// Processor handles the eBay price-drop monitoring pipeline.
type Processor struct {
	store                   EbayStore
	client                  *Client
	notifier                EbayNotifier
	mu                      sync.Mutex
	couponDiscoveryInterval time.Duration
	couponDiscoveryBudget   time.Duration
	paidLimiter             PaidLimiter
}

// NewProcessor creates a new eBay price-drop processor.
func NewProcessor(store EbayStore, client *Client, notifier EbayNotifier) *Processor {
	return &Processor{
		store:                   store,
		client:                  client,
		notifier:                notifier,
		couponDiscoveryInterval: defaultCouponDiscoveryInterval,
		couponDiscoveryBudget:   defaultCouponDiscoveryBudget,
	}
}

func (p *Processor) SetCouponDiscoveryInterval(interval time.Duration) {
	if interval > 0 {
		p.couponDiscoveryInterval = interval
	}
}

func (p *Processor) SetPaidLimiter(limiter PaidLimiter) {
	p.paidLimiter = limiter
	if p.client != nil && limiter != nil {
		p.client.SetPaidAttemptHook(limiter.BeforeAttempt)
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
	if p.paidLimiter != nil {
		p.paidLimiter.BeginRun()
	}

	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "ebay", "runID", runID)

	var stats struct {
		sellers    int
		fetched    int
		newItems   int
		updated    int
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
			"updated", stats.updated,
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

	// 4. Load existing tracked items from storage
	tracked, err := p.store.GetTrackedEbayItems(ctx)
	if err != nil {
		stats.exitReason = "tracked_load_error"
		return fmt.Errorf("failed to load tracked eBay items: %w", err)
	}

	// 5. Process each fetched item — detect price drops or add new items.
	// Only write to storage when fields actually changed to avoid redundant writes.
	now := time.Now()
	currentIDs := make(map[string]bool, len(apiItems))
	couponCache := make(map[string][]StoreCoupon)
	activatedCoupons := make(map[string]StoreCoupon)
	notifiedIDs := make(map[string]bool)
	var priceDropItems []EbayItem
	var itemsToWrite []TrackedItem

	for _, apiItem := range apiItems {
		itemID := ExtractItemID(apiItem.ItemID)
		currentIDs[itemID] = true

		basePrice := parsePrice(apiItem.Price)
		newPrice := effectiveItemPrice(basePrice, apiItem.CouponDiscount)
		if newPrice <= 0 {
			if apiItem.Price != nil && apiItem.Price.Value != "" {
				logger.Warn("Failed to parse eBay item price, skipping", "itemID", itemID, "price_value", apiItem.Price.Value)
			}
			continue
		}

		existing, exists := tracked[itemID]
		if !exists {
			// New item — queue for batch write, no notification
			stats.newItems++
			itemsToWrite = append(itemsToWrite, TrackedItem{
				ItemID:          itemID,
				Title:           apiItem.Title,
				Price:           newPrice,
				BasePrice:       basePrice,
				CouponDiscount:  apiItem.CouponDiscount,
				CouponCode:      apiItem.CouponCode,
				CouponMessage:   apiItem.CouponMessage,
				CouponSource:    apiItem.CouponSource,
				CouponSignature: apiItem.CouponSignature,
				OriginalPrice:   newPrice,
				Currency:        currencyOrDefault(apiItem.Price),
				Seller:          sellerUsername(apiItem.Seller),
				Condition:       apiItem.Condition,
				ItemURL:         apiItem.ItemWebURL,
				ImageURL:        imageURL(apiItem.Image),
				FirstSeenAt:     now,
				LastSeenAt:      now,
			})
			continue
		}

		// Backfill original price for legacy tracked items that predate this field.
		backfilledOriginalPrice := false
		if existing.OriginalPrice <= 0 {
			existing.OriginalPrice = existing.Price
			backfilledOriginalPrice = true
		}
		backfilledDropCount := false
		if existing.DropCount <= 0 && existing.LastNotifiedPrice > 0 {
			existing.DropCount = 1
			backfilledDropCount = true
		}

		_, _, _, browseDropCandidate := shouldNotifyPriceDrop(existing, newPrice)
		if browseDropCandidate && shouldFetchPageCoupon(existing, basePrice, apiItem.CouponDiscount) {
			var activated *StoreCoupon
			apiItem, newPrice, activated = p.applyCouponForPriceDrop(ctx, apiItem, existing, basePrice, newPrice, couponCache, now, logger)
			if activated != nil {
				activatedCoupons[sellerCouponKey(activated.Marketplace, activated.Seller)] = *activated
			}
		}

		// Existing item — notify on the first qualifying drop from original price,
		// then only on materially deeper drops than the last alerted price.
		baselinePrice, dollarDrop, percentDrop, shouldNotify := shouldNotifyPriceDrop(existing, newPrice)

		if shouldNotify {
			stats.priceDrops++
			existing.DropCount = priorDropCount(existing) + 1
			if apiItem.CouponSignature != "" {
				existing.LastCouponAlertSignature = apiItem.CouponSignature
				existing.LastCouponAlertAt = now
			}
			notifiedIDs[itemID] = true
			logger.Info("Price drop detected",
				"itemID", itemID,
				"title", apiItem.Title,
				"baseline_price", baselinePrice,
				"last_seen_price", existing.Price,
				"base_price", basePrice,
				"coupon_discount", apiItem.CouponDiscount,
				"new_price", newPrice,
				"drop_pct", fmt.Sprintf("%.1f%%", percentDrop),
				"drop_dollars", fmt.Sprintf("$%.2f", dollarDrop),
				"drop_count", existing.DropCount,
			)
			priceDropItems = append(priceDropItems, EbayItem{
				ItemID:                   itemID,
				Title:                    apiItem.Title,
				CurrentPrice:             newPrice,
				PreviousPrice:            baselinePrice,
				BasePrice:                basePrice,
				CouponDiscount:           apiItem.CouponDiscount,
				CouponCode:               apiItem.CouponCode,
				CouponMessage:            apiItem.CouponMessage,
				CouponSource:             apiItem.CouponSource,
				PriceDrop:                dollarDrop,
				PercentDrop:              percentDrop,
				DropCount:                existing.DropCount,
				Currency:                 currencyOrDefault(apiItem.Price),
				ItemURL:                  apiItem.ItemWebURL,
				ImageURL:                 imageURL(apiItem.Image),
				Seller:                   sellerUsername(apiItem.Seller),
				SellerFeedbackScore:      sellerFeedbackScore(apiItem.Seller),
				SellerFeedbackPercentage: sellerFeedbackPercentage(apiItem.Seller),
				Condition:                apiItem.Condition,
				Marketplace:              apiItem.Marketplace,
				ListedAt:                 parseItemCreationDate(apiItem.ItemCreationDate),
			})
			existing.LastNotifiedPrice = newPrice
		}

		// Only write back if something actually changed
		newImgURL := imageURL(apiItem.Image)
		newCurrency := currencyOrDefault(apiItem.Price)
		newSeller := sellerUsername(apiItem.Seller)
		newBasePrice := basePrice
		newCouponDiscount := apiItem.CouponDiscount
		newCouponCode := apiItem.CouponCode
		newCouponMessage := apiItem.CouponMessage
		newCouponSource := apiItem.CouponSource
		newCouponSignature := apiItem.CouponSignature
		if existing.Price != newPrice || existing.Title != apiItem.Title ||
			existing.Condition != apiItem.Condition || existing.ItemURL != apiItem.ItemWebURL ||
			existing.ImageURL != newImgURL || existing.Currency != newCurrency ||
			existing.Seller != newSeller || existing.BasePrice != newBasePrice ||
			existing.CouponDiscount != newCouponDiscount || existing.CouponCode != newCouponCode ||
			existing.CouponMessage != newCouponMessage || existing.CouponSource != newCouponSource ||
			existing.CouponSignature != newCouponSignature ||
			backfilledOriginalPrice || backfilledDropCount || shouldNotify {
			stats.updated++
			existing.Price = newPrice
			existing.BasePrice = newBasePrice
			existing.CouponDiscount = newCouponDiscount
			existing.CouponCode = newCouponCode
			existing.CouponMessage = newCouponMessage
			existing.CouponSource = newCouponSource
			existing.CouponSignature = newCouponSignature
			existing.LastSeenAt = now
			existing.Title = apiItem.Title
			existing.Currency = newCurrency
			existing.Seller = newSeller
			existing.ItemURL = apiItem.ItemWebURL
			existing.ImageURL = newImgURL
			existing.Condition = apiItem.Condition
			itemsToWrite = append(itemsToWrite, existing)
			tracked[itemID] = existing
		}
	}

	if len(activatedCoupons) > 0 {
		retroItems, retroWrites := p.retroactiveCouponAlerts(apiItems, tracked, activatedCoupons, notifiedIDs, now, logger)
		if len(retroItems) > 0 {
			stats.priceDrops += len(retroItems)
			stats.updated += len(retroWrites)
			priceDropItems = append(priceDropItems, retroItems...)
			itemsToWrite = append(itemsToWrite, retroWrites...)
		}
	}

	// Bulk write all new and changed items
	if len(itemsToWrite) > 0 {
		logger.Info("Writing eBay item changes to storage", "count", len(itemsToWrite))
		if err := p.store.BulkUpsertTrackedEbayItems(ctx, itemsToWrite); err != nil {
			logger.Error("Failed to bulk upsert eBay items", "error", err)
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
	if len(priceDropItems) > 0 && p.notifier != nil {
		subs, err := p.store.GetAllSubscriptions(ctx)
		if err != nil {
			logger.Error("Failed to get subscriptions for eBay notifications", "error", err)
		}

		var eligibleSubs []models.Subscription
		for _, sub := range subs {
			if isEbayDealType(sub.DealType) {
				eligibleSubs = append(eligibleSubs, sub)
			}
		}

		if len(eligibleSubs) > 0 {
			for i := range priceDropItems {
				if ctx.Err() != nil {
					logger.Warn("Context cancelled, stopping Discord notifications")
					break
				}
				itemSubs := eligibleEbaySubscriptions(priceDropItems[i], eligibleSubs)
				if len(itemSubs) == 0 {
					continue
				}
				if _, err := p.notifier.SendEbayDeal(ctx, priceDropItems[i], itemSubs); err != nil {
					logger.Error("Failed to send eBay price drop to Discord", "item", priceDropItems[i].Title, "error", err)
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
func isEbayEligible(item EbayItem, sub models.Subscription) bool {
	return dealtypes.EbayEligible(sub.DealType, ebayItemMarketplace(item))
}

func isEbayDealType(dealType string) bool {
	return dealtypes.IsEbay(dealType)
}

func eligibleEbaySubscriptions(item EbayItem, subs []models.Subscription) []models.Subscription {
	var eligible []models.Subscription
	for _, sub := range subs {
		if isEbayEligible(item, sub) {
			eligible = append(eligible, sub)
		}
	}
	return eligible
}

func ebayItemMarketplace(item EbayItem) string {
	switch item.Marketplace {
	case "EBAY_CA", "EBAY_US":
		return item.Marketplace
	}
	if strings.Contains(item.ItemURL, "ebay.com") {
		return "EBAY_US"
	}
	if strings.Contains(item.ItemURL, "ebay.ca") {
		return "EBAY_CA"
	}
	return ""
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

func effectiveItemPrice(basePrice, couponDiscount float64) float64 {
	if basePrice <= 0 {
		return 0
	}
	if couponDiscount <= 0 {
		return basePrice
	}
	effectivePrice := basePrice - couponDiscount
	if effectivePrice < 0 {
		return 0
	}
	return effectivePrice
}

func shouldFetchPageCoupon(existing TrackedItem, basePrice, apiCouponDiscount float64) bool {
	if basePrice <= 0 {
		return false
	}

	baselineBasePrice := existing.BasePrice
	if baselineBasePrice <= 0 {
		baselineBasePrice = existing.Price
	}
	if baselineBasePrice > 0 && basePrice < baselineBasePrice {
		return true
	}

	if apiCouponDiscount > existing.CouponDiscount {
		apiEffective := effectiveItemPrice(basePrice, apiCouponDiscount)
		return apiEffective > 0 && existing.Price > 0 && apiEffective < existing.Price
	}

	return false
}

func shouldNotifyPriceDrop(existing TrackedItem, newPrice float64) (baselinePrice, dollarDrop, percentDrop float64, ok bool) {
	if newPrice <= 0 {
		return 0, 0, 0, false
	}

	baselinePrice = existing.OriginalPrice
	if existing.LastNotifiedPrice > 0 {
		baselinePrice = existing.LastNotifiedPrice
	} else if baselinePrice <= 0 {
		baselinePrice = existing.Price
	}

	if baselinePrice <= 0 {
		return 0, 0, 0, false
	}

	// Once a price has already been alerted, suppress duplicate or worse prices
	// until the listing reaches a new lower notification level.
	if existing.LastNotifiedPrice > 0 && newPrice >= existing.LastNotifiedPrice {
		return baselinePrice, 0, 0, false
	}

	dollarDrop = baselinePrice - newPrice
	if dollarDrop <= 0 {
		return baselinePrice, 0, 0, false
	}

	percentDrop = (dollarDrop / baselinePrice) * 100
	if dollarDrop < priceDropMinDollars || percentDrop < priceDropMinPercent {
		return baselinePrice, dollarDrop, percentDrop, false
	}

	return baselinePrice, dollarDrop, percentDrop, true
}

func priorDropCount(existing TrackedItem) int {
	if existing.DropCount > 0 {
		return existing.DropCount
	}
	if existing.LastNotifiedPrice > 0 {
		return 1
	}
	return 0
}

func (p *Processor) applyCouponForPriceDrop(ctx context.Context, apiItem BrowseAPIItem, existing TrackedItem, basePrice, currentPrice float64, couponCache map[string][]StoreCoupon, now time.Time, logger *slog.Logger) (BrowseAPIItem, float64, *StoreCoupon) {
	marketplace := apiItem.Marketplace
	if marketplace == "" {
		marketplace = "EBAY_CA"
	}
	seller := sellerUsername(apiItem.Seller)
	if seller == "" || seller == "Unknown" {
		return apiItem, currentPrice, nil
	}
	key := sellerCouponKey(marketplace, seller)
	coupons, ok := couponCache[key]
	if !ok {
		loaded, err := p.store.GetEbayStoreCoupons(ctx, marketplace, seller)
		if err != nil {
			logger.Warn("Failed to load eBay seller coupon cache", "seller", seller, "marketplace", marketplace, "error", err)
			return apiItem, currentPrice, nil
		}
		coupons = loaded
		couponCache[key] = coupons
	}

	if cachedCoupon := bestCachedCoupon(coupons, basePrice); cachedCoupon.DiscountAmount > apiItem.CouponDiscount {
		applyCouponSnapshotToItem(&apiItem, cachedCoupon)
		currentPrice = effectiveItemPrice(basePrice, apiItem.CouponDiscount)
		logger.Info("Applied cached eBay seller coupon to effective price",
			"itemID", ExtractItemID(apiItem.ItemID),
			"seller", seller,
			"base_price", basePrice,
			"coupon_discount", apiItem.CouponDiscount,
			"coupon_source", apiItem.CouponSource,
			"effective_price", currentPrice,
		)
		return apiItem, currentPrice, nil
	}

	if couponCacheFresh(coupons, now) {
		return apiItem, currentPrice, nil
	}

	timeout := 30 * time.Second
	if budget := p.couponDiscoveryBudgetOrDefault(); budget > 0 && budget < timeout {
		timeout = budget
	}
	sampleCtx, cancel := context.WithTimeout(ctx, timeout)
	pageCoupon, source, err := p.client.FetchPageCouponWithTimeout(sampleCtx, apiItem, basePrice, timeout)
	cancel()
	if err != nil {
		logger.Warn("Failed to fetch eBay page coupon after API price drop",
			"itemID", ExtractItemID(apiItem.ItemID),
			"seller", seller,
			"backend_order", p.client.couponBackends,
			"error", err,
		)
		blocked := blockedCouponCheck(marketplace, seller, now, p.couponDiscoveryIntervalOrDefault(), err)
		if saveErr := p.store.SaveEbayStoreCoupon(ctx, blocked); saveErr != nil {
			logger.Warn("Failed to save eBay coupon blocked throttle", "seller", seller, "marketplace", marketplace, "error", saveErr)
		} else {
			couponCache[key] = appendFreshCoupon(coupons, blocked)
		}
		return apiItem, currentPrice, nil
	}
	if pageCoupon.DiscountAmount <= 0 {
		negative := negativeCouponCheck(marketplace, seller, coupons, now, p.couponDiscoveryIntervalOrDefault())
		if saveErr := p.store.SaveEbayStoreCoupon(ctx, negative); saveErr != nil {
			logger.Warn("Failed to save eBay no-coupon cache", "seller", seller, "marketplace", marketplace, "error", saveErr)
		} else {
			couponCache[key] = appendFreshCoupon(coupons, negative)
		}
		return apiItem, currentPrice, nil
	}

	observation := CouponObservation{
		Marketplace:    marketplace,
		Seller:         seller,
		Signature:      pageCoupon.Signature,
		ItemID:         ExtractItemID(apiItem.ItemID),
		ItemURL:        apiItem.ItemWebURL,
		BasePrice:      basePrice,
		DiscountAmount: pageCoupon.DiscountAmount,
		Code:           pageCoupon.Code,
		Message:        pageCoupon.Message,
		EvidenceText:   couponEvidenceText(pageCoupon),
		Scope:          pageCoupon.Scope,
		Backend:        source,
		Confidence:     pageCoupon.Confidence,
		ObservedAt:     now,
		ExpiresAt:      pageCoupon.ExpiresAt,
	}
	if err := p.store.SaveEbayCouponObservation(ctx, observation); err != nil {
		logger.Warn("Failed to save eBay coupon observation", "seller", seller, "marketplace", marketplace, "signature", pageCoupon.Signature, "error", err)
	}
	observations, err := p.store.GetEbayCouponObservations(ctx, marketplace, seller, pageCoupon.Signature)
	if err != nil {
		logger.Warn("Failed to load eBay coupon observations", "seller", seller, "marketplace", marketplace, "signature", pageCoupon.Signature, "error", err)
		observations = []CouponObservation{observation}
	} else if !containsCouponObservation(observations, observation) {
		observations = append(observations, observation)
	}

	refreshed := p.storeCouponFromObservation(marketplace, seller, pageCoupon, observations, coupons, now)
	previouslyActive := hasActiveCoupon(coupons, refreshed.Signature, now)
	if err := p.store.SaveEbayStoreCoupon(ctx, refreshed); err != nil {
		logger.Warn("Failed to save eBay seller coupon cache", "seller", seller, "marketplace", marketplace, "signature", refreshed.Signature, "error", err)
	} else {
		couponCache[key] = appendFreshCoupon(coupons, refreshed)
	}

	if refreshed.Active {
		if cachedCoupon := bestCachedCoupon([]StoreCoupon{refreshed}, basePrice); cachedCoupon.DiscountAmount > apiItem.CouponDiscount {
			applyCouponSnapshotToItem(&apiItem, cachedCoupon)
			currentPrice = effectiveItemPrice(basePrice, apiItem.CouponDiscount)
		}
		if !previouslyActive {
			return apiItem, currentPrice, &refreshed
		}
	}

	return apiItem, currentPrice, nil
}

func applyCouponSnapshotToItem(item *BrowseAPIItem, coupon couponSnapshot) {
	item.CouponDiscount = coupon.DiscountAmount
	item.CouponCode = coupon.Code
	item.CouponMessage = coupon.Message
	item.CouponSource = coupon.Source
	item.CouponSignature = coupon.Signature
}

func negativeCouponCheck(marketplace, seller string, existing []StoreCoupon, now time.Time, interval time.Duration) StoreCoupon {
	negative := StoreCoupon{
		Marketplace:         marketplace,
		Seller:              seller,
		Signature:           "none",
		DiscountType:        "none",
		Scope:               "none",
		Confidence:          0.6,
		LastChecked:         now,
		LastSeen:            now,
		NextCheckAt:         now.Add(interval),
		Active:              false,
		ConsecutiveNoCoupon: previousNoCouponCount(existing) + 1,
	}
	if firstSeen := firstSeenForSignature(existing, "none"); !firstSeen.IsZero() {
		negative.FirstSeen = firstSeen
	} else {
		negative.FirstSeen = now
	}
	return negative
}

func blockedCouponCheck(marketplace, seller string, now time.Time, interval time.Duration, err error) StoreCoupon {
	return StoreCoupon{
		Marketplace:  marketplace,
		Seller:       seller,
		Signature:    "blocked",
		DiscountType: "unknown",
		FormulaType:  couponinfer.TypeUnknown,
		RawText:      err.Error(),
		Scope:        "unknown",
		Confidence:   0,
		FirstSeen:    now,
		LastSeen:     now,
		LastChecked:  now,
		NextCheckAt:  now.Add(interval),
		Active:       false,
	}
}

func (p *Processor) storeCouponFromObservation(marketplace, seller string, pageCoupon PageCoupon, observations []CouponObservation, existing []StoreCoupon, now time.Time) StoreCoupon {
	samples := couponSamplesFromObservations(observations)
	inference := couponinfer.Infer(samples)

	scope := "item"
	if pageCoupon.Scope == "store" || distinctPositiveObservationItems(observations) >= 2 {
		scope = "store"
	}

	formulaType := inference.Rule.Type
	discountType := pageCoupon.DiscountType
	discountValue := pageCoupon.DiscountValue
	maxDiscount := pageCoupon.MaxDiscount
	thresholdAmount := 0.0
	signature := pageCoupon.Signature
	if inferredCouponRuleUsable(inference.Rule.Type) {
		discountType, discountValue, maxDiscount, thresholdAmount = storeCouponFieldsFromRule(inference.Rule)
		signature = inference.Rule.Signature(pageCoupon.Code)
	}

	itemIDs, itemURLs := sampledCouponItems(observations)
	nextCheck := now.Add(p.couponDiscoveryIntervalOrDefault())
	if !pageCoupon.ExpiresAt.IsZero() && pageCoupon.ExpiresAt.After(now) {
		nextCheck = pageCoupon.ExpiresAt.Add(15 * time.Minute)
	}

	coupon := StoreCoupon{
		Marketplace:               marketplace,
		Seller:                    seller,
		Signature:                 signature,
		DiscountType:              discountType,
		DiscountValue:             discountValue,
		MaxDiscount:               maxDiscount,
		FormulaType:               formulaType,
		ThresholdAmount:           thresholdAmount,
		Code:                      pageCoupon.Code,
		RawText:                   pageCoupon.Message,
		Confidence:                inference.Confidence,
		InferenceMaxErrorCents:    int(inference.MaxErrorCents),
		InferenceCompetingRules:   inference.CompetingRules,
		InferenceNeedsMoreSamples: inference.NeedsMoreSamples,
		InferenceNextSampleHint:   inference.NextSamplePriceHint,
		Scope:                     scope,
		SampledItemIDs:            itemIDs,
		SampledItemURLs:           itemURLs,
		FirstSeen:                 firstSeenOrNow(existing, signature, now),
		LastSeen:                  now,
		LastChecked:               now,
		ExpiresAt:                 pageCoupon.ExpiresAt,
		NextCheckAt:               nextCheck,
	}
	coupon.Active = storeCouponReadyForStoreWideUse(coupon, now)
	return coupon
}

func couponSamplesFromObservations(observations []CouponObservation) []couponinfer.Sample {
	byItem := make(map[string]couponinfer.Sample)
	for _, obs := range observations {
		if obs.DiscountAmount <= 0 || obs.BasePrice <= 0 {
			continue
		}
		text := strings.TrimSpace(obs.EvidenceText)
		if text == "" {
			text = obs.Message
		}
		key := obs.ItemID
		if key == "" {
			key = obs.ItemURL
		}
		if key == "" {
			key = fmt.Sprintf("%f-%f", obs.BasePrice, obs.DiscountAmount)
		}
		byItem[key] = couponinfer.Sample{
			BaseCents:     priceToCents(obs.BasePrice),
			DiscountCents: priceToCents(obs.DiscountAmount),
			Text:          text,
		}
	}
	samples := make([]couponinfer.Sample, 0, len(byItem))
	for _, sample := range byItem {
		samples = append(samples, sample)
	}
	return samples
}

func distinctPositiveObservationItems(observations []CouponObservation) int {
	seen := make(map[string]bool)
	for _, obs := range observations {
		if obs.DiscountAmount <= 0 {
			continue
		}
		key := obs.ItemID
		if key == "" {
			key = obs.ItemURL
		}
		if key == "" {
			continue
		}
		seen[key] = true
	}
	return len(seen)
}

func sampledCouponItems(observations []CouponObservation) ([]string, []string) {
	seenIDs := make(map[string]bool)
	seenURLs := make(map[string]bool)
	var ids []string
	var urls []string
	for _, obs := range observations {
		if obs.ItemID != "" && !seenIDs[obs.ItemID] {
			seenIDs[obs.ItemID] = true
			ids = append(ids, obs.ItemID)
		}
		if obs.ItemURL != "" && !seenURLs[obs.ItemURL] {
			seenURLs[obs.ItemURL] = true
			urls = append(urls, obs.ItemURL)
		}
	}
	return ids, urls
}

func containsCouponObservation(observations []CouponObservation, target CouponObservation) bool {
	for _, obs := range observations {
		if obs.ItemID == target.ItemID && obs.Signature == target.Signature && obs.ObservedAt.Equal(target.ObservedAt) {
			return true
		}
	}
	return false
}

func hasActiveCoupon(coupons []StoreCoupon, signature string, now time.Time) bool {
	for _, coupon := range coupons {
		if coupon.Signature != signature || !coupon.Active {
			continue
		}
		if !coupon.ExpiresAt.IsZero() && coupon.ExpiresAt.Before(now) {
			continue
		}
		return true
	}
	return false
}

func storeCouponReadyForStoreWideUse(coupon StoreCoupon, now time.Time) bool {
	if coupon.Scope != "store" || !inferredCouponRuleUsable(coupon.FormulaType) {
		return false
	}
	if coupon.Confidence < storeCouponConfidenceThreshold || coupon.InferenceMaxErrorCents > storeCouponMaxErrorCents {
		return false
	}
	if !coupon.ExpiresAt.IsZero() && coupon.ExpiresAt.Before(now) {
		return false
	}
	return true
}

func (p *Processor) retroactiveCouponAlerts(apiItems []BrowseAPIItem, tracked map[string]TrackedItem, activated map[string]StoreCoupon, notifiedIDs map[string]bool, now time.Time, logger *slog.Logger) ([]EbayItem, []TrackedItem) {
	var alerts []EbayItem
	var writes []TrackedItem
	for _, apiItem := range apiItems {
		itemID := ExtractItemID(apiItem.ItemID)
		if notifiedIDs[itemID] {
			continue
		}
		existing, exists := tracked[itemID]
		if !exists {
			continue
		}
		marketplace := apiItem.Marketplace
		if marketplace == "" {
			marketplace = "EBAY_CA"
		}
		seller := sellerUsername(apiItem.Seller)
		coupon, ok := activated[sellerCouponKey(marketplace, seller)]
		if !ok || existing.LastCouponAlertSignature == coupon.Signature {
			continue
		}
		basePrice := parsePrice(apiItem.Price)
		if basePrice <= 0 {
			continue
		}
		discount := storeCouponDiscount(coupon, basePrice)
		if discount <= apiItem.CouponDiscount {
			continue
		}
		effectivePrice := effectiveItemPrice(basePrice, discount)
		baselinePrice, dollarDrop, percentDrop, shouldNotify := shouldNotifyPriceDrop(existing, effectivePrice)
		if !shouldNotify {
			continue
		}

		existing.DropCount = priorDropCount(existing) + 1
		existing.Price = effectivePrice
		existing.BasePrice = basePrice
		existing.CouponDiscount = discount
		existing.CouponCode = coupon.Code
		existing.CouponMessage = coupon.RawText
		existing.CouponSource = "seller-coupon-cache"
		existing.CouponSignature = coupon.Signature
		existing.LastNotifiedPrice = effectivePrice
		existing.LastCouponAlertSignature = coupon.Signature
		existing.LastCouponAlertAt = now
		existing.LastSeenAt = now
		writes = append(writes, existing)
		tracked[itemID] = existing
		notifiedIDs[itemID] = true

		logger.Info("Retroactive eBay seller coupon drop detected",
			"itemID", itemID,
			"seller", seller,
			"coupon_signature", coupon.Signature,
			"base_price", basePrice,
			"coupon_discount", discount,
			"effective_price", effectivePrice,
			"drop_pct", fmt.Sprintf("%.1f%%", percentDrop),
			"drop_dollars", fmt.Sprintf("$%.2f", dollarDrop),
		)

		alerts = append(alerts, EbayItem{
			ItemID:                   itemID,
			Title:                    apiItem.Title,
			CurrentPrice:             effectivePrice,
			PreviousPrice:            baselinePrice,
			BasePrice:                basePrice,
			CouponDiscount:           discount,
			CouponCode:               coupon.Code,
			CouponMessage:            coupon.RawText,
			CouponSource:             "seller-coupon-cache",
			PriceDrop:                dollarDrop,
			PercentDrop:              percentDrop,
			DropCount:                existing.DropCount,
			Currency:                 currencyOrDefault(apiItem.Price),
			ItemURL:                  apiItem.ItemWebURL,
			ImageURL:                 imageURL(apiItem.Image),
			Seller:                   seller,
			SellerFeedbackScore:      sellerFeedbackScore(apiItem.Seller),
			SellerFeedbackPercentage: sellerFeedbackPercentage(apiItem.Seller),
			Condition:                apiItem.Condition,
			Marketplace:              marketplace,
			ListedAt:                 parseItemCreationDate(apiItem.ItemCreationDate),
		})
	}
	return alerts, writes
}

func couponEvidenceText(coupon PageCoupon) string {
	if strings.TrimSpace(coupon.EvidenceText) != "" {
		return coupon.EvidenceText
	}
	return coupon.Message
}

func priceToCents(price float64) int64 {
	if price <= 0 {
		return 0
	}
	return int64(math.Round(price * 100))
}

func inferredCouponRuleUsable(ruleType string) bool {
	switch ruleType {
	case couponinfer.TypeFlat, couponinfer.TypePercent, couponinfer.TypePercentCap,
		couponinfer.TypeThresholdFlat, couponinfer.TypeThresholdPercent:
		return true
	default:
		return false
	}
}

func storeCouponFieldsFromRule(rule couponinfer.Rule) (discountType string, discountValue, maxDiscount, thresholdAmount float64) {
	switch rule.Type {
	case couponinfer.TypeFlat, couponinfer.TypeThresholdFlat:
		discountType = "fixed"
		discountValue = float64(rule.ValueCents) / 100
	case couponinfer.TypePercent, couponinfer.TypePercentCap, couponinfer.TypeThresholdPercent:
		discountType = "percent"
		discountValue = float64(rule.BasisPoints) / 100
		maxDiscount = float64(rule.CapCents) / 100
	default:
		discountType = "unknown"
	}
	thresholdAmount = float64(rule.ThresholdCents) / 100
	return discountType, discountValue, maxDiscount, thresholdAmount
}

func (p *Processor) couponDiscoveryIntervalOrDefault() time.Duration {
	if p.couponDiscoveryInterval > 0 {
		return p.couponDiscoveryInterval
	}
	return defaultCouponDiscoveryInterval
}

func (p *Processor) couponDiscoveryBudgetOrDefault() time.Duration {
	if p.couponDiscoveryBudget > 0 {
		return p.couponDiscoveryBudget
	}
	return defaultCouponDiscoveryBudget
}

func sellerCouponKey(marketplace, seller string) string {
	return strings.ToUpper(strings.TrimSpace(marketplace)) + "|" + strings.ToLower(strings.TrimSpace(seller))
}

func couponCacheFresh(coupons []StoreCoupon, now time.Time) bool {
	for _, coupon := range coupons {
		if coupon.NextCheckAt.After(now) {
			return true
		}
	}
	return false
}

func bestCachedCoupon(coupons []StoreCoupon, basePrice float64) couponSnapshot {
	var best couponSnapshot
	now := time.Now()
	latestChecked := latestCouponCheck(coupons)
	for _, coupon := range coupons {
		if !latestChecked.IsZero() && !coupon.LastChecked.Equal(latestChecked) {
			continue
		}
		if !storeCouponReadyForStoreWideUse(coupon, now) {
			continue
		}
		discount := storeCouponDiscount(coupon, basePrice)
		if discount <= best.DiscountAmount {
			continue
		}
		best = couponSnapshot{
			DiscountAmount: discount,
			Code:           coupon.Code,
			Message:        coupon.RawText,
			Source:         "seller-coupon-cache",
			Signature:      coupon.Signature,
		}
	}
	return best
}

func latestCouponCheck(coupons []StoreCoupon) time.Time {
	var latest time.Time
	for _, coupon := range coupons {
		if coupon.LastChecked.After(latest) {
			latest = coupon.LastChecked
		}
	}
	return latest
}

func storeCouponDiscount(coupon StoreCoupon, basePrice float64) float64 {
	if coupon.ThresholdAmount > 0 && basePrice < coupon.ThresholdAmount {
		return 0
	}
	formulaType := coupon.FormulaType
	if formulaType == "" {
		switch coupon.DiscountType {
		case "fixed":
			formulaType = couponinfer.TypeFlat
		case "percent":
			if coupon.MaxDiscount > 0 {
				formulaType = couponinfer.TypePercentCap
			} else {
				formulaType = couponinfer.TypePercent
			}
		}
	}
	switch formulaType {
	case couponinfer.TypeFlat, couponinfer.TypeThresholdFlat:
		if coupon.DiscountValue >= basePrice {
			return 0
		}
		return roundCents(coupon.DiscountValue)
	case couponinfer.TypePercent, couponinfer.TypePercentCap, couponinfer.TypeThresholdPercent:
		discount := basePrice * coupon.DiscountValue / 100
		if coupon.MaxDiscount > 0 && coupon.MaxDiscount < discount {
			discount = coupon.MaxDiscount
		}
		return roundCents(discount)
	default:
		return 0
	}
}

func appendFreshCoupon(coupons []StoreCoupon, fresh StoreCoupon) []StoreCoupon {
	out := make([]StoreCoupon, 0, len(coupons)+1)
	for _, coupon := range coupons {
		if coupon.Signature == fresh.Signature {
			continue
		}
		out = append(out, coupon)
	}
	out = append(out, fresh)
	return out
}

func previousNoCouponCount(coupons []StoreCoupon) int {
	for _, coupon := range coupons {
		if coupon.Signature == "none" {
			return coupon.ConsecutiveNoCoupon
		}
	}
	return 0
}

func firstSeenForSignature(coupons []StoreCoupon, signature string) time.Time {
	for _, coupon := range coupons {
		if coupon.Signature == signature {
			return coupon.FirstSeen
		}
	}
	return time.Time{}
}

func firstSeenOrNow(coupons []StoreCoupon, signature string, now time.Time) time.Time {
	if firstSeen := firstSeenForSignature(coupons, signature); !firstSeen.IsZero() {
		return firstSeen
	}
	return now
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

func sellerFeedbackScore(s *SellerInfo) int {
	if s == nil {
		return 0
	}
	return s.FeedbackScore
}

func sellerFeedbackPercentage(s *SellerInfo) string {
	if s == nil {
		return ""
	}
	return s.FeedbackPercentage
}

// imageURL extracts the image URL from an Image, or returns empty string.
func imageURL(img *Image) string {
	if img == nil {
		return ""
	}
	return img.ImageURL
}

func parseItemCreationDate(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	createdAt, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return createdAt
}
