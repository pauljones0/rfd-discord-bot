package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"cloud.google.com/go/firestore/apiv1/firestorepb" // Added import
	"google.golang.org/api/iterator"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	firestoreCollection = "deals" // Changed from "bot_state"
	// firestoreDocumentID is no longer needed as we work with a collection.
)

// initFirestoreClient initializes and returns a Firestore client.
// It reads the GOOGLE_CLOUD_PROJECT ID from an environment variable.
func initFirestoreClient(ctx context.Context) (*firestore.Client, error) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		// Fallback for local testing if GOOGLE_CLOUD_PROJECT is not set.
		// Consider making this configurable or removing for production.
		log.Println("Warning: GOOGLE_CLOUD_PROJECT environment variable not set. Attempting to use a default project ID for local testing (this might fail).")
		// You might need to set a default project ID here if you have one for local dev,
		// or ensure the emulator is running and configured if projectID is truly empty.
		// For now, let's proceed, Firestore client might infer it if running in GCP environment or with gcloud auth.
	}

	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("firestore.NewClient: %w", err)
	}
	return client, nil
}

// WriteDealInfo writes a DealInfo object to the "deals" collection.
// If deal.FirestoreID is empty, it's a new deal; adds it and returns the new document ID.
// If deal.FirestoreID is not empty, it updates the existing document.
// Sets/updates the LastUpdated field to time.Now() before writing.
func WriteDealInfo(ctx context.Context, client *firestore.Client, deal DealInfo) (string, error) {
	deal.LastUpdated = time.Now()
	collectionRef := client.Collection(firestoreCollection)

	var docRef *firestore.DocumentRef
	var err error

	// If FirestoreID is provided, use it as the document ID.
	// This supports deterministic IDs.
	if deal.FirestoreID != "" {
		docRef = collectionRef.Doc(deal.FirestoreID)
	} else {
		// Fallback to auto-generated ID if none provided (legacy behavior, discourage use)
		docRef = collectionRef.NewDoc()
	}

	// Use Set with MergeAll is safer for updates, but for full overwrites Set is fine.
	// Here we want to ensure we write the struct as is.
	_, err = docRef.Set(ctx, deal)
	if err != nil {
		log.Printf("Error writing deal to Firestore (ID: %s): %v. Deal: %+v", docRef.ID, err, deal)
		return "", fmt.Errorf("failed to write deal to Firestore (ID: %s): %w", docRef.ID, err)
	}
	log.Printf("Successfully wrote deal to Firestore with ID %s. Title: %s", docRef.ID, deal.Title)

	// We only trim if we think this is a new addition, but checking that is hard with upsert.
	// We can just run trim occasionally or let the scheduler handle it.
	// For safety, we can leave it here, or move it to the handler.
	// To avoid excessive reads, we might want to move it, but keeping it ensures bounds.
	if trimErr := TrimOldDeals(ctx, client, 50); trimErr != nil { // Increased limit slightly
		log.Printf("Error trimming old deals: %v", trimErr)
	}

	return docRef.ID, nil
}

// GetDealByID retrieves a deal by its Firestore Document ID.
func GetDealByID(ctx context.Context, client *firestore.Client, id string) (*DealInfo, error) {
	docRef := client.Collection(firestoreCollection).Doc(id)
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

	var deal DealInfo
	if err := doc.DataTo(&deal); err != nil {
		return nil, fmt.Errorf("failed to unmarshal deal data: %w", err)
	}
	deal.FirestoreID = doc.Ref.ID
	return &deal, nil
}

