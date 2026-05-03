package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// facebookSubscriptionDocID generates a unique document ID for a Facebook subscription.
// Format: {guildID}_{channelID}_facebook_{city}
func facebookSubscriptionDocID(guildID, channelID, city string) string {
	return fmt.Sprintf("%s_%s_facebook_%s", guildID, channelID, strings.ToLower(strings.ReplaceAll(city, " ", "-")))
}

// SaveFacebookSubscription creates or updates a Facebook subscription.
func (c *Client) SaveFacebookSubscription(ctx context.Context, sub models.Subscription) error {
	return c.SetDocument(ctx, subscriptionsCollection, facebookSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.City), sub)
}

// RemoveFacebookSubscription removes a Facebook subscription by guild, channel, and city.
func (c *Client) RemoveFacebookSubscription(ctx context.Context, guildID, channelID, city string) error {
	return c.DeleteDocument(ctx, subscriptionsCollection, facebookSubscriptionDocID(guildID, channelID, city))
}

// GetFacebookSubscriptions retrieves all active Facebook subscriptions.
func (c *Client) GetFacebookSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{"subscriptionType": "facebook"}, nil)
}

// GetFacebookSubscriptionsByGuild retrieves all Facebook subscriptions for a guild.
func (c *Client) GetFacebookSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{
		"guildID":          guildID,
		"subscriptionType": "facebook",
	}, nil)
}
