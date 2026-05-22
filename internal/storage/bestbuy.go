package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const bestbuyCollection = "bestbuy_deals"
const bestbuySellersCollection = "bestbuy_sellers"
const bestbuyComputeCollection = "bestbuy_compute_observations"
const bestbuySoldCompCacheCollection = "bestbuy_sold_comp_cache"

// GetActiveBestBuySellers returns active Best Buy seller targets.
func (c *Client) GetActiveBestBuySellers(ctx context.Context) ([]bestbuy.Seller, error) {
	rows, err := c.ListDocumentsWhere(ctx, bestbuySellersCollection, map[string]any{"isActive": true})
	if err != nil {
		return nil, err
	}
	var sellers []bestbuy.Seller
	for _, row := range rows {
		var seller bestbuy.Seller
		if err := decodeDocument(row.Data, &seller); err != nil {
			slog.Warn("Failed to decode bestbuy seller", "processor", "bestbuy", "id", row.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedBestBuySellers backfills any missing default seller targets.
func (c *Client) SeedBestBuySellers(ctx context.Context) (bool, error) {
	rows, err := c.ListDocuments(ctx, bestbuySellersCollection)
	if err != nil {
		return false, err
	}

	existingIDs := make(map[string]struct{}, len(rows))
	existingNames := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		existingIDs[row.ID] = struct{}{}
		var seller bestbuy.Seller
		if err := decodeDocument(row.Data, &seller); err != nil {
			slog.Warn("Failed to decode existing bestbuy seller while seeding", "processor", "bestbuy", "id", row.ID, "error", err)
			continue
		}
		if name := strings.ToLower(strings.TrimSpace(seller.Name)); name != "" {
			existingNames[name] = struct{}{}
		}
	}

	now := time.Now()
	var errs []error
	created := false
	for _, seller := range bestbuy.DefaultSellers {
		seller.AddedAt = now
		if !seller.IsActive {
			seller.IsActive = true
		}
		docID := seller.ID
		if docID == "" {
			docID = seller.Name
		}
		if _, ok := existingIDs[docID]; ok {
			continue
		}
		if _, ok := existingNames[strings.ToLower(strings.TrimSpace(seller.Name))]; ok {
			continue
		}
		if err := c.CreateDocument(ctx, bestbuySellersCollection, docID, seller); err != nil {
			errs = append(errs, fmt.Errorf("seed %s: %w", docID, err))
			continue
		}
		created = true
	}
	return created, errors.Join(errs...)
}

// bestBuyDocID generates a unique document ID for a Best Buy product.
func bestBuyDocID(sku, source string) string {
	return fmt.Sprintf("%s_%s", sku, source)
}

// GetBestBuyProduct returns a stored Best Buy product by SKU/source.
func (c *Client) GetBestBuyProduct(ctx context.Context, sku, source string) (bestbuy.AnalyzedProduct, bool, error) {
	var product bestbuy.AnalyzedProduct
	if sku == "" || source == "" {
		return product, false, nil
	}
	ok, err := c.GetDocument(ctx, bestbuyCollection, bestBuyDocID(sku, source), &product)
	return product, ok, err
}

// SaveBestBuyProduct saves a product.
func (c *Client) SaveBestBuyProduct(ctx context.Context, product bestbuy.AnalyzedProduct) error {
	return c.SetDocument(ctx, bestbuyCollection, bestBuyDocID(product.SKU, product.Source), product)
}

func (c *Client) RefreshBestBuyProduct(ctx context.Context, product bestbuy.AnalyzedProduct) error {
	return c.SetDocument(ctx, bestbuyCollection, bestBuyDocID(product.SKU, product.Source), product)
}

func (c *Client) GetBestBuySoldCompSnapshot(ctx context.Context, key string) (bestbuy.SoldCompSnapshot, bool, error) {
	var snapshot bestbuy.SoldCompSnapshot
	if key == "" {
		return snapshot, false, nil
	}
	ok, err := c.GetDocument(ctx, bestbuySoldCompCacheCollection, key, &snapshot)
	return snapshot, ok, err
}

func (c *Client) SaveBestBuySoldCompSnapshot(ctx context.Context, key string, snapshot bestbuy.SoldCompSnapshot) error {
	if key == "" {
		return nil
	}
	snapshot.Key = key
	return c.SetDocument(ctx, bestbuySoldCompCacheCollection, key, snapshot)
}

func bestBuyComputeDocID(sku, source string) string {
	return fmt.Sprintf("%s_%s", sku, source)
}

func (c *Client) SaveBestBuyComputeObservation(ctx context.Context, observation bestbuy.ComputeObservation) error {
	return c.SetDocument(ctx, bestbuyComputeCollection, bestBuyComputeDocID(observation.SKU, observation.Source), observation)
}

func (c *Client) ListBestBuyComputeObservations(ctx context.Context) ([]bestbuy.ComputeObservation, error) {
	rows, err := c.ListDocuments(ctx, bestbuyComputeCollection)
	if err != nil {
		return nil, err
	}
	observations := make([]bestbuy.ComputeObservation, 0, len(rows))
	for _, row := range rows {
		var observation bestbuy.ComputeObservation
		if err := decodeDocument(row.Data, &observation); err != nil {
			slog.Warn("Failed to decode bestbuy compute observation", "processor", "bestbuy_compute", "id", row.ID, "error", err)
			continue
		}
		observations = append(observations, observation)
	}
	return observations, nil
}

func (c *Client) PruneBestBuyComputeObservations(ctx context.Context, maxAgeDays, maxRecords int) error {
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	_, err := c.PruneDocumentsByTime(ctx, bestbuyComputeCollection, "lastSeen", cutoff, maxRecords)
	return err
}

// PruneBestBuyProducts deletes products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	_, err := c.PruneDocumentsByTime(ctx, bestbuyCollection, "lastSeen", cutoff, maxRecords)
	return err
}

// bestBuySubscriptionDocID generates a unique document ID for a Best Buy subscription.
func bestBuySubscriptionDocID(guildID, channelID string) string {
	return fmt.Sprintf("%s_%s_bestbuy", guildID, channelID)
}

// SaveBestBuySubscription creates or updates a Best Buy subscription.
func (c *Client) SaveBestBuySubscription(ctx context.Context, sub models.Subscription) error {
	return c.SetDocument(ctx, subscriptionsCollection, bestBuySubscriptionDocID(sub.GuildID, sub.ChannelID), sub)
}

// RemoveBestBuySubscription removes a Best Buy subscription by guild and channel.
func (c *Client) RemoveBestBuySubscription(ctx context.Context, guildID, channelID string) error {
	return c.DeleteDocument(ctx, subscriptionsCollection, bestBuySubscriptionDocID(guildID, channelID))
}

// GetBestBuySubscriptionsByGuild retrieves all Best Buy subscriptions for a guild.
func (c *Client) GetBestBuySubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	return c.subscriptionsMatching(ctx, map[string]any{
		"guildID":          guildID,
		"subscriptionType": "bestbuy",
	}, nil)
}