// TryCreateDeal attempts to create a new deal. Returns error if it already exists.
// This is used to safely claim a new deal and prevent race conditions.
func TryCreateDeal(ctx context.Context, client *firestore.Client, deal DealInfo) error {
	collectionRef := client.Collection(firestoreCollection)
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

// UpdateDeal updates a specific deal.
func UpdateDeal(ctx context.Context, client *firestore.Client, deal DealInfo) error {
	collectionRef := client.Collection(firestoreCollection)
	docRef := collectionRef.Doc(deal.FirestoreID)
	// Set with default options overwrites. This is fine as we pass the full struct.
	// For partial updates we would use Update, but here we want to sync the full state.
	_, err := docRef.Set(ctx, deal)
	return err
}

// ReadRecentDeals queries the "deals" collection, orders by PublishedTimestamp descending,
// and limits to the specified 'limit'. Populates FirestoreID in each returned DealInfo.
func ReadRecentDeals(ctx context.Context, client *firestore.Client, limit int) ([]DealInfo, error) {
	var deals []DealInfo
	iter := client.Collection(firestoreCollection).
		OrderBy("publishedTimestamp", firestore.Desc).
		Limit(limit).
		Documents(ctx)
	defer iter.Stop()

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Printf("Error iterating recent deals from Firestore: %v", err)
			return nil, fmt.Errorf("failed to iterate recent deals: %w", err)
		}

		var deal DealInfo
		if err := doc.DataTo(&deal); err != nil {
			log.Printf("Error unmarshaling deal data from Firestore (ID: %s): %v", doc.Ref.ID, err)
			// Skip this deal and continue
			continue
		}
		deal.FirestoreID = doc.Ref.ID // Populate FirestoreID
		deals = append(deals, deal)
	}
	log.Printf("Successfully read %d recent deals from Firestore.", len(deals))
	return deals, nil
}

// GetDealByPublishedTimestamp queries for a deal with a matching PublishedTimestamp.
// Returns the DealInfo (with FirestoreID populated) or nil, nil if not found.
func GetDealByPublishedTimestamp(ctx context.Context, client *firestore.Client, publishedTimestamp time.Time) (*DealInfo, error) {
	iter := client.Collection(firestoreCollection).
		Where("publishedTimestamp", "==", publishedTimestamp).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()

	doc, err := iter.Next()
	if err == iterator.Done {
		log.Printf("No deal found in Firestore with PublishedTimestamp: %s", publishedTimestamp.String())
		return nil, nil // Not found
	}
	if err != nil {
		log.Printf("Error querying deal by PublishedTimestamp from Firestore (Timestamp: %s): %v", publishedTimestamp.String(), err)
		return nil, fmt.Errorf("failed to query deal by PublishedTimestamp (Timestamp: %s): %w", publishedTimestamp.String(), err)
	}

	var deal DealInfo
	if err := doc.DataTo(&deal); err != nil {
		log.Printf("Error unmarshaling deal data from Firestore (ID: %s, PublishedTimestamp: %s): %v", doc.Ref.ID, publishedTimestamp.String(), err)
		return nil, fmt.Errorf("failed to unmarshal deal data (ID: %s, PublishedTimestamp: %s): %w", doc.Ref.ID, publishedTimestamp.String(), err)
	}
	deal.FirestoreID = doc.Ref.ID // Populate FirestoreID
	log.Printf("Successfully found deal by PublishedTimestamp: %s (ID: %s, Title: %s)", publishedTimestamp.String(), deal.FirestoreID, deal.Title)
	return &deal, nil
}

