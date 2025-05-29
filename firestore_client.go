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

	if deal.FirestoreID == "" {
		// New deal, add to Firestore
		docRef = collectionRef.NewDoc() // Firestore generates ID
		_, err = docRef.Set(ctx, deal)
		if err != nil {
			log.Printf("Error adding new deal to Firestore: %v. Deal: %+v", err, deal)
			return "", fmt.Errorf("failed to add new deal to Firestore: %w", err)
		}
		log.Printf("Successfully added new deal to Firestore with ID %s. Title: %s", docRef.ID, deal.Title)
		// After adding, trim old deals if necessary
		if trimErr := TrimOldDeals(ctx, client, 28); trimErr != nil {
			log.Printf("Error trimming old deals after adding new one: %v", trimErr)
			// Continue, as the main operation (adding deal) was successful
		}
		return docRef.ID, nil
	}

	// Existing deal, update
	docRef = collectionRef.Doc(deal.FirestoreID)
	_, err = docRef.Set(ctx, deal) // Set will overwrite or create if not exists (though ID implies exists)
	if err != nil {
		log.Printf("Error updating deal in Firestore with ID %s: %v. Deal: %+v", deal.FirestoreID, err, deal)
		return "", fmt.Errorf("failed to update deal in Firestore (ID: %s): %w", deal.FirestoreID, err)
	}
	log.Printf("Successfully updated deal in Firestore with ID %s. Title: %s", deal.FirestoreID, deal.Title)
	return deal.FirestoreID, nil
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
