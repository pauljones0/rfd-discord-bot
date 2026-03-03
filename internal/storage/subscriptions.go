package storage

import (
	"context"
	"fmt"

	"google.golang.org/api/iterator"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const subscriptionsCollection = "subscriptions"

// SaveSubscription saves a new guild/channel subscription to Firestore.
func (c *Client) SaveSubscription(ctx context.Context, sub models.Subscription) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	docID := fmt.Sprintf("%s_%s", guildID, channelID)
	docRef := c.client.Collection(subscriptionsCollection).Doc(docID)
	_, err := docRef.Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to remove subscription %s: %w", docID, err)
	}

	return nil
}

// GetSubscriptionsByGuild retrieves all active subscriptions for a specific guild.
func (c *Client) GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	var subs []models.Subscription
	iter := c.client.Collection(subscriptionsCollection).Where("GuildID", "==", guildID).Documents(ctx)
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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
