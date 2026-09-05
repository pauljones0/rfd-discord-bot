package storage

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenRejectsLegacyDatabaseWithoutChangingTablesPayloadOrJournal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacy, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.Exec("CREATE TABLE documents(collection TEXT NOT NULL,doc_id TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(collection,doc_id)); INSERT INTO documents VALUES('deals','legacy-deal','preserve this payload')"); err != nil {
		t.Fatal(err)
	}
	var beforeJournal string
	if err = legacy.QueryRow("PRAGMA journal_mode").Scan(&beforeJournal); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "foreign table") {
		if store != nil {
			store.Close()
		}
		t.Fatalf("foreign database should fail before schema changes: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("opening rejected database changed its contents: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
			t.Fatalf("created journal artifact %s: %v", suffix, err)
		}
	}
	legacy, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	var afterJournal, payload string
	var tables int
	if err = legacy.QueryRow("PRAGMA journal_mode").Scan(&afterJournal); err != nil || afterJournal != beforeJournal {
		t.Fatalf("journal mode changed: %q -> %q: %v", beforeJournal, afterJournal, err)
	}
	if err = legacy.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("destination schema was initialized in foreign database: %d %v", tables, err)
	}
	if err = legacy.QueryRow("SELECT payload FROM documents WHERE doc_id='legacy-deal'").Scan(&payload); err != nil || payload != "preserve this payload" {
		t.Fatalf("legacy payload changed: %q %v", payload, err)
	}
}

func TestOpenRejectsIncompatibleKnownTableSchema(t *testing.T) {
	for name, schema := range map[string]string{
		"nullable payload":      "CREATE TABLE deals(id TEXT PRIMARY KEY,payload TEXT,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)",
		"different primary key": "CREATE TABLE deals(id TEXT,payload TEXT NOT NULL,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL)",
		"hidden extra column":   "CREATE TABLE deals(id TEXT PRIMARY KEY,payload TEXT NOT NULL,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL,extra INTEGER GENERATED ALWAYS AS (length(payload)))",
		"different settings":    "CREATE TABLE settings(key TEXT PRIMARY KEY,value TEXT NOT NULL)",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "incompatible.sqlite")
			db, err := sql.Open("sqlite3", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(schema); err != nil {
				t.Fatal(err)
			}
			db.Close()
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if store, err := Open(context.Background(), path); err == nil || !strings.Contains(err.Error(), "incompatible schema") {
				if store != nil {
					store.Close()
				}
				t.Fatalf("accepted incompatible table: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("rejected schema was mutated: %v", err)
			}
		})
	}
}

func TestImportRejectsForeignTableCreatedAfterStoreOpened(t *testing.T) {
	s := openMigrationStore(t)
	if _, err := s.db.Exec("CREATE TABLE documents(payload TEXT NOT NULL); INSERT INTO documents VALUES('preserve this payload')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportMigration(context.Background(), migrationFixture(), ImportOptions{"1001", "1002"}); err == nil || !strings.Contains(err.Error(), "foreign table") {
		t.Fatalf("import accepted destination with foreign data: %v", err)
	}
	assertMigrationEmpty(t, s)
	var payload string
	if err := s.db.QueryRow("SELECT payload FROM documents").Scan(&payload); err != nil || payload != "preserve this payload" {
		t.Fatalf("rejected import changed foreign data: %q %v", payload, err)
	}
}
