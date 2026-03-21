package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const firestoreCollection = "deals"

// DefaultTimeout is the default duration for Firestore operations if the context has no deadline.
const DefaultTimeout = 30 * time.Second

type Client struct {
	client *firestore.Client
}

// ensureDeadline returns a context with a deadline if one isn't already set.
// The caller must defer the returned cancel function.
func ensureDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); !ok {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}

func New(ctx context.Context, projectID string) (*Client, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("firestore.NewClient: %w", err)
	}
	return &Client{client: client}, nil
}

func (c *Client) Close() error {
	return c.client.Close()
}

// GetDealByID retrieves a deal by its Firestore Document ID.
func (c *Client) GetDealByID(ctx context.Context, id string) (*models.DealInfo, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docRef := c.client.Collection(firestoreCollection).Doc(id)
	doc, err := docRef.Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get deal by ID %s: %w", id, err)
	}

	var deal models.DealInfo
	if err := doc.DataTo(&deal); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deal data: %w", err)
	}
	deal.FirestoreID = doc.Ref.ID
	return &deal, nil
}

// GetDealsByIDs retrieves multiple deals by their Firestore Document IDs in a single batch read.
// Returns a map of ID -> DealInfo. Missing documents are omitted from the map (no error).
func (c *Client) GetDealsByIDs(ctx context.Context, ids []string) (map[string]*models.DealInfo, error) {
	if len(ids) == 0 {
		return make(map[string]*models.DealInfo), nil
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	refs := make([]*firestore.DocumentRef, len(ids))
	for i, id := range ids {
		refs[i] = c.client.Collection(firestoreCollection).Doc(id)
	}

	docs, err := c.client.GetAll(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get deals: %w", err)
	}

	result := make(map[string]*models.DealInfo, len(docs))
	for _, doc := range docs {
		if !doc.Exists() {
			continue
		}
		var deal models.DealInfo
		if err := doc.DataTo(&deal); err != nil {
			slog.Warn("Failed to unmarshal deal in batch read", "id", doc.Ref.ID, "error", err)
			continue
		}
		deal.FirestoreID = doc.Ref.ID
		result[doc.Ref.ID] = &deal
	}

	return result, nil
}

// TryCreateDeal attempts to create a new deal. Returns error if it already exists.
func (c *Client) TryCreateDeal(ctx context.Context, deal models.DealInfo) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	collectionRef := c.client.Collection(firestoreCollection)
	docRef := collectionRef.Doc(deal.FirestoreID)
	// Create fails if the document already exists.
	_, err := docRef.Create(ctx, deal)
	if err != nil {
		if status.Code(err) == codes.AlreadyExists {
			return models.ErrDealExists
		}
		return err
	}
	return nil
}

// buildDealUpdates constructs the Firestore update fields for a deal.
// This is shared between UpdateDeal and BatchWrite to keep field lists in sync.
func buildDealUpdates(deal models.DealInfo) []firestore.Update {
	updates := []firestore.Update{
		{Path: "title", Value: deal.Title},
		{Path: "threadImageURL", Value: deal.ThreadImageURL},
		{Path: "actualDealURL", Value: deal.ActualDealURL},
		{Path: "lastUpdated", Value: deal.LastUpdated},
		{Path: "discordMessageID", Value: deal.DiscordMessageID},
		{Path: "discordMessageIDs", Value: deal.DiscordMessageIDs},
		{Path: "discordLastUpdatedTime", Value: deal.DiscordLastUpdatedTime},
		{Path: "publishedTimestamp", Value: deal.PublishedTimestamp},
		{Path: "cleanTitle", Value: deal.CleanTitle},
		{Path: "isWarm", Value: deal.IsWarm},
		{Path: "isLavaHot", Value: deal.IsLavaHot},
		{Path: "hasBeenWarm", Value: deal.HasBeenWarm},
		{Path: "hasBeenHot", Value: deal.HasBeenHot},
		{Path: "aiProcessed", Value: deal.AIProcessed},
		{Path: "threads", Value: deal.Threads},
		{Path: "searchTokens", Value: deal.SearchTokens},
		{Path: "category", Value: deal.Category},
		{Path: "retailer", Value: deal.Retailer},
		{Path: "price", Value: deal.Price},
		{Path: "originalPrice", Value: deal.OriginalPrice},
		{Path: "savings", Value: deal.Savings},
	}

	// Handle optional fields that should be deleted if empty to save space
	// This helps prevent "leaky bucket" storage growth
	if deal.Description == "" {
		updates = append(updates, firestore.Update{Path: "description", Value: firestore.Delete})
	} else {
		updates = append(updates, firestore.Update{Path: "description", Value: deal.Description})
	}

	if deal.Comments == "" {
		updates = append(updates, firestore.Update{Path: "comments", Value: firestore.Delete})
	} else {
		updates = append(updates, firestore.Update{Path: "comments", Value: deal.Comments})
	}

	if deal.Summary == "" {
		updates = append(updates, firestore.Update{Path: "summary", Value: firestore.Delete})
	} else {
		updates = append(updates, firestore.Update{Path: "summary", Value: deal.Summary})
	}

	return updates
}

// UpdateDeal updates a specific deal using specific fields to avoid race conditions.
func (c *Client) UpdateDeal(ctx context.Context, deal models.DealInfo) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	collectionRef := c.client.Collection(firestoreCollection)
	docRef := collectionRef.Doc(deal.FirestoreID)

	_, err := docRef.Update(ctx, buildDealUpdates(deal))
	return err
}

