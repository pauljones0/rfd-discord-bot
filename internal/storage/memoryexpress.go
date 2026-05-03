package storage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const memexpressCollection = "memexpress_deals"

// MemExpressProductExists checks whether a clearance product already exists in Firestore.
func (c *Client) MemExpressProductExists(ctx context.Context, sku, storeCode string) (bool, error) {
	if sku == "" || storeCode == "" {
		return false, nil
	}
	if c.usesPostgres() {
		_, ok, err := c.GetRawDocument(ctx, memexpressCollection, memoryexpress.DocID(sku, storeCode))
		return ok, err
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memoryexpress.DocID(sku, storeCode)
	_, err := c.client.Collection(memexpressCollection).Doc(docID).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, fmt.Errorf("failed to check memexpress product existence: %v", err)
	}
	return true, nil
}

// GetExistingMemExpressProductIDs batch-loads known Memory Express product IDs.
func (c *Client) GetExistingMemExpressProductIDs(ctx context.Context, products []memoryexpress.Product) (map[string]struct{}, error) {
	if len(products) == 0 {
		return make(map[string]struct{}), nil
	}
	if c.usesPostgres() {
		existing := make(map[string]struct{})
		seen := make(map[string]struct{}, len(products))
		for _, product := range products {
			if product.SKU == "" || product.StoreCode == "" {
				continue
			}
			docID := memoryexpress.DocID(product.SKU, product.StoreCode)
			if _, ok := seen[docID]; ok {
				continue
			}
			seen[docID] = struct{}{}
			if _, ok, err := c.GetRawDocument(ctx, memexpressCollection, docID); err != nil {
				return nil, err
			} else if ok {
				existing[docID] = struct{}{}
			}
		}
		return existing, nil
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	refs := make([]*firestore.DocumentRef, 0, len(products))
	seen := make(map[string]struct{}, len(products))
	for _, product := range products {
		if product.SKU == "" || product.StoreCode == "" {
			continue
		}
		docID := memoryexpress.DocID(product.SKU, product.StoreCode)
		if _, ok := seen[docID]; ok {
			continue
		}
		seen[docID] = struct{}{}
		refs = append(refs, c.client.Collection(memexpressCollection).Doc(docID))
	}

	if len(refs) == 0 {
		return make(map[string]struct{}), nil
	}

	docs, err := c.client.GetAll(ctx, refs)
	if err != nil {
		return nil, fmt.Errorf("failed to batch get memexpress products: %w", err)
	}

	existing := make(map[string]struct{}, len(docs))
	for _, doc := range docs {
		if doc.Exists() {
			existing[doc.Ref.ID] = struct{}{}
		}
	}

	return existing, nil
}

// SaveMemExpressProduct saves a clearance product to Firestore.
func (c *Client) SaveMemExpressProduct(ctx context.Context, product memoryexpress.AnalyzedProduct) error {
	if c.usesPostgres() {
		return c.SetDocument(ctx, memexpressCollection, memoryexpress.DocID(product.SKU, product.StoreCode), product)
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memoryexpress.DocID(product.SKU, product.StoreCode)
	_, err := c.client.Collection(memexpressCollection).Doc(docID).Set(ctx, product)
	if err != nil {
		return fmt.Errorf("failed to save memexpress product %s: %v", docID, err)
	}
	return nil
}

// PruneMemExpressProducts deletes clearance products older than maxAgeDays or exceeding maxRecords.
func (c *Client) PruneMemExpressProducts(ctx context.Context, maxAgeDays, maxRecords int) error {
	if c.usesPostgres() {
		rows, err := c.ListDocuments(ctx, memexpressCollection)
		if err != nil {
			return err
		}
		cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
		for _, row := range rows {
			if !documentTime(row.Data, "lastSeen").IsZero() && documentTime(row.Data, "lastSeen").Before(cutoff) {
				if err := c.DeleteDocument(ctx, memexpressCollection, row.ID); err != nil {
					slog.Warn("Failed to delete old memexpress product", "processor", "memoryexpress", "id", row.ID, "error", err)
				}
			}
		}
		rows, err = c.ListDocuments(ctx, memexpressCollection)
		if err != nil {
			return err
		}
		if len(rows) > maxRecords {
			sortDocumentsByTime(rows, "lastSeen", true)
			for _, row := range rows[:len(rows)-maxRecords] {
				if err := c.DeleteDocument(ctx, memexpressCollection, row.ID); err != nil {
					slog.Warn("Failed to delete excess memexpress record", "processor", "memoryexpress", "id", row.ID, "error", err)
				}
			}
		}
		return nil
	}

	ctx, cancel := ensureDeadline(ctx, 2*time.Minute)
	defer cancel()

	// Delete by age
	cutoff := time.Now().AddDate(0, 0, -maxAgeDays)
	iter := c.client.Collection(memexpressCollection).Where("lastSeen", "<", cutoff).Documents(ctx)
	defer iter.Stop()

	bw := c.client.BulkWriter(ctx)
	defer func() {
		bw.Flush()
		bw.End()
	}()
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return err
		}
		if _, err := bw.Delete(doc.Ref); err != nil {
			slog.Warn("Failed to queue old memexpress product delete", "processor", "memoryexpress", "id", doc.Ref.ID, "error", err)
		}
	}

	countSnapshot, err := c.client.Collection(memexpressCollection).NewAggregationQuery().WithCount("all").Get(ctx)
	if err != nil {
		return fmt.Errorf("failed to count memexpress records: %w", err)
	}

	countValue, ok := countSnapshot["all"]
	if !ok {
		return fmt.Errorf("count aggregation result for memexpress was invalid: 'all' key missing")
	}

	var totalCount int64
	switch val := countValue.(type) {
	case int64:
		totalCount = val
	case *firestorepb.Value:
		totalCount = val.GetIntegerValue()
	default:
		return fmt.Errorf("count aggregation result has unexpected type %T", countValue)
	}

	if int(totalCount) > maxRecords {
		excess := int(totalCount) - maxRecords
		deleteIter := c.client.Collection(memexpressCollection).OrderBy("lastSeen", firestore.Asc).Limit(excess).Documents(ctx)
		defer deleteIter.Stop()
		for {
			doc, err := deleteIter.Next()
			if err == iterator.Done {
				break
			}
			if err != nil {
				return fmt.Errorf("failed to iterate excess memexpress records: %v", err)
			}
			if _, err := bw.Delete(doc.Ref); err != nil {
				slog.Warn("Failed to queue excess memexpress record delete", "processor", "memoryexpress", "id", doc.Ref.ID, "error", err)
			}
		}
	}

	return nil
}

