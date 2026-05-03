package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const memexpressCollection = "memexpress_deals"

// MemExpressProductExists checks whether a clearance product already exists.
func (c *Client) MemExpressProductExists(ctx context.Context, sku, storeCode string) (bool, error) {
	if sku == "" || storeCode == "" {
		return false, nil
	}
	_, ok, err := c.GetRawDocument(ctx, memexpressCollection, memoryexpress.DocID(sku, storeCode))
	return ok, err
}

// GetExistingMemExpressProductIDs batch-loads known Memory Express product IDs.
func (c *Client) GetExistingMemExpressProductIDs(ctx context.Context, products []memoryexpress.Product) (map[string]struct{}, error) {
	if len(products) == 0 {
		return make(map[string]struct{}), nil
	}
	existing := make(map[string]struct{})
	seen := make(map[string]struct{}, len(products))
	for _, product := range products {
		if product.SKU == "" || product.StoreCode == "" {
			continue
		}
		docID := memoryexpress.DocID(product.SKU, product.StoreCode)
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		if _, ok, err := c.GetRawDocument(ctx, memexpressCollection, docID); err != nil {
			return nil, err
		} else if ok {
			existing[docID] = struct{}{}
		}
	}
	return existing, nil
}

// SaveMemExpressProduct saves a clearance product.
func (c *Client) SaveMemExpressProduct(ctx context.Context, product memoryexpress.AnalyzedProduct) error {
	return c.SetDocument(ctx, memexpressCollection, memoryexpress.DocID(product.SKU, product.StoreCode), product)
}

func (c *Client) RefreshMemExpressProductLastSeen(ctx context.Context, product memoryexpress.Product) error {
	docID := memoryexpress.DocID(product.SKU, product.StoreCode)
	now := time.Now()
	var existing memoryexpress.AnalyzedProduct
	ok, err := c.GetDocument(ctx, memexpressCollection, docID, &existing)
	if err != nil || !ok {
		return err
	}
	existing.Product = product
	existing.LastSeen = now
	return c.SetDocument(ctx, memexpressCollection, docID, existing)
}

// PruneMemExpressProducts deletes clearance products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	rows, err := c.ListDocuments(ctx, memexpressCollection)
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	for _, row := range rows {
		if !documentTime(row.Data, "lastSeen").IsZero() && documentTime(row.Data, "lastSeen").Before(cutoff) {
			if err := c.DeleteDocument(ctx, memexpressCollection, row.ID); err != nil {
				slog.Warn("Failed to delete old memexpress product", "processor", "memoryexpress", "id", row.ID, "error", err)
			}
		}
	}
	rows, err = c.ListDocuments(ctx, memexpressCollection)
	if err != nil {
		return err
	}
	if len(rows) > maxRecords {
		sortDocumentsByTime(rows, "lastSeen", true)
		for _, row := range rows[:len(rows)-maxRecords] {
			if err := c.DeleteDocument(ctx, memexpressCollection, row.ID); err != nil {
				slog.Warn("Failed to delete excess memexpress record", "processor", "memoryexpress", "id", row.ID, "error", err)
			}
		}
	}
	return nil
}

// memExpressSubscriptionDocID generates a unique document ID for a Memory Express subscription.
func memExpressSubscriptionDocID(guildID, channelID, storeCode string) string {
	return fmt.Sprintf("%s_%s_memoryexpress_%s", guildID, channelID, storeCode)
}

// SaveMemExpressSubscription creates or updates a Memory Express subscription.
func (c *Client) SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error {
	return c.SetDocument(ctx, subscriptionsCollection, memExpressSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.StoreCode), sub)
}

// RemoveMemExpressSubscription removes a Memory Express subscription by guild, channel, and store.
func (c *Client) RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error {
	return c.DeleteDocument(ctx, subscriptionsCollection, memExpressSubscriptionDocID(guildID, channelID, storeCode))
}

// GetMemExpressSubscriptions retrieves all active Memory Express subscriptions.
func (c *Client) GetMemExpressSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{"subscriptionType": "memoryexpress"}, nil)
}

// GetMemExpressSubscriptionsByGuild retrieves all Memory Express subscriptions for a guild.
func (c *Client) GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{
		"guildID":          guildID,
		"subscriptionType": "memoryexpress",
	}, nil)
}
