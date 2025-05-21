package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	firestoreCollection = "bot_state"
	firestoreDocumentID = "last_processed_deal"
)

// BotState represents the data stored in Firestore.
type BotState struct {
	LastProcessedTimestamp time.Time `firestore:"lastProcessedTimestamp"`
}

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

// readLastProcessedTimestamp reads the last processed deal's timestamp from Firestore.
// If the document doesn't exist (first run), it returns a zero time.Time.
func readLastProcessedTimestamp(ctx context.Context, client *firestore.Client) (time.Time, error) {
	docRef := client.Collection(firestoreCollection).Doc(firestoreDocumentID)
	docSnap, err := docRef.Get(ctx)

	if err != nil {
		if status.Code(err) == codes.NotFound {
			log.Printf("Firestore document %s/%s not found. Assuming first run.", firestoreCollection, firestoreDocumentID)
			return time.Time{}, nil // Return zero time if document not found (first run)
		}
		return time.Time{}, fmt.Errorf("failed to get document from Firestore: %w", err)
	}

	var state BotState
	if err := docSnap.DataTo(&state); err != nil {
		return time.Time{}, fmt.Errorf("failed to unmarshal Firestore document data: %w", err)
	}

	log.Printf("Successfully read last processed timestamp from Firestore: %s", state.LastProcessedTimestamp.Format(time.RFC3339))
	return state.LastProcessedTimestamp, nil
}

// writeLastProcessedTimestamp writes/updates the latest processed deal's timestamp to Firestore.
func writeLastProcessedTimestamp(ctx context.Context, client *firestore.Client, timestamp time.Time) error {
	if timestamp.IsZero() {
		log.Println("Skipping Firestore write: timestamp is zero.")
		return nil // Avoid writing a zero timestamp if not intended
	}

	docRef := client.Collection(firestoreCollection).Doc(firestoreDocumentID)
	state := BotState{LastProcessedTimestamp: timestamp}

	_, err := docRef.Set(ctx, state)
	if err != nil {
		return fmt.Errorf("failed to set document in Firestore: %w", err)
	}
	log.Printf("Successfully wrote last processed timestamp to Firestore: %s", timestamp.Format(time.RFC3339))
	return nil
}