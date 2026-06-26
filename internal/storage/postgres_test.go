package storage

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/crux"
)

func TestEncodeDecodeDocumentPrefersDocstoreTags(t *testing.T) {
	type sample struct {
		Name string `docstore:"displayName"`
		Skip string `docstore:"-"`
	}

	data, err := encodeDocument(sample{Name: "current", Skip: "hidden"})
	if err != nil {
		t.Fatalf("encodeDocument() error = %v", err)
	}
	if data["displayName"] != "current" {
		t.Fatalf("displayName = %v, want current", data["displayName"])
	}
	if _, ok := data["legacyName"]; ok {
		t.Fatalf("legacyName should not be encoded when docstore tag exists")
	}
	if _, ok := data["Skip"]; ok {
		t.Fatalf("Skip should not be encoded")
	}

	var out sample
	if err := decodeDocument(map[string]any{"displayName": "decoded"}, &out); err != nil {
		t.Fatalf("decodeDocument() error = %v", err)
	}
	if out.Name != "decoded" {
		t.Fatalf("Name = %q, want decoded", out.Name)
	}
}

func TestPostgresDocumentHelpersIntegration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}

	ctx := context.Background()
	client, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer client.Close()

	collection := fmt.Sprintf("test_document_helpers_%d", time.Now().UnixNano())
	cleanup := func() {
		rows, err := client.ListDocuments(ctx, collection)
		if err != nil {
			return
		}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		_, _ = client.DeleteDocuments(ctx, collection, ids)
	}
	defer cleanup()

	now := time.Now().UTC()
	docs := map[string]map[string]any{
		"old-g1":   {"guildID": "g1", "lastSeen": now.Add(-72 * time.Hour).Format(time.RFC3339)},
		"new-g1":   {"guildID": "g1", "lastSeen": now.Format(time.RFC3339)},
		"new-g2":   {"guildID": "g2", "lastSeen": now.Add(-time.Hour).Format(time.RFC3339)},
		"older-g2": {"guildID": "g2", "lastSeen": now.Add(-2 * time.Hour).Format(time.RFC3339)},
	}
	if err := client.SetRawDocuments(ctx, collection, docs); err != nil {
		t.Fatalf("SetRawDocuments() error = %v", err)
	}

	if err := client.SetDocuments(ctx, collection, map[string]any{
		"typed-g3": struct {
			GuildID  string `docstore:"guildID"`
			LastSeen string `docstore:"lastSeen"`
		}{GuildID: "g3", LastSeen: now.Format(time.RFC3339)},
	}); err != nil {
		t.Fatalf("SetDocuments() error = %v", err)
	}
	deletedTyped, err := client.DeleteDocuments(ctx, collection, []string{"typed-g3"})
	if err != nil {
		t.Fatalf("DeleteDocuments() error = %v", err)
	}
	if deletedTyped != 1 {
		t.Fatalf("DeleteDocuments() deleted %d typed docs, want 1", deletedTyped)
	}

	got, err := client.GetRawDocuments(ctx, collection, []string{"old-g1", "missing", "new-g1"})
	if err != nil {
		t.Fatalf("GetRawDocuments() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetRawDocuments() returned %d docs, want 2", len(got))
	}

	deleted, err := client.DeleteDocumentsWhere(ctx, collection, map[string]any{"guildID": "g1"})
	if err != nil {
		t.Fatalf("DeleteDocumentsWhere() error = %v", err)
	}
	if deleted != 2 {
		t.Fatalf("DeleteDocumentsWhere() deleted %d, want 2", deleted)
	}

	if _, err := client.DeleteDocumentsWhere(ctx, collection, nil); err == nil {
		t.Fatal("DeleteDocumentsWhere() with empty predicate returned nil error")
	}

	pruned, err := client.PruneDocumentsByTime(ctx, collection, "lastSeen", now.Add(-90*time.Minute), 1)
	if err != nil {
		t.Fatalf("PruneDocumentsByTime() error = %v", err)
	}
	if pruned != 1 {
		t.Fatalf("PruneDocumentsByTime() deleted %d, want 1", pruned)
	}
	remaining, err := client.ListDocuments(ctx, collection)
	if err != nil {
		t.Fatalf("ListDocuments() error = %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "new-g2" {
		t.Fatalf("remaining docs = %#v, want only new-g2", remaining)
	}
}

func TestPostgresCruxSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}

	ctx := context.Background()
	client, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer client.Close()

	now := time.Now().UTC()
	symbol := fmt.Sprintf("TX%d", now.UnixNano())
	key := "TSXV:" + symbol
	change := crux.Change{
		Type:        crux.ChangeUpgraded,
		Key:         key,
		Exchange:    "TSXV",
		Symbol:      symbol,
		Ticker:      key,
		Name:        "Crux Transaction Test",
		OldScore:    3,
		HasOldScore: true,
		NewScore:    4,
		HasNewScore: true,
		DetectedAt:  now,
	}
	changeID := crux.ChangeDocID(change)
	defer func() {
		_, _ = client.DeleteDocuments(ctx, cruxCompaniesCollection, []string{key})
		_, _ = client.DeleteDocuments(ctx, cruxChangesCollection, []string{changeID})
	}()

	company := crux.Company{
		Key:          key,
		Exchange:     "TSXV",
		Symbol:       symbol,
		Ticker:       key,
		Name:         "Crux Transaction Test",
		CruxScore:    4,
		HasCruxScore: true,
		Active:       true,
		FirstSeenAt:  now,
		LastSeenAt:   now,
	}
	if err := client.SaveCruxSnapshot(ctx, []crux.Company{company}, []crux.Change{change}); err != nil {
		t.Fatalf("SaveCruxSnapshot() error = %v", err)
	}

	companyDocs, err := client.GetRawDocuments(ctx, cruxCompaniesCollection, []string{key})
	if err != nil {
		t.Fatalf("GetRawDocuments(companies) error = %v", err)
	}
	if len(companyDocs) != 1 || companyDocs[key].Data["name"] != company.Name {
		t.Fatalf("company docs = %#v, want saved company", companyDocs)
	}
	changeDocs, err := client.GetRawDocuments(ctx, cruxChangesCollection, []string{changeID})
	if err != nil {
		t.Fatalf("GetRawDocuments(changes) error = %v", err)
	}
	if len(changeDocs) != 1 || changeDocs[changeID].Data["type"] != crux.ChangeUpgraded {
		t.Fatalf("change docs = %#v, want saved change", changeDocs)
	}
}

func TestPostgresCruxSnapshotRollsBackCompanyWritesWhenChangeWriteFails(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}

	ctx := context.Background()
	client, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer client.Close()

	key := fmt.Sprintf("TSXV:ROLLBACK%d", time.Now().UnixNano())
	defer func() { _, _ = client.DeleteDocuments(ctx, cruxCompaniesCollection, []string{key}) }()

	err = client.saveCruxDocuments(ctx, map[string]map[string]any{
		key: {"key": key, "active": true},
	}, map[string]map[string]any{
		"bad-change": {"bad": math.Inf(1)},
	})
	if err == nil {
		t.Fatal("saveCruxDocuments() returned nil error, want marshal failure")
	}
	companyDocs, err := client.GetRawDocuments(ctx, cruxCompaniesCollection, []string{key})
	if err != nil {
		t.Fatalf("GetRawDocuments(companies) error = %v", err)
	}
	if len(companyDocs) != 0 {
		t.Fatalf("company docs after rollback = %#v, want none", companyDocs)
	}
}

func TestBestBuySoldCompSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set")
	}

	ctx := context.Background()
	client, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("NewPostgres() error = %v", err)
	}
	defer client.Close()

	key := fmt.Sprintf("sold-comp-%d", time.Now().UnixNano())
	defer func() { _ = client.DeleteDocument(ctx, bestbuySoldCompCacheCollection, key) }()

	snapshot := bestbuy.SoldCompSnapshot{
		Query:     "Sony WH-1000XM5",
		Backend:   "http",
		Verdict:   "pass",
		Count:     2,
		Median:    450,
		P25:       425,
		GapAmount: 150,
		GapPct:    33,
		CheckedAt: time.Now().UTC(),
		Examples:  []bestbuy.SoldCompListing{{Title: "Sony WH-1000XM5", Price: 450}},
	}
	if err := client.SaveBestBuySoldCompSnapshot(ctx, key, snapshot); err != nil {
		t.Fatalf("SaveBestBuySoldCompSnapshot() error = %v", err)
	}
	got, ok, err := client.GetBestBuySoldCompSnapshot(ctx, key)
	if err != nil {
		t.Fatalf("GetBestBuySoldCompSnapshot() error = %v", err)
	}
	if !ok {
		t.Fatal("GetBestBuySoldCompSnapshot() ok=false, want true")
	}
	if got.Query != snapshot.Query || got.Count != snapshot.Count || got.Median != snapshot.Median || len(got.Examples) != 1 {
		t.Fatalf("snapshot = %#v, want saved snapshot", got)
	}
}
