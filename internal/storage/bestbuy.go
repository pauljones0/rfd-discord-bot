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

// BestBuyProductExists checks whether a product already exists.
func (c *Client) BestBuyProductExists(ctx context.Context, sku, source string) (bool, error) {
	if sku == "" || source == "" {
		return false, nil
	}
	_, ok, err := c.GetRawDocument(ctx, bestbuyCollection, bestBuyDocID(sku, source))
	return ok, err
}

// SaveBestBuyProduct saves a product.
func (c *Client) SaveBestBuyProduct(ctx context.Context, product bestbuy.AnalyzedProduct) error {
	return c.SetDocument(ctx, bestbuyCollection, bestBuyDocID(product.SKU, product.Source), product)
}

func (c *Client) RefreshBestBuyProductLastSeen(ctx context.Context, product bestbuy.Product) error {
	docID := bestBuyDocID(product.SKU, product.Source)
	now := time.Now()
	var existing bestbuy.AnalyzedProduct
	ok, err := c.GetDocument(ctx, bestbuyCollection, docID, &existing)
	if err != nil || !ok {
		return err
	}
	existing.Product = product
	existing.LastSeen = now
	if existing.DiscountPct == 0 {
		existing.DiscountPct = bestbuyDiscount(product)
	}
	return c.SetDocument(ctx, bestbuyCollection, docID, existing)
}

// PruneBestBuyProducts deletes products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	rows, err := c.ListDocuments(ctx, bestbuyCollection)
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	for _, row := range rows {
		if !documentTime(row.Data, "lastSeen").IsZero() && documentTime(row.Data, "lastSeen").Before(cutoff) {
			if err := c.DeleteDocument(ctx, bestbuyCollection, row.ID); err != nil {
				slog.Warn("Failed to delete old bestbuy product", "processor", "bestbuy", "id", row.ID, "error", err)
			}
		}
	}
	rows, err = c.ListDocuments(ctx, bestbuyCollection)
	if err != nil {
		return err
	}
	if len(rows) > maxRecords {
		sortDocumentsByTime(rows, "lastSeen", true)
		for _, row := range rows[:len(rows)-maxRecords] {
			if err := c.DeleteDocument(ctx, bestbuyCollection, row.ID); err != nil {
				slog.Warn("Failed to delete excess bestbuy record", "processor", "bestbuy", "id", row.ID, "error", err)
			}
		}
	}
	return nil
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

func bestbuyDiscount(p bestbuy.Product) float64 {
	if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
		return (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
	}
	return 0
}
