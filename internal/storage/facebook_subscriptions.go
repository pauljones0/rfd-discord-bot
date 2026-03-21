package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/api/iterator"
)

// facebookSubscriptionDocID generates a unique document ID for a Facebook subscription.
// Format: {guildID}_{channelID}_facebook_{city}
func facebookSubscriptionDocID(guildID, channelID, city string) string {
	return fmt.Sprintf("%s_%s_facebook_%s", guildID, channelID, strings.ToLower(strings.ReplaceAll(city, " ", "-")))
}

// SaveFacebookSubscription creates or updates a Facebook subscription in Firestore.
func (c *Client) SaveFacebookSubscription(ctx context.Context, sub models.Subscription) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := facebookSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.City)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Set(ctx, sub)
	if err != nil {
		return fmt.Errorf("failed to save facebook subscription %s: %w", docID, err)
	}
	return nil
}

// RemoveFacebookSubscription removes a Facebook subscription by guild, channel, and city.
func (c *Client) RemoveFacebookSubscription(ctx context.Context, guildID, channelID, city string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := facebookSubscriptionDocID(guildID, channelID, city)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to remove facebook subscription %s: %w", docID, err)
	}
	return nil
}

// GetFacebookSubscriptions retrieves all active Facebook subscriptions.
func (c *Client) GetFacebookSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("subscriptionType", "==", "facebook").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate facebook subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal facebook subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

// GetFacebookSubscriptionsByGuild retrieves all Facebook subscriptions for a guild.
func (c *Client) GetFacebookSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("guildID", "==", guildID).
		Where("subscriptionType", "==", "facebook").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate guild facebook subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild facebook subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}
