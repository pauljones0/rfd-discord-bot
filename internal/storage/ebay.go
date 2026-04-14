package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

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
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

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
			slog.Warn("Failed to unmarshal ebay seller", "processor", "ebay", "id", doc.Ref.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedEbaySellers populates the ebay_sellers collection if it's empty.
// Returns true if seeding was performed.
func (c *Client) SeedEbaySellers(ctx context.Context) (bool, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

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
	var errs []error
	for _, seller := range defaults {
		doc := c.client.Collection(ebaySellersCollection).Doc(seller.Username)
		if _, err := bw.Create(doc, seller); err != nil {
			slog.Error("Failed to queue ebay seller seed", "processor", "ebay", "username", seller.Username, "error", err)
			errs = append(errs, fmt.Errorf("seed %s: %w", seller.Username, err))
		}
	}
	bw.Flush()
	bw.End()

	if err := errors.Join(errs...); err != nil {
		return true, fmt.Errorf("seeding partially failed: %w", err)
	}
	slog.Info("Seeded ebay_sellers collection", "count", len(defaults))
	return true, nil
}

// --- eBay Poll State ---

// GetEbayPollState retrieves the eBay polling state from bot_config.
func (c *Client) GetEbayPollState(ctx context.Context) (*ebay.EbayPollState, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

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
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	state.LastUpdated = time.Now()
	_, err := c.client.Collection("bot_config").Doc("ebay_poll_state").Set(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to update ebay poll state: %w", err)
	}
	return nil
}

// --- eBay Tracked Items ---

// GetTrackedEbayItems returns all tracked items from the ebay_items collection.
func (c *Client) GetTrackedEbayItems(ctx context.Context) (map[string]ebay.TrackedItem, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(ebayItemsCollection).Documents(ctx)
	defer iter.Stop()

	items := make(map[string]ebay.TrackedItem)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate ebay items: %w", err)
		}

		var item ebay.TrackedItem
		if err := doc.DataTo(&item); err != nil {
			slog.Warn("Failed to unmarshal ebay item", "processor", "ebay", "id", doc.Ref.ID, "error", err)
			continue
		}
		items[doc.Ref.ID] = item
	}
	return items, nil
}

// BulkUpsertTrackedEbayItems creates or updates tracked eBay items in a single batch.
func (c *Client) BulkUpsertTrackedEbayItems(ctx context.Context, items []ebay.TrackedItem) error {
	if len(items) == 0 {
		return nil
	}

	ctx, cancel := ensureDeadline(ctx, 60*time.Second)
	defer cancel()

	bw := c.client.BulkWriter(ctx)
	for _, item := range items {
		doc := c.client.Collection(ebayItemsCollection).Doc(item.ItemID)
		if _, err := bw.Set(doc, item); err != nil {
			slog.Warn("Failed to queue ebay item upsert", "processor", "ebay", "itemID", item.ItemID, "error", err)
		}
	}
	bw.Flush()
	bw.End()
	return nil
}

// DeleteTrackedEbayItems deletes tracked items by their IDs (items no longer listed).
func (c *Client) DeleteTrackedEbayItems(ctx context.Context, itemIDs []string) error {
	if len(itemIDs) == 0 {
		return nil
	}

	ctx, cancel := ensureDeadline(ctx, 60*time.Second)
	defer cancel()

	bw := c.client.BulkWriter(ctx)
	for _, id := range itemIDs {
		doc := c.client.Collection(ebayItemsCollection).Doc(id)
		if _, err := bw.Delete(doc); err != nil {
			slog.Warn("Failed to queue ebay item deletion", "processor", "ebay", "itemID", id, "error", err)
		}
	}
	bw.Flush()
	bw.End()
	return nil
}
