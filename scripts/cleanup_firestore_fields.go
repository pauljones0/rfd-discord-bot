//go:build ignore

// cleanup_firestore_fields removes stale isWarm and isLavaHot fields
// from all documents in the "deals" Firestore collection.
//
// Usage: go run scripts/cleanup_firestore_fields.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

func main() {
	ctx := context.Background()
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT environment variable is required")
	}

	client, err := firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create Firestore client: %v", err)
	}
	defer client.Close()

	collection := client.Collection("deals")
	bulkWriter := client.BulkWriter(ctx)

	iter := collection.Documents(ctx)
	var updated int
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Fatalf("Error iterating documents: %v", err)
		}

		data := doc.Data()
		_, hasWarm := data["isWarm"]
		_, hasHot := data["isLavaHot"]

		if hasWarm || hasHot {
			_, err := bulkWriter.Update(doc.Ref, []firestore.Update{
				{Path: "isWarm", Value: firestore.Delete},
				{Path: "isLavaHot", Value: firestore.Delete},
			})
			if err != nil {
				log.Printf("Failed to queue update for %s: %v", doc.Ref.ID, err)
				continue
			}
			updated++
		}
	}

	bulkWriter.End()
	fmt.Printf("Cleaned %d documents\n", updated)
}
