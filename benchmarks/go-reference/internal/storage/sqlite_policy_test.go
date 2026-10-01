package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestSQLiteSmallVMPolicyPersistsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	for range 2 {
		s, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		var mode string
		if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
			t.Fatalf("journal=%s %v", mode, err)
		}
		for pragma, want := range map[string]int{"synchronous": 2, "cache_size": -2048, "wal_autocheckpoint": 256, "journal_size_limit": 1048576} {
			var got int
			if err := s.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("%s=%d want=%d err=%v", pragma, got, want, err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
