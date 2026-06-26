package storage

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	corePriceHistoryCollection     = "core_price_history"
	coreCategoryStatsCollection    = "core_category_stats"
	coreRawNotificationsCollection = "core_raw_notifications"
)

// GetCorePriceHistory retrieves the price history for a given product.
func (c *Client) GetCorePriceHistory(ctx context.Context, productName string) (*models.CorePriceHistory, bool, error) {
	var history models.CorePriceHistory
	ok, err := c.GetDocument(ctx, corePriceHistoryCollection, productName, &history)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &history, true, nil
}

// SaveCorePriceHistory saves or updates the price history for a product.
func (c *Client) SaveCorePriceHistory(ctx context.Context, history models.CorePriceHistory) error {
	return c.SetDocument(ctx, corePriceHistoryCollection, history.ProductName, history)
}

// GetCoreCategoryStats retrieves the observation stats for a category.
func (c *Client) GetCoreCategoryStats(ctx context.Context, category string) (*models.CoreCategoryStats, bool, error) {
	var stats models.CoreCategoryStats
	ok, err := c.GetDocument(ctx, coreCategoryStatsCollection, category, &stats)
	if err != nil || !ok {
		return nil, ok, err
	}
	return &stats, true, nil
}

// SaveCoreCategoryStats saves or updates the observation stats for a category.
func (c *Client) SaveCoreCategoryStats(ctx context.Context, stats models.CoreCategoryStats) error {
	return c.SetDocument(ctx, coreCategoryStatsCollection, stats.Category, stats)
}

// WipeCorePriceHistory clears all core price history and category stats.
func (c *Client) WipeCorePriceHistory(ctx context.Context) error {
	if _, err := c.pg.Exec(ctx, "DELETE FROM documents WHERE collection = $1 OR collection = $2", corePriceHistoryCollection, coreCategoryStatsCollection); err != nil {
		return err
	}
	return nil
}

// GetCoreSubscriptionsByGuild retrieves all core subscriptions for a specific guild.
func (c *Client) GetCoreSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{
		"guildID":          guildID,
		"subscriptionType": "core",
	}, nil)
}

// SaveCoreRawNotification saves a raw notification received from the phone listener.
func (c *Client) SaveCoreRawNotification(ctx context.Context, notif models.CoreRawNotification) error {
	return c.SetDocument(ctx, coreRawNotificationsCollection, notif.EventID, notif)
}

// GetRecentCoreRawNotifications retrieves raw notifications since the given duration.
func (c *Client) GetRecentCoreRawNotifications(ctx context.Context, duration time.Duration) ([]models.CoreRawNotification, error) {
	since := time.Now().Add(-duration)
	rows, err := c.pg.Query(ctx, `
		SELECT data
		FROM (
			SELECT data,
				CASE
					WHEN data ? 'receivedAt' AND data->>'receivedAt' <> '' THEN (data->>'receivedAt')::timestamptz
					ELSE updated_at
				END AS received_at
			FROM documents
			WHERE collection = $1
		) raw
		WHERE received_at >= $2
		ORDER BY received_at DESC`, coreRawNotificationsCollection, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []models.CoreRawNotification
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var data map[string]any
		if err := json.Unmarshal(payload, &data); err != nil {
			return nil, err
		}
		var notif models.CoreRawNotification
		if err := decodeDocument(data, &notif); err != nil {
			return nil, err
		}
		list = append(list, notif)
	}
	return list, rows.Err()
}

// PruneCoreRawNotifications prunes raw notifications older than the specified max age.
func (c *Client) PruneCoreRawNotifications(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().Add(-maxAge)
	tag, err := c.pg.Exec(ctx, `
		DELETE FROM documents
		WHERE collection = $1
			AND CASE
				WHEN data ? 'receivedAt' AND data->>'receivedAt' <> '' THEN (data->>'receivedAt')::timestamptz
				ELSE updated_at
			END < $2`, coreRawNotificationsCollection, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

const coreConfigCollection = "core_config"

// GetCoreRules retrieves the active regex replacement rules.
func (c *Client) GetCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	var cfg models.CoreRulesConfig
	ok, err := c.GetDocument(ctx, coreConfigCollection, "active_rules", &cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []models.CoreRule{}, nil
	}
	return cfg.Rules, nil
}

// SaveCoreRules saves or updates the active regex replacement rules.
func (c *Client) SaveCoreRules(ctx context.Context, rules []models.CoreRule) error {
	return c.SetDocument(ctx, coreConfigCollection, "active_rules", models.CoreRulesConfig{
		ID:        "active_rules",
		Rules:     rules,
		UpdatedAt: time.Now(),
	})
}

// GetPendingCoreRules retrieves the pending (unapproved) regex rules.
func (c *Client) GetPendingCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	var cfg models.CoreRulesConfig
	ok, err := c.GetDocument(ctx, coreConfigCollection, "pending_rules", &cfg)
	if err != nil {
		return nil, err
	}
	if !ok {
		return []models.CoreRule{}, nil
	}
	return cfg.Rules, nil
}

// SavePendingCoreRules saves the pending (unapproved) regex rules for review.
func (c *Client) SavePendingCoreRules(ctx context.Context, rules []models.CoreRule) error {
	return c.SetDocument(ctx, coreConfigCollection, "pending_rules", models.CoreRulesConfig{
		ID:        "pending_rules",
		Rules:     rules,
		UpdatedAt: time.Now(),
	})
}

// DeletePendingCoreRules deletes the pending regex rules.
func (c *Client) DeletePendingCoreRules(ctx context.Context) error {
	return c.DeleteDocument(ctx, coreConfigCollection, "pending_rules")
}
