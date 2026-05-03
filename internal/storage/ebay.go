package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
)

const (
	ebaySellersCollection            = "ebay_sellers"
	ebayItemsCollection              = "ebay_items"
	ebayStoreCouponsCollection       = "ebay_store_coupons"
	ebayCouponObservationsCollection = "ebay_coupon_observations"
)

// --- eBay Sellers ---

// GetActiveEbaySellers returns all active sellers.
func (c *Client) GetActiveEbaySellers(ctx context.Context) ([]ebay.EbaySeller, error) {
	rows, err := c.ListDocuments(ctx, ebaySellersCollection)
	if err != nil {
		return nil, err
	}
	var sellers []ebay.EbaySeller
	for _, row := range rows {
		if !documentBool(row.Data, "isActive") {
			continue
		}
		var seller ebay.EbaySeller
		if err := decodeDocument(row.Data, &seller); err != nil {
			slog.Warn("Failed to decode ebay seller", "processor", "ebay", "id", row.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedEbaySellers populates the ebay_sellers collection if it's empty.
// Returns true if seeding was performed.
func (c *Client) SeedEbaySellers(ctx context.Context) (bool, error) {
	rows, err := c.ListDocuments(ctx, ebaySellersCollection)
	if err != nil {
		return false, err
	}
	if len(rows) > 0 {
		return false, nil
	}
	slog.Info("Seeding ebay_sellers collection with default sellers")
	var errs []error
	defaults := ebay.DefaultSellers()
	for _, seller := range defaults {
		if err := c.CreateDocument(ctx, ebaySellersCollection, seller.Username, seller); err != nil {
			errs = append(errs, fmt.Errorf("seed %s: %w", seller.Username, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return true, fmt.Errorf("seeding partially failed: %w", err)
	}
	slog.Info("Seeded ebay_sellers collection", "count", len(defaults))
	return true, nil
}

// --- eBay Poll State ---

// GetEbayPollState retrieves the eBay polling state from bot_config.
func (c *Client) GetEbayPollState(ctx context.Context) (*ebay.EbayPollState, error) {
	var state ebay.EbayPollState
	ok, err := c.GetDocument(ctx, "bot_config", "ebay_poll_state", &state)
	if err != nil || !ok {
		return nil, err
	}
	return &state, nil
}

// UpdateEbayPollState updates the eBay polling state in bot_config.
func (c *Client) UpdateEbayPollState(ctx context.Context, state ebay.EbayPollState) error {
	state.LastUpdated = time.Now()
	return c.SetDocument(ctx, "bot_config", "ebay_poll_state", state)
}

// --- eBay Tracked Items ---

// GetTrackedEbayItems returns all tracked items from the ebay_items collection.
func (c *Client) GetTrackedEbayItems(ctx context.Context) (map[string]ebay.TrackedItem, error) {
	rows, err := c.ListDocuments(ctx, ebayItemsCollection)
	if err != nil {
		return nil, err
	}
	items := make(map[string]ebay.TrackedItem, len(rows))
	for _, row := range rows {
		var item ebay.TrackedItem
		if err := decodeDocument(row.Data, &item); err != nil {
			slog.Warn("Failed to decode ebay item", "processor", "ebay", "id", row.ID, "error", err)
			continue
		}
		items[row.ID] = item
	}
	return items, nil
}

// BulkUpsertTrackedEbayItems creates or updates tracked eBay items in a single batch.
func (c *Client) BulkUpsertTrackedEbayItems(ctx context.Context, items []ebay.TrackedItem) error {
	if len(items) == 0 {
		return nil
	}
	for _, item := range items {
		if err := c.SetDocument(ctx, ebayItemsCollection, item.ItemID, item); err != nil {
			slog.Warn("Failed to upsert ebay item", "processor", "ebay", "itemID", item.ItemID, "error", err)
		}
	}
	return nil
}

// DeleteTrackedEbayItems deletes tracked items by their IDs (items no longer listed).
func (c *Client) DeleteTrackedEbayItems(ctx context.Context, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}
	for _, id := range itemIDs {
		if err := c.DeleteDocument(ctx, ebayItemsCollection, id); err != nil {
			slog.Warn("Failed to delete ebay item", "processor", "ebay", "itemID", id, "error", err)
		}
	}
	return nil
}

func ebayStoreCouponDocID(marketplace, seller, signature string) string {
	marketplace = strings.ToUpper(strings.TrimSpace(marketplace))
	seller = strings.ToLower(strings.TrimSpace(seller))
	signature = strings.ToLower(strings.TrimSpace(signature))
	if signature == "" {
		signature = "none"
	}
	replacer := strings.NewReplacer("/", "_", " ", "_", ":", "_", "|", "_")
	return replacer.Replace(marketplace + "_" + seller + "_" + signature)
}

func (c *Client) GetEbayStoreCoupons(ctx context.Context, marketplace, seller string) ([]ebay.StoreCoupon, error) {
	rows, err := c.ListDocuments(ctx, ebayStoreCouponsCollection)
	if err != nil {
		return nil, err
	}
	var coupons []ebay.StoreCoupon
	for _, row := range rows {
		if !sameMarketplaceSeller(row.Data, marketplace, seller) {
			continue
		}
		var coupon ebay.StoreCoupon
		if err := decodeDocument(row.Data, &coupon); err != nil {
			slog.Warn("Failed to decode ebay store coupon", "processor", "ebay", "id", row.ID, "error", err)
			continue
		}
		coupons = append(coupons, coupon)
	}
	return coupons, nil
}

func (c *Client) SaveEbayStoreCoupon(ctx context.Context, coupon ebay.StoreCoupon) error {
	docID := ebayStoreCouponDocID(coupon.Marketplace, coupon.Seller, coupon.Signature)
	return c.SetDocument(ctx, ebayStoreCouponsCollection, docID, coupon)
}

func ebayCouponObservationDocID(obs ebay.CouponObservation) string {
	marketplace := strings.ToUpper(strings.TrimSpace(obs.Marketplace))
	seller := strings.ToLower(strings.TrimSpace(obs.Seller))
	signature := strings.ToLower(strings.TrimSpace(obs.Signature))
	itemID := strings.ToLower(strings.TrimSpace(obs.ItemID))
	if signature == "" {
		signature = "none"
	}
	if itemID == "" {
		itemID = "unknown"
	}
	replacer := strings.NewReplacer("/", "_", " ", "_", ":", "_", "|", "_", "\\", "_")
	return replacer.Replace(marketplace + "_" + seller + "_" + signature + "_" + itemID)
}

func (c *Client) GetEbayCouponObservations(ctx context.Context, marketplace, seller, signature string) ([]ebay.CouponObservation, error) {
	rows, err := c.ListDocuments(ctx, ebayCouponObservationsCollection)
	if err != nil {
		return nil, err
	}
	var observations []ebay.CouponObservation
	for _, row := range rows {
		if !sameMarketplaceSeller(row.Data, marketplace, seller) {
			continue
		}
		if !strings.EqualFold(documentString(row.Data, "signature"), signature) {
			continue
		}
		var obs ebay.CouponObservation
		if err := decodeDocument(row.Data, &obs); err != nil {
			slog.Warn("Failed to decode ebay coupon observation", "processor", "ebay", "id", row.ID, "error", err)
			continue
		}
		observations = append(observations, obs)
	}
	return observations, nil
}

func (c *Client) SaveEbayCouponObservation(ctx context.Context, obs ebay.CouponObservation) error {
	docID := ebayCouponObservationDocID(obs)
	return c.SetDocument(ctx, ebayCouponObservationsCollection, docID, obs)
}

func sameMarketplaceSeller(data map[string]any, marketplace, seller string) bool {
	return strings.EqualFold(documentString(data, "marketplace"), marketplace) &&
		strings.EqualFold(documentString(data, "seller"), seller)
}
