package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// BenchmarkRuntimeRFD exercises a full retention-sized history plus a batch of
// updates and lookups, including retained thread aliases and unknown identities.
func BenchmarkRuntimeRFD(b *testing.B) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "rfd.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	deals := make([]models.DealInfo, 2000)
	ids := make([]string, 0, 100)
	for i := range deals {
		id := fmt.Sprintf("deal-%04d", i)
		deals[i] = models.DealInfo{DocumentID: id, Title: fmt.Sprintf("Example SSD %d", i), Description: strings.Repeat("description ", 100), PublishedTimestamp: now, LastUpdated: now, Threads: []models.ThreadContext{{DocumentID: fmt.Sprintf("thread-%04d", i), PostURL: "https://fixture.invalid/thread", LikeCount: 20}}, DiscordMessageIDs: map[string]string{"channel": "receipt"}}
		if i < 50 {
			ids = append(ids, id, fmt.Sprintf("thread-%04d", i))
		}
	}
	ids = append(ids, "unknown")
	if err := s.BatchWrite(ctx, deals, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := s.BatchWrite(ctx, nil, deals[:100]); err != nil {
			b.Fatal(err)
		}
		got, err := s.GetRecentDeals(ctx, 48*time.Hour)
		if err != nil || len(got) != len(deals) {
			b.Fatalf("deals=%d err=%v", len(got), err)
		}
		byID, err := s.GetDealsByIDs(ctx, ids)
		if err != nil || len(byID) != 100 {
			b.Fatalf("identities=%d err=%v", len(byID), err)
		}
		if err := s.TrimOldDeals(ctx, 2000); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRuntimeRFDAliases(b *testing.B) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "rfd.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	deals := make([]models.DealInfo, 2000)
	ids := make([]string, 0, 51)
	for i := range deals {
		deals[i] = models.DealInfo{DocumentID: fmt.Sprintf("deal-%04d", i), Title: "Example SSD", Description: strings.Repeat("description ", 100), Threads: []models.ThreadContext{{DocumentID: fmt.Sprintf("thread-%04d", i)}}}
		if i < 50 {
			ids = append(ids, fmt.Sprintf("thread-%04d", i))
		}
	}
	ids = append(ids, "unknown")
	if err := s.BatchWrite(ctx, deals, nil); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		got, err := s.GetDealsByIDs(ctx, ids)
		if err != nil || len(got) != 50 {
			b.Fatalf("identities=%d err=%v", len(got), err)
		}
	}
}
