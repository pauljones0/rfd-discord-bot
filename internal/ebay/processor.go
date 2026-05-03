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
	// priceDropMinPercent is the minimum percentage drop to trigger a notification.
	priceDropMinPercent = 20.0
	// priceDropMinDollars is the minimum dollar drop to trigger a notification.
	priceDropMinDollars            = 50.0
	defaultCouponDiscoveryInterval = 6 * time.Hour
	defaultCouponSampleSize        = 3
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
}

// EbayNotifier abstracts the Discord notification layer for eBay deals.
type EbayNotifier interface {
	SendEbayDeal(ctx context.Context, item EbayItem, subs []models.Subscription) (map[string]string, error)
}

// Processor handles the eBay price-drop monitoring pipeline.
type Processor struct {
	store                   EbayStore
	client                  *Client
	notifier                EbayNotifier
	mu                      sync.Mutex
	couponDiscoveryInterval time.Duration
	couponSampleSize        int
}

// NewProcessor creates a new eBay price-drop processor.
func NewProcessor(store EbayStore, client *Client, notifier EbayNotifier) *Processor {
	return &Processor{
		store:                   store,
		client:                  client,
		notifier:                notifier,
		couponDiscoveryInterval: defaultCouponDiscoveryInterval,
		couponSampleSize:        defaultCouponSampleSize,
	}
}

