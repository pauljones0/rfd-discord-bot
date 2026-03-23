package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const memexpressCollection = "memexpress_deals"

// memExpressDocID generates a unique document ID for a Memory Express clearance product.
func memExpressDocID(sku, storeCode string) string {
	return fmt.Sprintf("%s_%s", sku, storeCode)
}

// MemExpressProductExists checks whether a clearance product already exists in Firestore.
func (c *Client) MemExpressProductExists(ctx context.Context, sku, storeCode string) (bool, error) {
	if sku == "" || storeCode == "" {
		return false, nil
	}
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressDocID(sku, storeCode)
	_, err := c.client.Collection(memexpressCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to check memexpress product existence: %v", err)
	}
	return true, nil
}

// SaveMemExpressProduct saves a clearance product to Firestore.
func (c *Client) SaveMemExpressProduct(ctx context.Context, product memoryexpress.AnalyzedProduct) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressDocID(product.SKU, product.StoreCode)
	_, err := c.client.Collection(memexpressCollection).Doc(docID).Set(ctx, product)
	if err != nil {
		return fmt.Errorf("failed to save memexpress product %s: %v", docID, err)
	}
	return nil
}

// PruneMemExpressProducts deletes clearance products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	ctx, cancel := ensureDeadline(ctx, 2*time.Minute)
	defer cancel()

	// Delete by age
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	iter := c.client.Collection(memexpressCollection).Where("lastSeen", "<", cutoff).Documents(ctx)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if _, err := doc.Ref.Delete(ctx); err != nil {
			slog.Warn("Failed to delete old memexpress product", "processor", "memoryexpress", "id", doc.Ref.ID, "error", err)
		}
	}

	// Delete oldest if exceeding maxRecords
	allIter := c.client.Collection(memexpressCollection).Documents(ctx)
	totalCount := 0
	for {
		_, err := allIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to count memexpress records: %v", err)
		}
		totalCount++
	}

	if totalCount > maxRecords {
		excess := totalCount - maxRecords
		deleteIter := c.client.Collection(memexpressCollection).OrderBy("lastSeen", firestore.Asc).Limit(excess).Documents(ctx)
		for {
			doc, err := deleteIter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to iterate excess memexpress records: %v", err)
			}
			if _, err := doc.Ref.Delete(ctx); err != nil {
				slog.Warn("Failed to delete excess memexpress record", "processor", "memoryexpress", "id", doc.Ref.ID, "error", err)
			}
		}
	}

	return nil
}

// memExpressSubscriptionDocID generates a unique document ID for a Memory Express subscription.
func memExpressSubscriptionDocID(guildID, channelID, storeCode string) string {
	return fmt.Sprintf("%s_%s_memoryexpress_%s", guildID, channelID, storeCode)
}

// SaveMemExpressSubscription creates or updates a Memory Express subscription in Firestore.
func (c *Client) SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.StoreCode)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Set(ctx, sub)
	if err != nil {
		return fmt.Errorf("failed to save memexpress subscription %s: %w", docID, err)
	}
	return nil
}

// RemoveMemExpressSubscription removes a Memory Express subscription by guild, channel, and store.
func (c *Client) RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressSubscriptionDocID(guildID, channelID, storeCode)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to remove memexpress subscription %s: %w", docID, err)
	}
	return nil
}

// GetMemExpressSubscriptionsByGuild retrieves all Memory Express subscriptions for a guild.
func (c *Client) GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("guildID", "==", guildID).
		Where("subscriptionType", "==", "memoryexpress").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate guild memexpress subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild memexpress subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}
