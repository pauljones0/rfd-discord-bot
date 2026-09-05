package storage

import (
	"context"
	"github.com/pauljones0/rfd-discord-standalone/internal/models"
	"path/filepath"
	"testing"
	"time"
)

func TestPersistenceAndAtomicBatch(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rfd.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	deal := models.DealInfo{DocumentID: "one", Title: "Example", PublishedTimestamp: now, LastUpdated: now, DiscordMessageIDs: map[string]string{"channel": "receipt"}}
	if err = s.TryCreateDeal(ctx, deal); err != nil {
		t.Fatal(err)
	}
	if err = s.TryCreateDeal(ctx, deal); err != models.ErrDealExists {
		t.Fatalf("duplicate create: %v", err)
	}
	other := deal
	other.DocumentID = "two"
	if err = s.BatchWrite(ctx, []models.DealInfo{other, deal}, nil); err == nil {
		t.Fatal("expected duplicate to roll back entire batch")
	}
	if found, e := s.GetDealByID(ctx, "two"); e != nil || found != nil {
		t.Fatalf("partial batch was committed: %v %v", found, e)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.GetDealByID(ctx, "one")
	if err != nil || got == nil || got.DiscordMessageIDs["channel"] != "receipt" {
		t.Fatalf("lost notification receipt across restart: %v %v", got, err)
	}
	recent, err := s.GetRecentDeals(ctx, time.Hour)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent query: %v %v", recent, err)
	}
}

func TestSubscriptionsAreScopedToGuild(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, guild := range []string{"one", "two"} {
		sub := models.Subscription{GuildID: guild, ChannelID: "channel", DealType: "rfd_all"}
		for range 2 {
			if err = s.SaveSubscription(ctx, sub); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = s.RemoveSubscription(ctx, "one", "channel", ""); err != nil {
		t.Fatal(err)
	}
	first, err := s.GetSubscriptionsByGuild(ctx, "one")
	if err != nil || len(first) != 0 {
		t.Fatalf("removed guild: %v %v", first, err)
	}
	second, err := s.GetSubscriptionsByGuild(ctx, "two")
	if err != nil || len(second) != 1 {
		t.Fatalf("other guild changed or duplicates persisted: %v %v", second, err)
	}
	if err = s.SaveSubscription(ctx, models.Subscription{GuildID: "one", ChannelID: "channel", DealType: "ebay_ca_price_drop"}); err == nil {
		t.Fatal("accepted a different bot's subscription")
	}
}
