package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestThreadIndexUpgradesExistingDatabaseAndPreservesReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rfd.sqlite")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE deals(id TEXT PRIMARY KEY,payload TEXT NOT NULL,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	d := models.DealInfo{DocumentID: "canonical", Title: "Old deal", PublishedTimestamp: time.Now().Add(-72 * time.Hour), Threads: []models.ThreadContext{{DocumentID: "alias"}}, DiscordMessageIDs: map[string]string{"channel": "receipt"}}
	payload, _ := json.Marshal(d)
	_, err = db.Exec(`INSERT INTO deals VALUES(?,?,?,?)`, d.DocumentID, payload, d.PublishedTimestamp.UnixNano(), time.Now().UnixNano())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.GetDealsByIDs(ctx, []string{"alias"})
		if err != nil || got["alias"] == nil || got["alias"].DiscordMessageIDs["channel"] != "receipt" {
			t.Fatalf("upgraded alias: %+v %v", got, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestThreadIndexUpdatesAndPruningRemainAtomic(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := models.DealInfo{DocumentID: "canonical", Threads: []models.ThreadContext{{DocumentID: "old"}}, LastUpdated: time.Now().Add(-time.Hour)}
	if err := s.TryCreateDeal(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.Threads[0].DocumentID = "new"
	if err := s.BatchWrite(ctx, nil, []models.DealInfo{d}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDealsByIDs(ctx, []string{"old", "new"})
	if err != nil || got["old"] != nil || got["new"] == nil {
		t.Fatalf("alias update: %+v %v", got, err)
	}
	other := d
	other.DocumentID = "other"
	other.LastUpdated = time.Now()
	other.Threads = nil
	// A duplicate create must roll back both the row and its index entries.
	if err := s.BatchWrite(ctx, []models.DealInfo{other, d}, nil); err == nil {
		t.Fatal("duplicate batch should fail")
	}
	if row, err := s.GetDealByID(ctx, "other"); err != nil || row != nil {
		t.Fatal("partial batch survived")
	}
	if err := s.TryCreateDeal(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx, 1); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDealsByIDs(ctx, []string{"new"})
	if err != nil || len(got) != 0 {
		t.Fatalf("pruned alias survived: %+v %v", got, err)
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM deal_threads`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan aliases=%d %v", count, err)
	}
}
