package storage

import (
	"context"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"testing"
	"time"
)

func TestDuplicateThreadAliasUpsert(t *testing.T) {
	ctx := context.Background()
	s, e := Open(ctx, t.TempDir()+"/fixture.sqlite")
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	d := models.DealInfo{DocumentID: "canonical", Title: "fixture", PublishedTimestamp: time.Now(), LastUpdated: time.Now(), Threads: []models.ThreadContext{{DocumentID: "alias", PostURL: "http://example.invalid/old"}, {DocumentID: "alias", PostURL: "https://example.invalid/new"}}}
	if e = s.BatchWrite(ctx, []models.DealInfo{d}, nil); e != nil {
		t.Fatal(e)
	}
	d.Title = "updated"
	if e = s.BatchWrite(ctx, nil, []models.DealInfo{d}); e != nil {
		t.Fatal(e)
	}
	deals, e := s.GetDealsByIDs(ctx, []string{"alias"})
	if e != nil {
		t.Fatal(e)
	}
	if deals["alias"] == nil || deals["alias"].Title != "updated" {
		t.Fatal("alias ownership lost")
	}
}