// memExpressSubscriptionDocID generates a unique document ID for a Memory Express subscription.
func memExpressSubscriptionDocID(guildID, channelID, storeCode string) string {
	return fmt.Sprintf("%s_%s_memoryexpress_%s", guildID, channelID, storeCode)
}

// SaveMemExpressSubscription creates or updates a Memory Express subscription in Firestore.
func (c *Client) SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error {
	if c.usesPostgres() {
		return c.SetDocument(ctx, subscriptionsCollection, memExpressSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.StoreCode), sub)
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressSubscriptionDocID(sub.GuildID, sub.ChannelID, sub.StoreCode)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Set(ctx, sub)
	if err != nil {
		return fmt.Errorf("failed to save memexpress subscription %s: %w", docID, err)
	}
	return nil
}

// RemoveMemExpressSubscription removes a Memory Express subscription by guild, channel, and store.
func (c *Client) RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error {
	if c.usesPostgres() {
		return c.DeleteDocument(ctx, subscriptionsCollection, memExpressSubscriptionDocID(guildID, channelID, storeCode))
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	docID := memExpressSubscriptionDocID(guildID, channelID, storeCode)
	_, err := c.client.Collection(subscriptionsCollection).Doc(docID).Delete(ctx)
	if err != nil {
		return fmt.Errorf("failed to remove memexpress subscription %s: %w", docID, err)
	}
	return nil
}

// GetMemExpressSubscriptions retrieves all active Memory Express subscriptions.
func (c *Client) GetMemExpressSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	if c.usesPostgres() {
		return c.subscriptionsWhere(ctx, func(row Document) bool {
			return documentString(row.Data, "subscriptionType") == "memoryexpress"
		})
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("subscriptionType", "==", "memoryexpress").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate memexpress subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal memexpress subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}

// GetMemExpressSubscriptionsByGuild retrieves all Memory Express subscriptions for a guild.
func (c *Client) GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if c.usesPostgres() {
		return c.subscriptionsWhere(ctx, func(row Document) bool {
			return documentString(row.Data, "guildID") == guildID &&
				documentString(row.Data, "subscriptionType") == "memoryexpress"
		})
	}

	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	iter := c.client.Collection(subscriptionsCollection).
		Where("guildID", "==", guildID).
		Where("subscriptionType", "==", "memoryexpress").
		Documents(ctx)
	defer iter.Stop()

	var subs []models.Subscription
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate guild memexpress subscriptions: %w", err)
		}
		var sub models.Subscription
		if err := doc.DataTo(&sub); err != nil {
			return nil, fmt.Errorf("failed to unmarshal guild memexpress subscription: %w", err)
		}
		subs = append(subs, sub)
	}
	return subs, nil
}
