package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	"github.com/joho/godotenv"
	"google.golang.org/api/iterator"

	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

func main() {
	_ = godotenv.Load()

	var (
		projectID      = flag.String("project", os.Getenv("GOOGLE_CLOUD_PROJECT"), "GCP project ID for Firestore source")
		databaseURL    = flag.String("database-url", os.Getenv("DATABASE_URL"), "Postgres DATABASE_URL destination")
		verifyOnly     = flag.Bool("verify-only", false, "verify counts without writing")
		sampleCheck    = flag.Int("sample-check", 3, "number of docs per collection to sample-compare")
		collectionsArg = flag.String("collections", "", "comma-separated Firestore collections to migrate; defaults to known bot collections")
	)
	flag.Parse()

	if *projectID == "" {
		log.Fatal("GOOGLE_CLOUD_PROJECT or -project is required")
	}
	if *databaseURL == "" {
		log.Fatal("DATABASE_URL or -database-url is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	fs, err := firestore.NewClient(ctx, *projectID)
	if err != nil {
		log.Fatalf("firestore.NewClient: %v", err)
	}
	defer fs.Close()

	pg, err := storage.NewPostgres(ctx, *databaseURL)
	if err != nil {
		log.Fatalf("storage.NewPostgres: %v", err)
	}
	defer pg.Close()

	collections := parseCollections(*collectionsArg)
	if len(collections) == 0 {
		var err error
		collections, err = listCollections(ctx, fs)
		if err != nil {
			log.Printf("list collections failed (%v); falling back to known bot collections", err)
			collections = defaultCollections()
		}
	}
	sort.Strings(collections)

	type collectionResult struct {
		Collection string `json:"collection"`
		Firestore  int    `json:"firestore"`
		Postgres   int    `json:"postgres"`
		SamplesOK  int    `json:"samplesOK"`
	}
	var results []collectionResult
	for _, collection := range collections {
		count, sampleOK, err := migrateCollection(ctx, fs, pg, collection, *verifyOnly, *sampleCheck)
		if err != nil {
			log.Fatalf("migrate %s: %v", collection, err)
		}
		pgDocs, err := pg.ListDocuments(ctx, collection)
		if err != nil {
			log.Fatalf("count postgres %s: %v", collection, err)
		}
		results = append(results, collectionResult{
			Collection: collection,
			Firestore:  count,
			Postgres:   len(pgDocs),
			SamplesOK:  sampleOK,
		})
		log.Printf("%s: firestore=%d postgres=%d samples_ok=%d", collection, count, len(pgDocs), sampleOK)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		log.Fatal(err)
	}
}

func parseCollections(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func defaultCollections() []string {
	return []string{
		"deals",
		"subscriptions",
		"bot_config",
		"ebay_sellers",
		"ebay_items",
		"ebay_store_coupons",
		"memexpress_deals",
		"bestbuy_sellers",
		"bestbuy_deals",
		"car_deals",
		"price_history",
		"carfax_cache",
		"carfax_options",
		"blocked_proxy_ips",
		"hw_servers",
		"hw_alerts",
		"hw_posts",
		"hw_analytics",
		"hw_system_prompts",
	}
}

func listCollections(ctx context.Context, client *firestore.Client) ([]string, error) {
	iter := client.Collections(ctx)
	var names []string
	for {
		col, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		names = append(names, col.ID)
	}
	return names, nil
}

func migrateCollection(ctx context.Context, fs *firestore.Client, pg *storage.Client, collection string, verifyOnly bool, sampleLimit int) (int, int, error) {
	iter := fs.Collection(collection).Documents(ctx)
	defer iter.Stop()

	count := 0
	samplesOK := 0
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return count, samplesOK, err
		}
		data := doc.Data()
		if !verifyOnly {
			if err := pg.SetRawDocument(ctx, collection, doc.Ref.ID, data); err != nil {
				return count, samplesOK, err
			}
		}
		count++
		if sampleLimit <= 0 || samplesOK >= sampleLimit {
			continue
		}
		pgDoc, ok, err := pg.GetRawDocument(ctx, collection, doc.Ref.ID)
		if err != nil {
			return count, samplesOK, err
		}
		if !ok {
			if verifyOnly {
				continue
			}
			return count, samplesOK, fmt.Errorf("missing postgres sample %s/%s", collection, doc.Ref.ID)
		}
		if len(pgDoc.Data) > 0 {
			samplesOK++
		}
	}
	return count, samplesOK, nil
}
