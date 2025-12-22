package storage

import (
	"context"
	"fmt"
	"log"
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

	if !doc.Exists() {
		return nil, nil
	}

	var deal models.DealInfo
	if err := doc.DataTo(&deal); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deal data: %w", err)
	}
	deal.FirestoreID = doc.Ref.ID
	return &deal, nil
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
			return fmt.Errorf("deal already exists")
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

	log.Printf("TrimOldDeals: Entered function with maxDeals = %d", maxDeals)
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
	pbValue, okAssert := countValue.(*firestorepb.Value)
	if !okAssert {
		return fmt.Errorf("count aggregation result for trimming has unexpected type %T", countValue)
	}
	currentDealCountInt64 = pbValue.GetIntegerValue()

	currentDealCount := int(currentDealCountInt64)

	if currentDealCount <= maxDeals {
		return nil
	}

	numToDelete := currentDealCount - maxDeals
	log.Printf("TrimOldDeals: Trimming needed. Current: %d, Max: %d. Deleting: %d.", currentDealCount, maxDeals, numToDelete)

	// Query for the oldest deals to delete
	iter := collectionRef.
		OrderBy("publishedTimestamp", firestore.Asc). // Ascending to get oldest first
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	deletedCount := 0
	bulkWriter := c.client.BulkWriter(ctx)

	// Ensure we close the bulk writer properly
	defer func() {
		// End doesn't return an error in this SDK version, or the signature is different.
		// Checking the docs or source would confirm, but usually it returns void or error.
		// If the compiler says "no value used as value", it means End() returns nothing.
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
			log.Printf("TrimOldDeals: Error queueing delete for ID %s: %v", doc.Ref.ID, delErr)
		}
		deletedCount++
	}

	if deletedCount > 0 {
		bulkWriter.Flush()
		log.Printf("TrimOldDeals: Flushed %d delete operations.", deletedCount)
	}

	return nil
}