func (p *Processor) SetCouponDiscoveryConfig(interval time.Duration, sampleSize int) {
	if interval > 0 {
		p.couponDiscoveryInterval = interval
	}
	if sampleSize > 0 {
		p.couponSampleSize = sampleSize
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

	couponCache := p.refreshSellerCouponCaches(ctx, apiItems, logger)

	// 4. Load existing tracked items from storage
	tracked, err := p.store.GetTrackedEbayItems(ctx)
	if err != nil {
		stats.exitReason = "tracked_load_error"
		return fmt.Errorf("failed to load tracked eBay items: %w", err)
	}

	// 5. Process each fetched item — detect price drops or add new items.
	// Only write to Firestore when fields actually changed to avoid redundant writes.
	now := time.Now()
	currentIDs := make(map[string]bool, len(apiItems))
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
				ItemID:         itemID,
				Title:          apiItem.Title,
				Price:          newPrice,
				BasePrice:      basePrice,
				CouponDiscount: apiItem.CouponDiscount,
				CouponCode:     apiItem.CouponCode,
				CouponMessage:  apiItem.CouponMessage,
				CouponSource:   apiItem.CouponSource,
				OriginalPrice:  newPrice,
				Currency:       currencyOrDefault(apiItem.Price),
				Seller:         sellerUsername(apiItem.Seller),
				Condition:      apiItem.Condition,
				ItemURL:        apiItem.ItemWebURL,
				ImageURL:       imageURL(apiItem.Image),
				FirstSeenAt:    now,
				LastSeenAt:     now,
			})
			continue
		}

		if shouldFetchPageCoupon(existing, basePrice, apiItem.CouponDiscount) {
			if cachedCoupon := bestCachedCoupon(couponCache[sellerCouponKey(apiItem.Marketplace, sellerUsername(apiItem.Seller))], basePrice); cachedCoupon.DiscountAmount > apiItem.CouponDiscount {
				apiItem.CouponDiscount = cachedCoupon.DiscountAmount
				apiItem.CouponCode = cachedCoupon.Code
				apiItem.CouponMessage = cachedCoupon.Message
				apiItem.CouponSource = "seller-coupon-cache"
				newPrice = effectiveItemPrice(basePrice, apiItem.CouponDiscount)
				logger.Info("Applied cached eBay seller coupon to effective price",
					"itemID", itemID,
					"base_price", basePrice,
					"coupon_discount", apiItem.CouponDiscount,
					"coupon_source", apiItem.CouponSource,
					"effective_price", newPrice,
				)
			} else {
				pageCoupon, err := p.client.FetchPageCouponSnapshot(ctx, apiItem, basePrice)
				if err != nil {
					logger.Warn("Failed to fetch eBay page coupon",
						"itemID", itemID,
						"backend_order", p.client.couponBackends,
						"error", err,
					)
				} else if pageCoupon.DiscountAmount > apiItem.CouponDiscount {
					apiItem.CouponDiscount = pageCoupon.DiscountAmount
					apiItem.CouponCode = pageCoupon.Code
					apiItem.CouponMessage = pageCoupon.Message
					apiItem.CouponSource = pageCoupon.Source
					newPrice = effectiveItemPrice(basePrice, apiItem.CouponDiscount)
					logger.Info("Applied eBay page coupon to effective price",
						"itemID", itemID,
						"base_price", basePrice,
						"coupon_discount", apiItem.CouponDiscount,
						"coupon_source", apiItem.CouponSource,
						"effective_price", newPrice,
					)
				}
			}
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

		// Existing item — notify on the first qualifying drop from original price,
		// then only on materially deeper drops than the last alerted price.
		baselinePrice, dollarDrop, percentDrop, shouldNotify := shouldNotifyPriceDrop(existing, newPrice)

		if shouldNotify {
			stats.priceDrops++
			existing.DropCount = priorDropCount(existing) + 1
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
		if existing.Price != newPrice || existing.Title != apiItem.Title ||
			existing.Condition != apiItem.Condition || existing.ItemURL != apiItem.ItemWebURL ||
			existing.ImageURL != newImgURL || existing.Currency != newCurrency ||
			existing.Seller != newSeller || existing.BasePrice != newBasePrice ||
			existing.CouponDiscount != newCouponDiscount || existing.CouponCode != newCouponCode ||
			existing.CouponMessage != newCouponMessage || existing.CouponSource != newCouponSource ||
			backfilledOriginalPrice || backfilledDropCount || shouldNotify {
			stats.updated++
			existing.Price = newPrice
			existing.BasePrice = newBasePrice
			existing.CouponDiscount = newCouponDiscount
			existing.CouponCode = newCouponCode
			existing.CouponMessage = newCouponMessage
			existing.CouponSource = newCouponSource
			existing.LastSeenAt = now
			existing.Title = apiItem.Title
			existing.Currency = newCurrency
			existing.Seller = newSeller
			existing.ItemURL = apiItem.ItemWebURL
			existing.ImageURL = newImgURL
			existing.Condition = apiItem.Condition
			itemsToWrite = append(itemsToWrite, existing)
		}
	}

	// Bulk write all new and changed items
	if len(itemsToWrite) > 0 {
		logger.Info("Writing eBay item changes to Firestore", "count", len(itemsToWrite))
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
	switch sub.DealType {
	case "ebay_ca_price_drop":
		return ebayItemMarketplace(item) == "EBAY_CA"
	case "ebay_us_price_drop":
		return ebayItemMarketplace(item) == "EBAY_US"
	case "ebay_price_drop", "ebay_warm_hot", "ebay_hot", "warm_hot_all", "hot_all":
		return true
	default:
		return false
	}
}

func isEbayDealType(dealType string) bool {
	switch dealType {
	case "ebay_ca_price_drop", "ebay_us_price_drop", "ebay_price_drop", "ebay_warm_hot", "ebay_hot", "warm_hot_all", "hot_all":
		return true
	default:
		return false
	}
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

func (p *Processor) refreshSellerCouponCaches(ctx context.Context, items []BrowseAPIItem, logger *slog.Logger) map[string][]StoreCoupon {
	now := time.Now()
	grouped := groupItemsForCouponDiscovery(items)
	cache := make(map[string][]StoreCoupon, len(grouped))

	for key, group := range grouped {
		if ctx.Err() != nil {
			return cache
		}
		marketplace, seller := splitSellerCouponKey(key)
		existing, err := p.store.GetEbayStoreCoupons(ctx, marketplace, seller)
		if err != nil {
			logger.Warn("Failed to load eBay seller coupon cache", "seller", seller, "marketplace", marketplace, "error", err)
			continue
		}
		cache[key] = existing
		if couponCacheFresh(existing, now) {
			continue
		}

		refreshed := p.discoverSellerCoupon(ctx, marketplace, seller, group, existing, now, logger)
		if refreshed.Signature == "" {
			continue
		}
		if err := p.store.SaveEbayStoreCoupon(ctx, refreshed); err != nil {
			logger.Warn("Failed to save eBay seller coupon cache", "seller", seller, "marketplace", marketplace, "signature", refreshed.Signature, "error", err)
			continue
		}
		cache[key] = appendFreshCoupon(existing, refreshed)
	}
	return cache
}

func (p *Processor) discoverSellerCoupon(ctx context.Context, marketplace, seller string, items []BrowseAPIItem, existing []StoreCoupon, now time.Time, logger *slog.Logger) StoreCoupon {
	sampleSize := p.couponSampleSize
	if sampleSize <= 0 {
		sampleSize = defaultCouponSampleSize
	}
	if sampleSize > len(items) {
		sampleSize = len(items)
	}

	type sample struct {
		coupon PageCoupon
		item   BrowseAPIItem
		source string
	}
	var samples []sample
	for _, item := range items[:sampleSize] {
		if ctx.Err() != nil {
			break
		}
		basePrice := parsePrice(item.Price)
		if basePrice <= 0 {
			continue
		}
		coupon, source, err := p.client.FetchPageCoupon(ctx, item, basePrice)
		if err != nil {
			logger.Warn("Failed eBay seller coupon discovery sample",
				"seller", seller,
				"marketplace", marketplace,
				"itemID", ExtractItemID(item.ItemID),
				"error", err,
			)
			continue
		}
		if coupon.DiscountAmount <= 0 {
			continue
		}
		samples = append(samples, sample{coupon: coupon, item: item, source: source})
	}

	if len(samples) == 0 {
		negative := StoreCoupon{
			Marketplace:         marketplace,
			Seller:              seller,
			Signature:           "none",
			DiscountType:        "none",
			Scope:               "none",
			Confidence:          0.6,
			LastChecked:         now,
			LastSeen:            now,
			NextCheckAt:         now.Add(p.couponDiscoveryIntervalOrDefault()),
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

	counts := make(map[string]int)
	bestBySignature := make(map[string]sample)
	for _, s := range samples {
		sig := s.coupon.Signature
		counts[sig]++
		if bestBySignature[sig].coupon.DiscountAmount < s.coupon.DiscountAmount {
			bestBySignature[sig] = s
		}
	}

	var bestSig string
	for sig, count := range counts {
		if bestSig == "" || count > counts[bestSig] || bestBySignature[sig].coupon.DiscountAmount > bestBySignature[bestSig].coupon.DiscountAmount {
			bestSig = sig
		}
	}
	best := bestBySignature[bestSig]
	scope := best.coupon.Scope
	confidence := best.coupon.Confidence
	if counts[bestSig] >= 2 {
		scope = "store"
		confidence += 0.25
	}
	if confidence > 0.98 {
		confidence = 0.98
	}

	itemIDs := make([]string, 0, len(samples))
	itemURLs := make([]string, 0, len(samples))
	for _, s := range samples {
		if s.coupon.Signature != bestSig {
			continue
		}
		itemIDs = append(itemIDs, ExtractItemID(s.item.ItemID))
		itemURLs = append(itemURLs, s.item.ItemWebURL)
	}

	nextCheck := now.Add(p.couponDiscoveryIntervalOrDefault())
	if !best.coupon.ExpiresAt.IsZero() && best.coupon.ExpiresAt.After(now) {
		nextCheck = best.coupon.ExpiresAt.Add(15 * time.Minute)
	}
	coupon := StoreCoupon{
		Marketplace:     marketplace,
		Seller:          seller,
		Signature:       bestSig,
		DiscountType:    best.coupon.DiscountType,
		DiscountValue:   best.coupon.DiscountValue,
		MaxDiscount:     best.coupon.MaxDiscount,
		Code:            best.coupon.Code,
		RawText:         best.coupon.Message,
		Confidence:      confidence,
		Scope:           scope,
		SampledItemIDs:  itemIDs,
		SampledItemURLs: itemURLs,
		FirstSeen:       firstSeenOrNow(existing, bestSig, now),
		LastSeen:        now,
		LastChecked:     now,
		ExpiresAt:       best.coupon.ExpiresAt,
		NextCheckAt:     nextCheck,
		Active:          confidence >= 0.75 && scope == "store",
	}
	logger.Info("Refreshed eBay seller coupon cache",
		"seller", seller,
		"marketplace", marketplace,
		"signature", coupon.Signature,
		"scope", coupon.Scope,
		"confidence", coupon.Confidence,
		"active", coupon.Active,
		"sample_hits", counts[bestSig],
		"sample_size", sampleSize,
		"next_check", coupon.NextCheckAt,
	)
	return coupon
}

func (p *Processor) couponDiscoveryIntervalOrDefault() time.Duration {
	if p.couponDiscoveryInterval > 0 {
		return p.couponDiscoveryInterval
	}
	return defaultCouponDiscoveryInterval
}

func groupItemsForCouponDiscovery(items []BrowseAPIItem) map[string][]BrowseAPIItem {
	grouped := make(map[string][]BrowseAPIItem)
	for _, item := range items {
		seller := sellerUsername(item.Seller)
		if seller == "" || seller == "Unknown" {
			continue
		}
		marketplace := item.Marketplace
		if marketplace == "" {
			marketplace = "EBAY_CA"
		}
		grouped[sellerCouponKey(marketplace, seller)] = append(grouped[sellerCouponKey(marketplace, seller)], item)
	}
	return grouped
}

func sellerCouponKey(marketplace, seller string) string {
	return strings.ToUpper(strings.TrimSpace(marketplace)) + "|" + strings.ToLower(strings.TrimSpace(seller))
}

func splitSellerCouponKey(key string) (marketplace, seller string) {
	parts := strings.SplitN(key, "|", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", key
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
	for _, coupon := range coupons {
		if !coupon.Active || coupon.Confidence < 0.75 || coupon.Scope != "store" {
			continue
		}
		if !coupon.ExpiresAt.IsZero() && coupon.ExpiresAt.Before(now) {
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
		}
	}
	return best
}

func storeCouponDiscount(coupon StoreCoupon, basePrice float64) float64 {
	switch coupon.DiscountType {
	case "fixed":
		if coupon.DiscountValue >= basePrice {
			return 0
		}
		return roundCents(coupon.DiscountValue)
	case "percent":
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
