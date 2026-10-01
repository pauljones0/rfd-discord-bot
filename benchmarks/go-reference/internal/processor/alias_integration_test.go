//go:build integration

package processor

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

func TestRetainedDuplicateAliasDoesNotReplayAfterRecentHistoryWindow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retained-alias.sqlite")
	canonical, duplicate := titleFixture(806), titleFixture(807)
	canonical.PublishedTimestamp = time.Now().Add(-49 * time.Hour)
	canonical.DocumentID = generateDealID(canonical.PublishedTimestamp)
	canonical.Threads[0].DocumentID = canonical.DocumentID
	canonical.DiscordMessageIDs = map[string]string{"channel1": "retained-receipt"}
	canonical.DiscordMessageApplicationIDs = map[string]string{"channel1": "imported-owner"}
	duplicate.PublishedTimestamp = time.Now().Add(-time.Hour)
	aliasID := generateDealID(duplicate.PublishedTimestamp)
	duplicate.Threads[0].DocumentID = aliasID
	duplicate.ActualDealURL = canonical.ActualDealURL
	canonical.Threads = append(canonical.Threads, duplicate.Threads[0])
	duplicate.Threads[0].LikeCount++
	for iteration := range 2 {
		store, err := storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			if err = store.TryCreateDeal(ctx, canonical); err != nil {
				t.Fatal(err)
			}
			if err = store.SaveSubscription(ctx, testSubscription("channel1")); err != nil {
				t.Fatal(err)
			}
		}
		recent, err := store.GetRecentDeals(ctx, 48*time.Hour)
		if err != nil || len(recent) != 0 {
			t.Fatalf("fixture must be outside fuzzy history: recent=%d err=%v", len(recent), err)
		}
		notifier := newMockNotifier()
		p := newTestProcessor(store, notifier, &mockScraper{deals: []models.DealInfo{cloneDeal(duplicate)}})
		p.aiClient = nil
		if err = p.ProcessDeals(ctx); err != nil {
			t.Fatal(err)
		}
		all, err := store.GetRecentDeals(ctx, 7*24*time.Hour)
		if err != nil || len(all) != 1 || len(notifier.sentDeals) != 0 {
			t.Fatalf("iteration %d replayed retained alias: stored=%d sent=%d err=%v", iteration, len(all), len(notifier.sentDeals), err)
		}
		stored := all[0]
		if stored.DocumentID != canonical.DocumentID || stored.Title != canonical.Title || !stored.PublishedTimestamp.Equal(canonical.PublishedTimestamp) || stored.DiscordMessageIDs["channel1"] != "retained-receipt" || stored.DiscordMessageApplicationIDs["channel1"] != "imported-owner" {
			t.Fatalf("iteration %d changed canonical identity or receipt: %+v", iteration, stored)
		}
		updatedAlias := false
		for _, thread := range stored.Threads {
			if thread.DocumentID == aliasID && thread.LikeCount == duplicate.Threads[0].LikeCount {
				updatedAlias = true
			}
		}
		if !updatedAlias {
			t.Fatalf("iteration %d lost source alias or engagement update: %+v", iteration, stored.Threads)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
