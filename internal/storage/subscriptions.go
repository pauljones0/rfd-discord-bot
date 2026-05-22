package storage

import (
	"context"
	"fmt"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const subscriptionsCollection = "subscriptions"

// SaveSubscription saves a new guild/channel subscription.
func (c *Client) SaveSubscription(ctx context.Context, sub models.Subscription) error {
	subscriptionType := sub.SubscriptionType
	if subscriptionType == "" {
		subscriptionType = "rfd"
	}
	dealType := sub.DealType
	if dealType == "" {
		dealType = "all"
	}
	docID := fmt.Sprintf("%s_%s_%s_%s", sub.GuildID, sub.ChannelID, subscriptionType, dealType)
	return c.SetDocument(ctx, subscriptionsCollection, docID, sub)
}

// RemoveSubscription removes a specific channel's subscription.
func (c *Client) RemoveSubscription(ctx context.Context, guildID, channelID, dealType string) error {
	fields := map[string]any{"guildID": guildID, "channelID": channelID}
	if dealType != "" {
		fields["dealType"] = dealType
	}
	deleted, err := c.DeleteDocumentsWhere(ctx, subscriptionsCollection, fields)
	if err != nil {
		return err
	}
	if deleted == 0 && dealType == "" {
		return c.DeleteDocument(ctx, subscriptionsCollection, fmt.Sprintf("%s_%s", guildID, channelID))
	}
	return nil
}

// GetSubscriptionsByGuild retrieves all active subscriptions for a specific guild.
func (c *Client) GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{"guildID": guildID}, nil)
}

// GetAllSubscriptions retrieves all registered active subscriptions.
func (c *Client) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return c.subscriptionsWhere(ctx, func(row Document) bool { return true })
}

// GetSubscription retrieves a specific subscription by its guild and channel.
func (c *Client) GetSubscription(ctx context.Context, guildID, channelID string) (*models.Subscription, error) {
	subs, err := c.subscriptionsMatching(ctx, map[string]any{"guildID": guildID, "channelID": channelID}, nil)
	if err != nil || len(subs) == 0 {
		return nil, err
	}
	return &subs[0], nil
}

func (c *Client) subscriptionsWhere(ctx context.Context, keep func(Document) bool) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, nil, keep)
}

func (c *Client) subscriptionsMatching(ctx context.Context, fields map[string]any, keep func(Document) bool) ([]models.Subscription, error) {
	rows, err := c.ListDocumentsWhere(ctx, subscriptionsCollection, fields)
	if err != nil {
		return nil, err
	}
	var subs []models.Subscription
	for _, row := range rows {
		if keep != nil && !keep(row) {
			continue
		}
		var sub models.Subscription
		if err := decodeDocument(row.Data, &sub); err != nil {
			return nil, fmt.Errorf("failed to decode subscription %s: %w", row.ID, err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}