// TrimOldDeals deletes the oldest deals (by PublishedTimestamp) from the "deals" collection
func (c *Client) TrimOldDeals(ctx context.Context, maxDeals int) error {
	ctx, cancel := ensureDeadline(ctx, 2*time.Minute) // Longer timeout for cleanup
	defer cancel()

	slog.Debug("TrimOldDeals: Entered function", "maxDeals", maxDeals)
	collectionRef := c.client.Collection(firestoreCollection)

	// Get current count
	countSnapshot, err := collectionRef.NewAggregationQuery().WithCount("all").Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to get deal count for trimming: %w", err)
	}

	countValue, ok := countSnapshot["all"]
	if !ok {
		return fmt.Errorf("count aggregation result for trimming was invalid: 'all' key missing")
	}

	var currentDealCountInt64 int64
	switch val := countValue.(type) {
	case int64:
		currentDealCountInt64 = val
	case *firestorepb.Value:
		currentDealCountInt64 = val.GetIntegerValue()
	default:
		return fmt.Errorf("count aggregation result has unexpected type %T", countValue)
	}

	currentDealCount := int(currentDealCountInt64)

	if currentDealCount <= maxDeals {
		return nil
	}

	numToDelete := currentDealCount - maxDeals
	slog.Info("TrimOldDeals: Trimming needed", "current", currentDealCount, "max", maxDeals, "deleting", numToDelete)

	// Query for the oldest deals to delete
	iter := collectionRef.
		OrderBy("lastUpdated", firestore.Asc). // Ascending to get oldest first
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	deletedCount := 0
	bulkWriter := c.client.BulkWriter(ctx)
	defer func() {
		bulkWriter.Flush()
		bulkWriter.End()
	}()

	var errs []error
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to iterate deals for trimming: %w", err)
		}

		if _, delErr := bulkWriter.Delete(doc.Ref); delErr != nil {
			slog.Error("TrimOldDeals: Error queueing delete", "id", doc.Ref.ID, "error", delErr)
			errs = append(errs, fmt.Errorf("delete %s: %w", doc.Ref.ID, delErr))
			continue
		}
		deletedCount++
	}

	if deletedCount > 0 {
		logger.Notice("TrimOldDeals: Flushed delete operations", "queued", deletedCount)
	}

	return errors.Join(errs...)
}

// BatchWrite executes multiple creates and updates in a single bulk operation.
func (c *Client) BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error {
	if len(creates) == 0 && len(updates) == 0 {
		return nil
	}

	bw := c.client.BulkWriter(ctx)
	col := c.client.Collection(firestoreCollection)

	var errs []error
	for _, d := range creates {
		doc := col.Doc(d.FirestoreID)
		if _, err := bw.Create(doc, d); err != nil {
			slog.Error("BatchWrite: Failed to queue create", "id", d.FirestoreID, "error", err)
			errs = append(errs, fmt.Errorf("create %s: %w", d.FirestoreID, err))
		}
	}

	for _, d := range updates {
		doc := col.Doc(d.FirestoreID)
		if _, err := bw.Update(doc, buildDealUpdates(d)); err != nil {
			slog.Error("BatchWrite: Failed to queue update", "id", d.FirestoreID, "error", err)
			errs = append(errs, fmt.Errorf("update %s: %w", d.FirestoreID, err))
		}
	}

	bw.Flush()
	bw.End()
	return errors.Join(errs...)
}

// Ping checks connectivity to Firestore.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collections(ctx)
	_, err := iter.Next()
	if err != nil && err != iterator.Done {
		return err
	}
	return nil
}

// GetRecentDeals fetches deals published within the given duration ago from now.
func (c *Client) GetRecentDeals(ctx context.Context, d time.Duration) ([]models.DealInfo, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	since := time.Now().Add(-d)
	iter := c.client.Collection(firestoreCollection).
		Where("publishedTimestamp", ">=", since).
		OrderBy("publishedTimestamp", firestore.Desc).
		Documents(ctx)
	defer iter.Stop()

	var deals []models.DealInfo
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate recent deals: %w", err)
		}
		var deal models.DealInfo
		if err := doc.DataTo(&deal); err != nil {
			slog.Warn("Failed to unmarshal recent deal", "id", doc.Ref.ID, "error", err)
			continue
		}
		deal.FirestoreID = doc.Ref.ID
		deals = append(deals, deal)
	}
	return deals, nil
}

// GetGeminiQuotaStatus retrieves the Gemini fallback state.
func (c *Client) GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	doc, err := c.client.Collection("bot_config").Doc("gemini_quota").Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil // Not found is not an error, we just initialize it
		}
		return nil, fmt.Errorf("failed to get gemini quota status: %w", err)
	}

	var quota models.GeminiQuotaStatus
	if err := doc.DataTo(&quota); err != nil {
		return nil, fmt.Errorf("failed to unmarshal gemini quota status: %w", err)
	}

	return &quota, nil
}

// UpdateGeminiQuotaStatus updates the Gemini fallback state.
func (c *Client) UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	quota.LastUpdated = time.Now()
	_, err := c.client.Collection("bot_config").Doc("gemini_quota").Set(ctx, quota)
	if err != nil {
		return fmt.Errorf("failed to update gemini quota status: %w", err)
	}

	return nil
}
