package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
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

func New(ctx context.Context, projectID string) (*Client, error) {
	// Initialize client with a timeout if not present
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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

// UpdateDeal updates a specific deal using specific fields to avoid race conditions.
func (c *Client) UpdateDeal(ctx context.Context, deal models.DealInfo) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	collectionRef := c.client.Collection(firestoreCollection)
	docRef := collectionRef.Doc(deal.FirestoreID)

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
		{Path: "isLavaHot", Value: deal.IsLavaHot},
		{Path: "aiProcessed", Value: deal.AIProcessed},
		{Path: "threads", Value: deal.Threads},
		{Path: "searchTokens", Value: deal.SearchTokens},
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

	_, err := docRef.Update(ctx, updates)
	return err
}

// TrimOldDeals deletes the oldest deals (by PublishedTimestamp) from the "deals" collection
func (c *Client) TrimOldDeals(ctx context.Context, maxDeals int) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute) // Longer timeout for cleanup
		defer cancel()
	}

	slog.Debug("TrimOldDeals: Entered function", "maxDeals", maxDeals)
	collectionRef := c.client.Collection(firestoreCollection)

	// To avoid the cost and latency of querying the count of the entire collection,
	// we first find the boundary document that represents the oldest deal we want to keep.
	// By ordering descending and offsetting by maxDeals, we skip the newest `maxDeals` deals.
	// The first document returned is the newest deal that should be deleted.
	boundaryIter := collectionRef.
		OrderBy("lastUpdated", firestore.Desc).
		Offset(maxDeals).
		Limit(1).
		Documents(ctx)

	boundaryDoc, err := boundaryIter.Next()
	boundaryIter.Stop()

	if err == iterator.Done {
		// No boundary document found, meaning we have <= maxDeals documents in total.
		// Nothing to trim.
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get boundary document for trimming: %w", err)
	}

	// Safely retrieve the boundary timestamp
	boundaryData := boundaryDoc.Data()
	lastUpdatedRaw, ok := boundaryData["lastUpdated"]
	if !ok {
		return fmt.Errorf("boundary document missing lastUpdated field")
	}
	boundaryTime, ok := lastUpdatedRaw.(time.Time)
	if !ok {
		return fmt.Errorf("boundary document lastUpdated is not a time.Time, got %T", lastUpdatedRaw)
	}

	batchSize := 500
	totalDeletedCount := 0

	// Now delete all documents that are older than or equal to the boundary document's timestamp.
	// We do this in a loop with a limit to avoid unbounded memory usage or timeouts.
	for {
		iter := collectionRef.
			Where("lastUpdated", "<=", boundaryTime).
			OrderBy("lastUpdated", firestore.Asc). // Ascending: delete oldest first
			Limit(batchSize).
			Documents(ctx)

		deletedCount := 0
		bulkWriter := c.client.BulkWriter(ctx)

		for {
			doc, iterErr := iter.Next()
			if iterErr == iterator.Done {
				break
			}
			if iterErr != nil {
				iter.Stop()
				return fmt.Errorf("failed to iterate deals for trimming: %w", iterErr)
			}

			_, delErr := bulkWriter.Delete(doc.Ref)
			if delErr != nil {
				slog.Error("TrimOldDeals: Error queueing delete", "id", doc.Ref.ID, "error", delErr)
				continue
			}
			deletedCount++
		}
		iter.Stop()

		if deletedCount > 0 {
			bulkWriter.Flush()
			totalDeletedCount += deletedCount
			// If we deleted fewer than the batch size, we're done.
			if deletedCount < batchSize {
				break
			}
		} else {
			// Nothing left to delete
			break
		}
	}

	if totalDeletedCount > 0 {
		logger.Notice("TrimOldDeals: Completed delete operations", "total_deleted", totalDeletedCount)
	}

	return nil
}

// BatchWrite executes multiple creates and updates in a single bulk operation.
func (c *Client) BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error {
	if len(creates) == 0 && len(updates) == 0 {
		return nil
	}

	bw := c.client.BulkWriter(ctx)
	col := c.client.Collection(firestoreCollection)

	for _, d := range creates {
		doc := col.Doc(d.FirestoreID)
		if _, err := bw.Create(doc, d); err != nil {
			slog.Error("BatchWrite: Failed to queue create", "id", d.FirestoreID, "error", err)
		}
	}

	for _, d := range updates {
		doc := col.Doc(d.FirestoreID)
		updates := []firestore.Update{
			{Path: "title", Value: d.Title},
			{Path: "threadImageURL", Value: d.ThreadImageURL},
			{Path: "actualDealURL", Value: d.ActualDealURL},
			{Path: "lastUpdated", Value: d.LastUpdated},
			{Path: "discordMessageID", Value: d.DiscordMessageID},
			{Path: "discordMessageIDs", Value: d.DiscordMessageIDs},
			{Path: "discordLastUpdatedTime", Value: d.DiscordLastUpdatedTime},
			{Path: "publishedTimestamp", Value: d.PublishedTimestamp},
			{Path: "cleanTitle", Value: d.CleanTitle},
			{Path: "isLavaHot", Value: d.IsLavaHot},
			{Path: "aiProcessed", Value: d.AIProcessed},
			{Path: "threads", Value: d.Threads},
			{Path: "searchTokens", Value: d.SearchTokens},
		}

		if d.Description == "" {
			updates = append(updates, firestore.Update{Path: "description", Value: firestore.Delete})
		} else {
			updates = append(updates, firestore.Update{Path: "description", Value: d.Description})
		}

		if d.Comments == "" {
			updates = append(updates, firestore.Update{Path: "comments", Value: firestore.Delete})
		} else {
			updates = append(updates, firestore.Update{Path: "comments", Value: d.Comments})
		}

		if d.Summary == "" {
			updates = append(updates, firestore.Update{Path: "summary", Value: firestore.Delete})
		} else {
			updates = append(updates, firestore.Update{Path: "summary", Value: d.Summary})
		}

		_, err := bw.Update(doc, updates)
		if err != nil {
			slog.Error("BatchWrite: Failed to queue update", "id", d.FirestoreID, "error", err)
		}
	}

	bw.Flush()
	return nil
}

// Ping checks connectivity to Firestore.
func (c *Client) Ping(ctx context.Context) error {
	iter := c.client.Collections(ctx)
	_, err := iter.Next()
	if err != nil && err != iterator.Done {
		return err
	}
	return nil
}

// GetRecentDeals fetches deals published within the given duration ago from now.
func (c *Client) GetRecentDeals(ctx context.Context, d time.Duration) ([]models.DealInfo, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

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
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultTimeout)
		defer cancel()
	}

	quota.LastUpdated = time.Now()
	_, err := c.client.Collection("bot_config").Doc("gemini_quota").Set(ctx, quota)
	if err != nil {
		return fmt.Errorf("failed to update gemini quota status: %w", err)
	}

	return nil
}
