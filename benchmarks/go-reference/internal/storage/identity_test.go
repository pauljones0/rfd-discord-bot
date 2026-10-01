package storage

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestDealReadsRejectInconsistentIdentity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := models.DealInfo{DocumentID: "wrong", Title: "Example", PublishedTimestamp: time.Now()}
	payload, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO deals(id,payload,published_at,updated_at) VALUES(?,?,?,?)", "expected", payload, d.PublishedTimestamp.UnixNano(), d.PublishedTimestamp.UnixNano()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetDealByID(ctx, "expected"); err == nil || got != nil {
		t.Errorf("single lookup trusted mismatched identity: %v %v", got, err)
	}
	if got, err := s.GetDealsByIDs(ctx, []string{"expected"}); err == nil || got != nil {
		t.Errorf("bulk lookup trusted mismatched identity: %v %v", got, err)
	}
	if got, err := s.GetRecentDeals(ctx, time.Hour); err == nil || got != nil {
		t.Errorf("recent lookup trusted mismatched identity: %v %v", got, err)
	}
}

func TestSubscriptionReadsRejectInconsistentScope(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sub := models.Subscription{GuildID: "other-guild", ChannelID: "other-channel", DealType: "rfd_all"}
	payload, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO subscriptions(guild_id,channel_id,filter,payload) VALUES(?,?,?,?)", "expected-guild", "expected-channel", "rfd_all", payload); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetSubscriptionsByGuild(ctx, "expected-guild"); err == nil || got != nil {
		t.Errorf("guild lookup trusted mismatched scope: %v %v", got, err)
	}
	if got, err := s.GetAllSubscriptions(ctx); err == nil || got != nil {
		t.Errorf("all-subscription lookup trusted mismatched scope: %v %v", got, err)
	}
}

func TestRetainedThreadAliasesRespectCanonicalIdentity(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	canonical := models.DealInfo{DocumentID: "canonical", Title: "Example", PublishedTimestamp: time.Now().Add(-72 * time.Hour), Threads: []models.ThreadContext{{DocumentID: "alias", PostURL: "https://forums.redflagdeals.com/example-123/"}}}
	if err := s.TryCreateDeal(ctx, canonical); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDealsByIDs(ctx, []string{"canonical", "alias", "missing", "alias"})
	if err != nil || len(got) != 2 || got["canonical"].DocumentID != "canonical" || got["alias"].DocumentID != "canonical" {
		t.Fatalf("retained identity lookup: %v %v", got, err)
	}
	other := canonical
	other.DocumentID = "other-canonical"
	if err := s.TryCreateDeal(ctx, other); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetDealsByIDs(ctx, []string{"alias"}); err == nil || got != nil {
		t.Fatalf("ambiguous alias silently picked a record: %v %v", got, err)
	}
	// A real row identity takes precedence over aliases with the same spelling.
	direct := models.DealInfo{DocumentID: "alias", Title: "Direct record"}
	if err := s.TryCreateDeal(ctx, direct); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDealsByIDs(ctx, []string{"alias"})
	if err != nil || got["alias"].DocumentID != "alias" {
		t.Fatalf("direct identity precedence: %v %v", got, err)
	}
}
