package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

var legacyDealTypes = map[string]bool{
	"ebay_price_drop": true,
	"ebay_warm_hot":   true,
	"ebay_hot":        true,
	"warm_hot_all":    true,
	"hot_all":         true,
}

func main() {
	execute := flag.Bool("execute", false, "delete legacy subscription documents instead of only reporting them")
	flag.Parse()

	if _, err := config.Load(); err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx := context.Background()
	store, err := storage.New(ctx)
	if err != nil {
		log.Fatalf("init storage: %v", err)
	}
	defer store.Close()

	rows, err := store.ListDocuments(ctx, "subscriptions")
	if err != nil {
		log.Fatalf("list subscriptions: %v", err)
	}

	found := 0
	for _, row := range rows {
		dealType := strings.TrimSpace(fmt.Sprint(row.Data["dealType"]))
		if !legacyDealTypes[dealType] {
			continue
		}
		found++
		fmt.Printf("%s dealType=%s guild=%v channel=%v\n", row.ID, dealType, row.Data["guildID"], row.Data["channelID"])
		if *execute {
			if err := store.DeleteDocument(ctx, "subscriptions", row.ID); err != nil {
				log.Fatalf("delete %s: %v", row.ID, err)
			}
		}
	}

	if found == 0 {
		fmt.Println("No legacy subscription records found.")
	} else if *execute {
		fmt.Printf("Deleted %d legacy subscription records.\n", found)
	} else {
		fmt.Printf("Found %d legacy subscription records. Re-run with -execute to delete them.\n", found)
		os.Exit(2)
	}
}
