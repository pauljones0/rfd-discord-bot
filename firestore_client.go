package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	firestorepb "google.golang.org/genproto/googleapis/firestore/v1"
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

// GetDealByPostURL queries for a deal with a matching PostURL.
// Returns the DealInfo (with FirestoreID populated) or nil, nil if not found.
func GetDealByPostURL(ctx context.Context, client *firestore.Client, postURL string) (*DealInfo, error) {
	iter := client.Collection(firestoreCollection).
		Where("postURL", "==", postURL).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()

	doc, err := iter.Next()
	if err == iterator.Done {
		log.Printf("No deal found in Firestore with PostURL: %s", postURL)
		return nil, nil // Not found
	}
	if err != nil {
		log.Printf("Error querying deal by PostURL from Firestore (URL: %s): %v", postURL, err)
		return nil, fmt.Errorf("failed to query deal by PostURL (URL: %s): %w", postURL, err)
	}

	var deal DealInfo
	if err := doc.DataTo(&deal); err != nil {
		log.Printf("Error unmarshaling deal data from Firestore (ID: %s, PostURL: %s): %v", doc.Ref.ID, postURL, err)
		return nil, fmt.Errorf("failed to unmarshal deal data (ID: %s, PostURL: %s): %w", doc.Ref.ID, postURL, err)
	}
	deal.FirestoreID = doc.Ref.ID // Populate FirestoreID
	log.Printf("Successfully found deal by PostURL: %s (ID: %s, Title: %s)", postURL, deal.FirestoreID, deal.Title)
	return &deal, nil
}

// TrimOldDeals deletes the oldest deals (by PublishedTimestamp) from the "deals" collection
// if the total number of deals exceeds maxDeals.
func TrimOldDeals(ctx context.Context, client *firestore.Client, maxDeals int) error {
	collectionRef := client.Collection(firestoreCollection)

	// Get current count
	countSnapshot, err := collectionRef.NewAggregationQuery().WithCount("all").Get(ctx)
	if err != nil {
		log.Printf("Error getting count of deals for trimming: %v", err)
		return fmt.Errorf("failed to get deal count for trimming: %w", err)
	}
	count, ok := countSnapshot["all"]
	if !ok || count == nil {
		log.Printf("Error: 'all' key not found or nil in count aggregation result for trimming.")
		return fmt.Errorf("count aggregation result for trimming was invalid")
	}
	valuePb, okAssert := count.(*firestorepb.Value)
	if !okAssert {
		log.Printf("Error: count is not of type *firestorepb.Value for trimming. Actual type: %T", count)
		return fmt.Errorf("count aggregation result for trimming has unexpected type: %T", count)
	}
	currentDealCount := int(valuePb.GetIntegerValue())

	if currentDealCount <= maxDeals {
		log.Printf("No trimming needed. Current deals: %d, Max deals: %d", currentDealCount, maxDeals)
		return nil
	}

	numToDelete := currentDealCount - maxDeals
	log.Printf("Trimming needed. Current deals: %d, Max deals: %d. Will delete %d oldest deals.", currentDealCount, maxDeals, numToDelete)

	// Query for the oldest deals to delete
	iter := collectionRef.
		OrderBy("publishedTimestamp", firestore.Asc). // Ascending to get oldest first
		Limit(numToDelete).
		Documents(ctx)
	defer iter.Stop()

	deletedCount := 0
	batch := client.Batch()
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Printf("Error iterating deals to delete for trimming: %v", err)
			return fmt.Errorf("failed to iterate deals for trimming: %w", err)
		}
		batch.Delete(doc.Ref)
		deletedCount++
		log.Printf("Marked deal for deletion (trimming): ID %s, Published: %s", doc.Ref.ID, doc.Data()["publishedTimestamp"])
	}

	if deletedCount > 0 {
		_, err := batch.Commit(ctx)
		if err != nil {
			log.Printf("Error committing batch delete for trimming: %v", err)
			return fmt.Errorf("failed to commit batch delete for trimming: %w", err)
		}
		log.Printf("Successfully trimmed %d old deals from Firestore.", deletedCount)
	} else {
		log.Println("No deals were marked for deletion during trimming (this might be unexpected if numToDelete > 0).")
	}

	return nil
}
