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

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const firestoreCollection = "deals"

// DefaultTimeout is the default duration for Firestore operations if the context has no deadline.
const DefaultTimeout = 30 * time.Second

var ErrDealExists = errors.New("deal already exists")

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
			return ErrDealExists
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

	_, err := docRef.Update(ctx, []firestore.Update{
		{Path: "title", Value: deal.Title},
		{Path: "postURL", Value: deal.PostURL},
		{Path: "authorName", Value: deal.AuthorName},
		{Path: "authorURL", Value: deal.AuthorURL},
		{Path: "threadImageURL", Value: deal.ThreadImageURL},
		{Path: "likeCount", Value: deal.LikeCount},
		{Path: "commentCount", Value: deal.CommentCount},
		{Path: "viewCount", Value: deal.ViewCount},
		{Path: "actualDealURL", Value: deal.ActualDealURL},
		{Path: "lastUpdated", Value: deal.LastUpdated},
		{Path: "discordMessageID", Value: deal.DiscordMessageID},
		{Path: "discordLastUpdatedTime", Value: deal.DiscordLastUpdatedTime},
		{Path: "publishedTimestamp", Value: deal.PublishedTimestamp},
	})
	return err
}

// TrimOldDeals deletes the oldest deals (by PublishedTimestamp) from the "deals" collection
func (c *Client) TrimOldDeals(ctx context.Context, maxDeals int) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute) // Longer timeout for cleanup
		defer cancel()
	}

	slog.Info("TrimOldDeals: Entered function", "maxDeals", maxDeals)
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
		OrderBy("publishedTimestamp", firestore.Asc). // Ascending to get oldest first
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	deletedCount := 0
	bulkWriter := c.client.BulkWriter(ctx)

	defer func() {
		bulkWriter.End()
	}()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to iterate deals for trimming: %w", err)
		}

		_, delErr := bulkWriter.Delete(doc.Ref)
		if delErr != nil {
			slog.Error("TrimOldDeals: Error queueing delete", "id", doc.Ref.ID, "error", delErr)
			continue
		}
		deletedCount++
	}

	if deletedCount > 0 {
		bulkWriter.Flush()
		slog.Info("TrimOldDeals: Flushed delete operations", "count", deletedCount)
	}

	return nil
}