// TrimOldDeals deletes the oldest deals (by PublishedTimestamp) from the "deals" collection
// if the total number of deals exceeds maxDeals.
func TrimOldDeals(ctx context.Context, client *firestore.Client, maxDeals int) error {
	log.Printf("TrimOldDeals: Entered function with maxDeals = %d", maxDeals)
	collectionRef := client.Collection(firestoreCollection)

	// Get current count
	log.Printf("TrimOldDeals: Attempting to get deal count aggregation.")
	countSnapshot, err := collectionRef.NewAggregationQuery().WithCount("all").Get(ctx)
	if err != nil {
		log.Printf("TrimOldDeals: Error getting count of deals: %v", err)
		return fmt.Errorf("failed to get deal count for trimming: %w", err)
	}
	log.Printf("TrimOldDeals: Deal count aggregation result: %+v", countSnapshot)

	countValue, ok := countSnapshot["all"]
	if !ok {
		log.Printf("TrimOldDeals: Error - 'all' key not found in count aggregation result. Snapshot: %+v", countSnapshot)
		return fmt.Errorf("count aggregation result for trimming was invalid: 'all' key missing")
	}
	log.Printf("TrimOldDeals: Extracted countValue from snapshot: %v (type: %T)", countValue, countValue)

	var currentDealCountInt64 int64
	pbValue, okAssert := countValue.(*firestorepb.Value)
	if !okAssert {
		log.Printf("TrimOldDeals: Error - countValue is not of type *firestorepb.Value. Actual type: %T, Value: %v", countValue, countValue)
		return fmt.Errorf("count aggregation result for trimming has unexpected type %T, expected *firestorepb.Value", countValue)
	}
	currentDealCountInt64 = pbValue.GetIntegerValue()
	log.Printf("TrimOldDeals: Asserted countValue to *firestorepb.Value and got integer value: %d", currentDealCountInt64)

	currentDealCount := int(currentDealCountInt64)
	log.Printf("TrimOldDeals: Calculated currentDealCount as %d", currentDealCount)

	if currentDealCount <= maxDeals {
		log.Printf("TrimOldDeals: No trimming needed. Current deals: %d, Max deals: %d. Exiting.", currentDealCount, maxDeals)
		return nil
	}

	numToDelete := currentDealCount - maxDeals
	log.Printf("TrimOldDeals: Trimming needed. Current deals: %d, Max deals: %d. Calculated numToDelete: %d.", currentDealCount, maxDeals, numToDelete)

	// Query for the oldest deals to delete
	log.Printf("TrimOldDeals: Querying for %d oldest deals to delete.", numToDelete)
	iter := collectionRef.
		OrderBy("publishedTimestamp", firestore.Asc). // Ascending to get oldest first
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	deletedCount := 0
	bulkWriter := client.BulkWriter(ctx)
	// Defer End() to ensure it's called. According to Go SDK, End() does not return an error.
	defer bulkWriter.End()

	log.Printf("TrimOldDeals: Starting iteration to mark deals for deletion using BulkWriter.")
	for {
		doc, err := iter.Next() // Changed iterErr back to err for consistency
		if err == iterator.Done {
			log.Printf("TrimOldDeals: Finished iterating through deals to delete.")
			break
		}
		if err != nil {
			log.Printf("TrimOldDeals: Error iterating deals to delete: %v", err)
			// bulkWriter.End() will be called by defer.
			// No need to explicitly call bulkWriter.End() here as it was in the original pre-edit code.
			return fmt.Errorf("failed to iterate deals for trimming: %w", err)
		}

		// Extract publishedTimestamp safely
		var publishedTimestamp interface{}
		data := doc.Data()
		if ts, exists := data["publishedTimestamp"]; exists {
			publishedTimestamp = ts
		} else {
			publishedTimestamp = "N/A" // Or handle as an error/default
		}

		// Attempt to delete the document using BulkWriter.
		// BulkWriter.Delete() returns an error, which we log.
		// BulkWriter.Flush() and BulkWriter.End() do not return errors in this SDK.
		_, delErr := bulkWriter.Delete(doc.Ref)
		if delErr != nil {
			// Log the error from the Delete call itself.
			log.Printf("TrimOldDeals: Error during BulkWriter.Delete for ID %s: %v. Continuing to queue other operations.", doc.Ref.ID, delErr)
			// Continue, as per original logic, to attempt other deletes.
		}
		deletedCount++
		log.Printf("TrimOldDeals: Queued deal for deletion with BulkWriter. ID: %s, PublishedTimestamp: %v. Total queued: %d", doc.Ref.ID, publishedTimestamp, deletedCount)
	}

	// Operations are queued. Now flush them.
	// Flush() does not return an error.
	if deletedCount > 0 {
		log.Printf("TrimOldDeals: Attempting to flush BulkWriter operations for %d deals.", deletedCount)
		bulkWriter.Flush()
		log.Printf("TrimOldDeals: BulkWriter flush initiated for %d delete operations. Individual errors during Delete calls were logged if any.", deletedCount)
	} else {
		log.Printf("TrimOldDeals: No deals were queued for deletion. numToDelete was %d.", numToDelete)
	}

	// End() will be called by defer. It does not return an error.
	log.Printf("TrimOldDeals: Exiting function. BulkWriter.End() will be called by defer.")
	return nil
}
