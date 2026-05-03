package storage

import (
	"context"
	"errors"
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
const bestbuySellersCollection = "bestbuy_sellers"

// GetActiveBestBuySellers returns active Best Buy seller targets from Firestore.
func (c *Client) GetActiveBestBuySellers(ctx context.Context) ([]bestbuy.Seller, error) {
	if c.usesPostgres() {
		rows, err := c.ListDocuments(ctx, bestbuySellersCollection)
		if err != nil {
			return nil, err
		}
		var sellers []bestbuy.Seller
		for _, row := range rows {
			if !documentBool(row.Data, "isActive") {
				continue
			}
			var seller bestbuy.Seller
			if err := decodeDocument(row.Data, &seller); err != nil {
				slog.Warn("Failed to decode bestbuy seller", "processor", "bestbuy", "id", row.ID, "error", err)
				continue
			}
			sellers = append(sellers, seller)
		}
		return sellers, nil
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(bestbuySellersCollection).
		Where("isActive", "==", true).
		Documents(ctx)
	defer iter.Stop()

	var sellers []bestbuy.Seller
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate bestbuy sellers: %w", err)
		}
		var seller bestbuy.Seller
		if err := doc.DataTo(&seller); err != nil {
			slog.Warn("Failed to unmarshal bestbuy seller", "processor", "bestbuy", "id", doc.Ref.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedBestBuySellers populates the seller target collection if empty.
func (c *Client) SeedBestBuySellers(ctx context.Context) (bool, error) {
	if c.usesPostgres() {
		rows, err := c.ListDocuments(ctx, bestbuySellersCollection)
		if err != nil {
			return false, err
		}
		if len(rows) > 0 {
			return false, nil
		}
		now := time.Now()
		var errs []error
		for _, seller := range bestbuy.DefaultSellers {
			seller.AddedAt = now
			if !seller.IsActive {
				seller.IsActive = true
			}
			docID := seller.ID
			if docID == "" {
				docID = seller.Name
			}
			if err := c.CreateDocument(ctx, bestbuySellersCollection, docID, seller); err != nil {
				errs = append(errs, fmt.Errorf("seed %s: %w", docID, err))
			}
		}
		return true, errors.Join(errs...)
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(bestbuySellersCollection).Limit(1).Documents(ctx)
	defer iter.Stop()
	_, err := iter.Next()
	if err != iterator.Done {
		if err != nil {
			return false, fmt.Errorf("failed to check bestbuy sellers collection: %w", err)
		}
		return false, nil
	}

	now := time.Now()
	bw := c.client.BulkWriter(ctx)
	var errs []error
	for _, seller := range bestbuy.DefaultSellers {
		seller.AddedAt = now
		if seller.IsActive == false {
			seller.IsActive = true
		}
		docID := seller.ID
		if docID == "" {
			docID = seller.Name
		}
		if _, err := bw.Create(c.client.Collection(bestbuySellersCollection).Doc(docID), seller); err != nil {
			errs = append(errs, fmt.Errorf("seed %s: %w", docID, err))
		}
	}
	bw.Flush()
	bw.End()
	return true, errors.Join(errs...)
}

// bestBuyDocID generates a unique document ID for a Best Buy product.
func bestBuyDocID(sku, source string) string {
	return fmt.Sprintf("%s_%s", sku, source)
}

// BestBuyProductExists checks whether a product already exists in Firestore.
func (c *Client) BestBuyProductExists(ctx context.Context, sku, source string) (bool, error) {
	if sku == "" || source == "" {
		return false, nil
	}
	if c.usesPostgres() {
		_, ok, err := c.GetRawDocument(ctx, bestbuyCollection, bestBuyDocID(sku, source))
		return ok, err
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
	if c.usesPostgres() {
		return c.SetDocument(ctx, bestbuyCollection, bestBuyDocID(product.SKU, product.Source), product)
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := bestBuyDocID(product.SKU, product.Source)
	_, err := c.client.Collection(bestbuyCollection).Doc(docID).Set(ctx, product)
	if err != nil {
		return fmt.Errorf("failed to save bestbuy product %s: %v", docID, err)
	}
	return nil
}

func (c *Client) RefreshBestBuyProductLastSeen(ctx context.Context, product bestbuy.Product) error {
	docID := bestBuyDocID(product.SKU, product.Source)
	now := time.Now()
	if c.usesPostgres() {
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

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()
	_, err := c.client.Collection(bestbuyCollection).Doc(docID).Update(ctx, []firestore.Update{
		{Path: "lastSeen", Value: now},
		{Path: "regularPrice", Value: product.RegularPrice},
		{Path: "salePrice", Value: product.SalePrice},
		{Path: "saleEndDate", Value: product.SaleEndDate},
		{Path: "customerRating", Value: product.CustomerRating},
		{Path: "imageURL", Value: product.ImageURL},
		{Path: "url", Value: product.URL},
	})
	if err != nil {
		return fmt.Errorf("failed to refresh bestbuy product %s: %w", docID, err)
	}
	return nil
}

// PruneBestBuyProducts deletes products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneBestBuyProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	if c.usesPostgres() {
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
	if c.usesPostgres() {
		return c.SetDocument(ctx, subscriptionsCollection, bestBuySubscriptionDocID(sub.GuildID, sub.ChannelID), sub)
	}

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
	if c.usesPostgres() {
		return c.DeleteDocument(ctx, subscriptionsCollection, bestBuySubscriptionDocID(guildID, channelID))
	}

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
	if c.usesPostgres() {
		return c.subscriptionsWhere(ctx, func(row Document) bool {
			return documentString(row.Data, "guildID") == guildID &&
				documentString(row.Data, "subscriptionType") == "bestbuy"
		})
	}

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

func bestbuyDiscount(p bestbuy.Product) float64 {
	if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
		return (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
	}
	return 0
}
