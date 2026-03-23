package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const bestbuyCollection = "bestbuy_deals"

// bestBuyDocID generates a unique document ID for a Best Buy product.
func bestBuyDocID(sku, source string) string {
	return fmt.Sprintf("%s_%s", sku, source)
}

// BestBuyProductExists checks whether a product already exists in Firestore.
func (c *Client) BestBuyProductExists(ctx context.Context, sku, source string) (bool, error) {
	if sku == "" || source == "" {
		return false, nil
	}
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := bestBuyDocID(sku, source)
	_, err := c.client.Collection(bestbuyCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to check bestbuy product existence: %v", err)
	}
	return true, nil
}

// SaveBestBuyProduct saves a product to Firestore.
func (c *Client) SaveBestBuyProduct(ctx context.Context, product bestbuy.AnalyzedProduct) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := bestBuyDocID(product.SKU, product.Source)
	_, err := c.client.Collection(bestbuyCollection).Doc(docID).Set(ctx, product)
	if err != nil {
		return fmt.Errorf("failed to save bestbuy product %s: %v", docID, err)
	}
	return nil
}

// PruneBestBuyProducts deletes products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	ctx, cancel := ensureDeadline(ctx, 2*time.Minute)
	defer cancel()

	// Delete by age
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	iter := c.client.Collection(bestbuyCollection).Where("lastSeen", "<", cutoff).Documents(ctx)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if _, err := doc.Ref.Delete(ctx); err != nil {
			slog.Warn("Failed to delete old bestbuy product", "processor", "bestbuy", "id", doc.Ref.ID, "error", err)
		}
	}

	// Delete oldest if exceeding maxRecords
	allIter := c.client.Collection(bestbuyCollection).Documents(ctx)
	totalCount := 0
	for {
		_, err := allIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to count bestbuy records: %v", err)
		}
		totalCount++
	}

	if totalCount > maxRecords {
		excess := totalCount - maxRecords
		deleteIter := c.client.Collection(bestbuyCollection).OrderBy("lastSeen", firestore.Asc).Limit(excess).Documents(ctx)
		for {
			doc, err := deleteIter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to iterate excess bestbuy records: %v", err)
			}
			if _, err := doc.Ref.Delete(ctx); err != nil {
				slog.Warn("Failed to delete excess bestbuy record", "processor", "bestbuy", "id", doc.Ref.ID, "error", err)
			}
		}
	}

	return nil
}

// bestBuySubscriptionDocID generates a unique document ID for a Best Buy subscription.
func bestBuySubscriptionDocID(guildID, channelID string) string {
	return fmt.Sprintf("%s_%s_bestbuy", guildID, channelID)
}

// SaveBestBuySubscription creates or updates a Best Buy subscription in Firestore.
func (c *Client) SaveBestBuySubscription(ctx context.Context, sub models.Subscription) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := bestBuySubscriptionDocID(sub.GuildID, sub.ChannelID)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Set(ctx, sub)
	if err != nil {
		return fmt.Errorf("failed to save bestbuy subscription %s: %w", docID, err)
	}
	return nil
}

// RemoveBestBuySubscription removes a Best Buy subscription by guild and channel.
func (c *Client) RemoveBestBuySubscription(ctx context.Context, guildID, channelID string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := bestBuySubscriptionDocID(guildID, channelID)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to remove bestbuy subscription %s: %w", docID, err)
	}
	return nil
}

// GetBestBuySubscriptionsByGuild retrieves all Best Buy subscriptions for a guild.
func (c *Client) GetBestBuySubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("guildID", "==", guildID).
		Where("subscriptionType", "==", "bestbuy").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate guild bestbuy subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild bestbuy subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}
