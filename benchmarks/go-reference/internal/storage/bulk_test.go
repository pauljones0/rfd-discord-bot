package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestBulkLookupBoundariesAndIsolation(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var deals []models.DealInfo
	var ids []string
	for i := range 1001 {
		id := fmt.Sprintf("deal-%d", i)
		ids = append(ids, id)
		deals = append(deals, models.DealInfo{DocumentID: id, Title: id})
	}
	if err := s.BatchWrite(ctx, deals, nil); err != nil {
		t.Fatal(err)
	}
	// Duplicate and SQL-shaped unknown IDs must not change the result set.
	ids = append(ids, ids[0], "') OR 1=1 --")
	got, err := s.GetDealsByIDs(ctx, ids)
	if err != nil || len(got) != len(deals) {
		t.Fatalf("bulk lookup: count=%d err=%v", len(got), err)
	}
	for _, deal := range deals {
		if got[deal.DocumentID] == nil || got[deal.DocumentID].Title != deal.Title {
			t.Fatalf("lost or aliased deal %s", deal.DocumentID)
		}
	}
	if empty, err := s.GetDealsByIDs(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty lookup: %v %v", empty, err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE deals SET payload='invalid' WHERE id=?", ids[0]); err != nil {
		t.Fatal(err)
	}
	if partial, err := s.GetDealsByIDs(ctx, ids); err == nil || partial != nil {
		t.Fatal("corrupt history must fail the lookup instead of looking like missing deals")
	}
}
