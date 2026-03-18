package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
)

const (
	ebaySellersCollection = "ebay_sellers"
	ebayItemsCollection   = "ebay_items"
)

// --- eBay Sellers ---

// GetActiveEbaySellers returns all active sellers from Firestore.
func (c *Client) GetActiveEbaySellers(ctx context.Context) ([]ebay.EbaySeller, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	iter := c.client.Collection(ebaySellersCollection).
		Where("isActive", "==", true).
		Documents(ctx)
	defer iter.Stop()

	var sellers []ebay.EbaySeller
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate ebay sellers: %w", err)
		}

		var seller ebay.EbaySeller
		if err := doc.DataTo(&seller); err != nil {
			slog.Warn("Failed to unmarshal ebay seller", "id", doc.Ref.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedEbaySellers populates the ebay_sellers collection if it's empty.
// Returns true if seeding was performed.
func (c *Client) SeedEbaySellers(ctx context.Context) (bool, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	// Check if collection has any documents
	iter := c.client.Collection(ebaySellersCollection).Limit(1).Documents(ctx)
	defer iter.Stop()

	_, err := iter.Next()
	if err != iterator.Done {
		if err != nil {
			return false, fmt.Errorf("failed to check ebay sellers collection: %w", err)
		}
		// Collection already has documents, skip seeding
		return false, nil
	}

	// Collection is empty, seed with defaults
	slog.Info("Seeding ebay_sellers collection with default sellers")
	defaults := ebay.DefaultSellers()

	bw := c.client.BulkWriter(ctx)
	for _, seller := range defaults {
		doc := c.client.Collection(ebaySellersCollection).Doc(seller.Username)
		if _, err := bw.Create(doc, seller); err != nil {
			slog.Error("Failed to queue ebay seller seed", "username", seller.Username, "error", err)
		}
	}
	bw.Flush()
	bw.End()

	slog.Info("Seeded ebay_sellers collection", "count", len(defaults))
	return true, nil
}

// --- eBay Items ---

// GetEbayItemsByIDs retrieves multiple eBay items by their item IDs.
func (c *Client) GetEbayItemsByIDs(ctx context.Context, itemIDs []string) (map[string]*ebay.EbayItem, error) {
	if len(itemIDs) == 0 {
		return make(map[string]*ebay.EbayItem), nil
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	refs := make([]*firestore.DocumentRef, len(itemIDs))
	for i, id := range itemIDs {
		refs[i] = c.client.Collection(ebayItemsCollection).Doc(id)
	}

	docs, err := c.client.GetAll(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get ebay items: %w", err)
	}

	result := make(map[string]*ebay.EbayItem, len(docs))
	for _, doc := range docs {
		if !doc.Exists() {
			continue
		}
		var item ebay.EbayItem
		if err := doc.DataTo(&item); err != nil {
			slog.Warn("Failed to unmarshal ebay item", "id", doc.Ref.ID, "error", err)
			continue
		}
		result[doc.Ref.ID] = &item
	}
	return result, nil
}

// BatchWriteEbayItems creates new eBay items in Firestore.
func (c *Client) BatchWriteEbayItems(ctx context.Context, items []ebay.EbayItem) error {
	if len(items) == 0 {
		return nil
	}

	bw := c.client.BulkWriter(ctx)
	col := c.client.Collection(ebayItemsCollection)

	for _, item := range items {
		doc := col.Doc(item.ItemID)
		if _, err := bw.Set(doc, item); err != nil {
			slog.Error("BatchWriteEbayItems: Failed to queue write", "id", item.ItemID, "error", err)
		}
	}

	bw.Flush()
	bw.End()
	return nil
}

// TrimOldEbayItems deletes the oldest eBay items (by lastUpdated) to keep the collection under maxItems.
func (c *Client) TrimOldEbayItems(ctx context.Context, maxItems int) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}

	// Get count
	countSnapshot, err := c.client.Collection(ebayItemsCollection).NewAggregationQuery().WithCount("all").Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to get ebay item count: %w", err)
	}

	countValue, ok := countSnapshot["all"]
	if !ok {
		return nil
	}

	var count int64
	switch val := countValue.(type) {
	case int64:
		count = val
	default:
		return nil
	}

	if int(count) <= maxItems {
		return nil
	}

	numToDelete := int(count) - maxItems
	slog.Info("Trimming old eBay items", "current", count, "max", maxItems, "deleting", numToDelete)

	iter := c.client.Collection(ebayItemsCollection).
		OrderBy("lastUpdated", firestore.Asc).
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	bw := c.client.BulkWriter(ctx)
	deleted := 0

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to iterate ebay items for trimming: %w", err)
		}

		if _, err := bw.Delete(doc.Ref); err != nil {
			slog.Error("Failed to queue ebay item delete", "id", doc.Ref.ID, "error", err)
			continue
		}
		deleted++
	}

	if deleted > 0 {
		bw.Flush()
	}
	bw.End()

	slog.Info("Trimmed old eBay items", "deleted", deleted)
	return nil
}

// --- eBay Poll State ---

// GetEbayPollState retrieves the eBay polling state from bot_config.
func (c *Client) GetEbayPollState(ctx context.Context) (*ebay.EbayPollState, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	doc, err := c.client.Collection("bot_config").Doc("ebay_poll_state").Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ebay poll state: %w", err)
	}

	var state ebay.EbayPollState
	if err := doc.DataTo(&state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ebay poll state: %w", err)
	}
	return &state, nil
}

// UpdateEbayPollState updates the eBay polling state in bot_config.
func (c *Client) UpdateEbayPollState(ctx context.Context, state ebay.EbayPollState) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	state.LastUpdated = time.Now()
	_, err := c.client.Collection("bot_config").Doc("ebay_poll_state").Set(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to update ebay poll state: %w", err)
	}
	return nil
}
