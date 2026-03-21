package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
)

const (
	ebaySellersCollection = "ebay_sellers"
)

// --- eBay Sellers ---

// GetActiveEbaySellers returns all active sellers from Firestore.
func (c *Client) GetActiveEbaySellers(ctx context.Context) ([]ebay.EbaySeller, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	iter := c.client.Collection(ebaySellersCollection).
		Where("isActive", "==", true).
		Documents(ctx)
	defer iter.Stop()

	var sellers []ebay.EbaySeller
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate ebay sellers: %w", err)
		}

		var seller ebay.EbaySeller
		if err := doc.DataTo(&seller); err != nil {
			slog.Warn("Failed to unmarshal ebay seller", "id", doc.Ref.ID, "error", err)
			continue
		}
		sellers = append(sellers, seller)
	}
	return sellers, nil
}

// SeedEbaySellers populates the ebay_sellers collection if it's empty.
// Returns true if seeding was performed.
func (c *Client) SeedEbaySellers(ctx context.Context) (bool, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	// Check if collection has any documents
	iter := c.client.Collection(ebaySellersCollection).Limit(1).Documents(ctx)
	defer iter.Stop()

	_, err := iter.Next()
	if err != iterator.Done {
		if err != nil {
			return false, fmt.Errorf("failed to check ebay sellers collection: %w", err)
		}
		// Collection already has documents, skip seeding
		return false, nil
	}

	// Collection is empty, seed with defaults
	slog.Info("Seeding ebay_sellers collection with default sellers")
	defaults := ebay.DefaultSellers()

	bw := c.client.BulkWriter(ctx)
	var errs []error
	for _, seller := range defaults {
		doc := c.client.Collection(ebaySellersCollection).Doc(seller.Username)
		if _, err := bw.Create(doc, seller); err != nil {
			slog.Error("Failed to queue ebay seller seed", "username", seller.Username, "error", err)
			errs = append(errs, fmt.Errorf("seed %s: %w", seller.Username, err))
		}
	}
	bw.Flush()
	bw.End()

	if err := errors.Join(errs...); err != nil {
		return true, fmt.Errorf("seeding partially failed: %w", err)
	}
	slog.Info("Seeded ebay_sellers collection", "count", len(defaults))
	return true, nil
}

// --- eBay Poll State ---

// GetEbayPollState retrieves the eBay polling state from bot_config.
func (c *Client) GetEbayPollState(ctx context.Context) (*ebay.EbayPollState, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	doc, err := c.client.Collection("bot_config").Doc("ebay_poll_state").Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get ebay poll state: %w", err)
	}

	var state ebay.EbayPollState
	if err := doc.DataTo(&state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ebay poll state: %w", err)
	}
	return &state, nil
}

// UpdateEbayPollState updates the eBay polling state in bot_config.
func (c *Client) UpdateEbayPollState(ctx context.Context, state ebay.EbayPollState) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	state.LastUpdated = time.Now()
	_, err := c.client.Collection("bot_config").Doc("ebay_poll_state").Set(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to update ebay poll state: %w", err)
	}
	return nil
}
