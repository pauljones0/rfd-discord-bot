package storage

import (
	"context"
	"fmt"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const subscriptionsCollection = "subscriptions"

// SaveSubscription saves a new guild/channel subscription to Firestore.
func (c *Client) SaveSubscription(ctx context.Context, sub models.Subscription) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := fmt.Sprintf("%s_%s", sub.GuildID, sub.ChannelID)
	docRef := c.client.Collection(subscriptionsCollection).Doc(docID)
	_, err := docRef.Set(ctx, sub)
	if err != nil {
		return fmt.Errorf("failed to save subscription %s: %w", docID, err)
	}

	return nil
}

// RemoveSubscription removes a specific channel's subscription from Firestore.
func (c *Client) RemoveSubscription(ctx context.Context, guildID, channelID string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("guildID", "==", guildID).
		Where("channelID", "==", channelID).
		Documents(ctx)
	defer iter.Stop()

	// Keep track of any errors encountered during deletion, but try to delete all
	var lastErr error
	count := 0
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to iterate subscriptions for deletion: %w", err)
		}

		_, err = doc.Ref.Delete(ctx)
		if err != nil {
			lastErr = fmt.Errorf("failed to delete subscription doc %s: %w", doc.Ref.ID, err)
		} else {
			count++
		}
	}

	if lastErr != nil {
		return lastErr
	}

	if count == 0 {
		// If we didn't find any documents by querying, fallback to the expected ID just in case
		docID := fmt.Sprintf("%s_%s", guildID, channelID)
		docRef := c.client.Collection(subscriptionsCollection).Doc(docID)
		_, err := docRef.Delete(ctx)
		if err != nil {
			return fmt.Errorf("failed to remove subscription %s: %w", docID, err)
		}
	}

	return nil
}

// GetSubscriptionsByGuild retrieves all active subscriptions for a specific guild.
func (c *Client) GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	var subs []models.Subscription
	iter := c.client.Collection(subscriptionsCollection).Where("guildID", "==", guildID).Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate guild subscriptions: %w", err)
		}

		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal subscription config: %w", err)
		}
		subs = append(subs, sub)
	}

	return subs, nil
}

// GetAllSubscriptions retrieves all registered active subscriptions.
func (c *Client) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	var subs []models.Subscription
	iter := c.client.Collection(subscriptionsCollection).Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate subscriptions: %w", err)
		}

		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal subscription config: %w", err)
		}
		subs = append(subs, sub)
	}

	return subs, nil
}

// GetSubscription retrieves a specific subscription by its guild and channel.
func (c *Client) GetSubscription(ctx context.Context, guildID, channelID string) (*models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := fmt.Sprintf("%s_%s", guildID, channelID)
	doc, err := c.client.Collection(subscriptionsCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get subscription %s: %w", docID, err)
	}

	var sub models.Subscription
	if err := doc.DataTo(&sub); err != nil {
		return nil, fmt.Errorf("failed to unmarshal subscription: %w", err)
	}
	return &sub, nil
}
