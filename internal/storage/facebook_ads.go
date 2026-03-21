package storage

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const facebookAdsCollection = "car_deals"
const priceHistoryCollection = "price_history"

// sanitizeFacebookDocID replaces characters that are invalid in Firestore document IDs.
func sanitizeFacebookDocID(s string) string {
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, ".", "_")
	if s == "" {
		s = "unknown"
	}
	return s
}

// SaveFacebookAd saves or updates a Facebook ad record, returning true if it's a new record.
func (c *Client) SaveFacebookAd(ctx context.Context, ad *models.FacebookAdRecord) (bool, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := fmt.Sprintf("%s-%s-%d", sanitizeFacebookDocID(ad.Model), sanitizeFacebookDocID(ad.Price), ad.Year)
	docRef := c.client.Collection(facebookAdsCollection).Doc(docID)

	_, err := docRef.Get(ctx)
	isNew := false
	if err != nil {
		if status.Code(err) == codes.NotFound {
			isNew = true
		} else {
			return false, fmt.Errorf("failed to check existing facebook ad: %v", err)
		}
	}

	ad.LastSeen = time.Now()
	if isNew {
		ad.ProcessedAt = time.Now()
	}

	_, err = docRef.Set(ctx, ad)
	if err != nil {
		return false, fmt.Errorf("failed to save facebook ad: %v", err)
	}

	return isNew, nil
}

// PruneFacebookAds deletes Facebook ads older than maxAgeMonths or exceeding maxRecords.
func (c *Client) PruneFacebookAds(ctx context.Context, maxAgeMonths int, maxRecords int) error {
	ctx, cancel := ensureDeadline(ctx, 2*time.Minute)
	defer cancel()

	// Delete by age
	cutoff := time.Now().AddDate(0, -maxAgeMonths, 0)
	iter := c.client.Collection(facebookAdsCollection).Where("last_seen", "<", cutoff).Documents(ctx)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if _, err := doc.Ref.Delete(ctx); err != nil {
			slog.Warn("Failed to delete old facebook ad", "id", doc.Ref.ID, "error", err)
		}
	}

	// Delete oldest if exceeding maxRecords
	allIter := c.client.Collection(facebookAdsCollection).Documents(ctx)
	totalCount := 0
	for {
		_, err := allIter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to count facebook ad records: %v", err)
		}
		totalCount++
	}

	if totalCount > maxRecords {
		excess := totalCount - maxRecords
		deleteIter := c.client.Collection(facebookAdsCollection).OrderBy("last_seen", firestore.Asc).Limit(excess).Documents(ctx)
		for {
			doc, err := deleteIter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to iterate excess facebook records: %v", err)
			}
			if _, err := doc.Ref.Delete(ctx); err != nil {
				slog.Warn("Failed to delete excess facebook record", "id", doc.Ref.ID, "error", err)
			}
		}
	}

	return nil
}

// SavePriceHistory stores a daily price snapshot for a vehicle model.
func (c *Client) SavePriceHistory(ctx context.Context, model string, value float64) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	today := time.Now().Format("2006-01-02")
	docID := fmt.Sprintf("%s-%s", model, today)

	history := models.PriceHistory{
		Model: model,
		Date:  today,
		Value: value,
	}

	_, err := c.client.Collection(priceHistoryCollection).Doc(docID).Set(ctx, history)
	if err != nil {
		return fmt.Errorf("failed to save price history: %v", err)
	}

	return nil
}
