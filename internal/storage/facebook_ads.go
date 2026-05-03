package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const facebookAdsCollection = "car_deals"
const priceHistoryCollection = "price_history"

// sanitizeFacebookDocID replaces characters that are invalid in document IDs.
func sanitizeFacebookDocID(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ".", "_")
	if s == "" {
		s = "unknown"
	}
	return s
}

// FacebookAdExists checks whether a Facebook ad with the given listing ID already exists.
// This enables early dedup checks before expensive Gemini normalization calls.
func (c *Client) FacebookAdExists(ctx context.Context, listingID string) (bool, error) {
	if listingID == "" {
		return false, nil
	}
	_, ok, err := c.GetRawDocument(ctx, facebookAdsCollection, fmt.Sprintf("fb-%s", listingID))
	return ok, err
}

// SaveFacebookAd saves or updates a Facebook ad record, returning true if it's a new record.
// Uses the Facebook listing ID as the primary dedup key when available, falling back
// to model-price-year composite key for backwards compatibility.
func (c *Client) SaveFacebookAd(ctx context.Context, ad *models.FacebookAdRecord) (bool, error) {
	var docID string
	if ad.ID != "" {
		docID = fmt.Sprintf("fb-%s", ad.ID)
	} else {
		docID = fmt.Sprintf("%s-%s-%d", sanitizeFacebookDocID(ad.Model), sanitizeFacebookDocID(ad.Price), ad.Year)
	}
	_, exists, err := c.GetRawDocument(ctx, facebookAdsCollection, docID)
	if err != nil {
		return false, err
	}
	ad.LastSeen = time.Now()
	if !exists {
		ad.ProcessedAt = time.Now()
	}
	if err := c.SetDocument(ctx, facebookAdsCollection, docID, ad); err != nil {
		return false, err
	}
	return !exists, nil
}

// PruneFacebookAds deletes Facebook ads older than maxAgeMonths or exceeding maxRecords.
func (c *Client) PruneFacebookAds(ctx context.Context, maxAgeMonths int, maxRecords int) error {
	rows, err := c.ListDocuments(ctx, facebookAdsCollection)
	if err != nil {
		return err
	}
	cutoff := time.Now().AddDate(0, -maxAgeMonths, 0)
	for _, row := range rows {
		if !documentTime(row.Data, "last_seen").IsZero() && documentTime(row.Data, "last_seen").Before(cutoff) {
			if err := c.DeleteDocument(ctx, facebookAdsCollection, row.ID); err != nil {
				slog.Warn("Failed to delete old facebook ad", "processor", "facebook", "id", row.ID, "error", err)
			}
		}
	}
	rows, err = c.ListDocuments(ctx, facebookAdsCollection)
	if err != nil {
		return err
	}
	if len(rows) > maxRecords {
		sortDocumentsByTime(rows, "last_seen", true)
		for _, row := range rows[:len(rows)-maxRecords] {
			if err := c.DeleteDocument(ctx, facebookAdsCollection, row.ID); err != nil {
				slog.Warn("Failed to delete excess facebook record", "processor", "facebook", "id", row.ID, "error", err)
			}
		}
	}
	return nil
}

// SavePriceHistory stores a daily price snapshot for a vehicle model.
func (c *Client) SavePriceHistory(ctx context.Context, model string, value float64) error {
	today := time.Now().Format("2006-01-02")
	docID := fmt.Sprintf("%s-%s", model, today)
	return c.SetDocument(ctx, priceHistoryCollection, docID, models.PriceHistory{
		Model: model,
		Date:  today,
		Value: value,
	})
}
