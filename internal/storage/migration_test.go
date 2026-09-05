package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func migrationFixture() *Migration {
	now := time.Date(2026, 9, 5, 0, 0, 0, 123456789, time.UTC)
	return &Migration{
		Version: MigrationVersion, SourceApplicationID: "1001", ExportedAt: now,
		Subscriptions: []models.Subscription{{GuildID: "2001", ChannelID: "3001", ChannelName: "deals", DealType: "rfd_all", AddedBy: "4001", AddedAt: now}},
		Deals: []models.DealInfo{{
			DocumentID: "thread-12345", Title: "Example deal", PostURL: "https://forums.redflagdeals.com/example-12345/",
			PublishedTimestamp: now.Add(-time.Hour), LastUpdated: now, HasBeenHot: true, AIProcessed: true,
			DiscordMessageIDs: map[string]string{"3001": "5001", "3002": "5002"},
			Threads:           []models.ThreadContext{{DocumentID: "thread-12345", PostURL: "https://forums.redflagdeals.com/example-12345/", LikeCount: -2, CommentCount: 10}},
		}},
	}
}

func openMigrationStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "rfd.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func assertMigrationEmpty(t *testing.T, s *Store) {
	t.Helper()
	var count int
	if err := s.db.QueryRow("SELECT (SELECT COUNT(*) FROM deals)+(SELECT COUNT(*) FROM subscriptions)+(SELECT COUNT(*) FROM settings)").Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed import wrote partial data: count=%d error=%v", count, err)
	}
}

func TestMigrationPreservesHistoryOwnershipAndApplicationAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rfd.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	migration := migrationFixture()
	result, err := s.ImportMigration(ctx, migration, ImportOptions{"1001", "1002"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deals != 1 || result.Subscriptions != 1 || result.MessageReceipts != 2 || !result.ExportedAt.Equal(migration.ExportedAt) {
		t.Fatalf("incorrect import result: %+v", result)
	}
	if migration.Deals[0].DiscordMessageApplicationIDs != nil {
		t.Fatal("import mutated caller's history")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	deal, err := s.GetDealByID(ctx, "thread-12345")
	if err != nil || deal == nil {
		t.Fatalf("lost history: %v %v", deal, err)
	}
	if deal.DiscordMessageIDs["3001"] != "5001" || deal.DiscordMessageApplicationIDs["3001"] != "1001" || deal.DiscordMessageApplicationIDs["3002"] != "1001" || !deal.HasBeenHot || !deal.AIProcessed || !deal.PublishedTimestamp.Equal(migration.Deals[0].PublishedTimestamp) {
		t.Fatalf("import changed history or message ownership: %+v", deal)
	}
	if err = s.TryCreateDeal(ctx, *deal); err != models.ErrDealExists {
		t.Fatalf("receipt history failed deduplication: %v", err)
	}
	subs, err := s.GetSubscriptionsByGuild(ctx, "2001")
	if err != nil || len(subs) != 1 || subs[0].SubscriptionType != "rfd" || subs[0].ChannelID != "3001" {
		t.Fatalf("lost subscription: %+v %v", subs, err)
	}
	if err = s.BindApplication(ctx, "1002"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindApplication(ctx, "1001"); err == nil {
		t.Fatal("accepted old application for target database")
	}
	if _, err = s.ImportMigration(ctx, migration, ImportOptions{"1001", "1002"}); err == nil {
		t.Fatal("merged a second import into a populated database")
	}
	if got, err := s.GetDealByID(ctx, "thread-12345"); err != nil || got.DiscordMessageIDs["3001"] != "5001" {
		t.Fatalf("rejected import disturbed existing history: %+v %v", got, err)
	}
}

func TestMigrationRollsBackDatabaseFailureAfterSubscriptionAndFirstDeal(t *testing.T) {
	s := openMigrationStore(t)
	_, err := s.db.Exec("CREATE TRIGGER reject_second_deal BEFORE INSERT ON deals WHEN NEW.id='blocked' BEGIN SELECT RAISE(FAIL,'fixture failure'); END")
	if err != nil {
		t.Fatal(err)
	}
	migration := migrationFixture()
	second := migration.Deals[0]
	second.DocumentID = "blocked"
	migration.Deals = append(migration.Deals, second)
	if _, err = s.ImportMigration(context.Background(), migration, ImportOptions{"1001", "1002"}); err == nil || !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("expected real transactional insertion failure: %v", err)
	}
	assertMigrationEmpty(t, s)
}

func TestMigrationValidationRejectsBeforeWriting(t *testing.T) {
	tests := map[string]func(*Migration){
		"version":                func(m *Migration) { m.Version = 2 },
		"wrong source":           func(m *Migration) { m.SourceApplicationID = "1003" },
		"missing exported time":  func(m *Migration) { m.ExportedAt = time.Time{} },
		"missing arrays":         func(m *Migration) { m.Deals = nil },
		"invalid snowflake":      func(m *Migration) { m.Subscriptions[0].GuildID = "02001" },
		"invalid attribution":    func(m *Migration) { m.Subscriptions[0].AddedBy = "legacy\noperator" },
		"foreign filter":         func(m *Migration) { m.Subscriptions[0].DealType = "ebay_ca_price_drop" },
		"foreign type":           func(m *Migration) { m.Subscriptions[0].SubscriptionType = "core" },
		"duplicate subscription": func(m *Migration) { m.Subscriptions = append(m.Subscriptions, m.Subscriptions[0]) },
		"channel in different guild": func(m *Migration) {
			other := m.Subscriptions[0]
			other.GuildID = "2002"
			m.Subscriptions = append(m.Subscriptions, other)
		},
		"duplicate deal":              func(m *Migration) { m.Deals = append(m.Deals, m.Deals[0]) },
		"invalid document ID":         func(m *Migration) { m.Deals[0].DocumentID = "../other" },
		"missing title":               func(m *Migration) { m.Deals[0].Title = " " },
		"missing published timestamp": func(m *Migration) { m.Deals[0].PublishedTimestamp = time.Time{} },
		"unrepresentable timestamp":   func(m *Migration) { m.Deals[0].PublishedTimestamp = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) },
		"invalid post URL":            func(m *Migration) { m.Deals[0].PostURL = "file:///etc/passwd" },
		"credentials in URL":          func(m *Migration) { m.Deals[0].PostURL = "https://secret:secret@example.com/thread" },
		"invalid receipt":             func(m *Migration) { m.Deals[0].DiscordMessageIDs["3001"] = "message" },
		"invalid receipt channel":     func(m *Migration) { m.Deals[0].DiscordMessageIDs["channel"] = "5003" },
		"foreign ownership":           func(m *Migration) { m.Deals[0].DiscordMessageApplicationIDs = map[string]string{"3001": "9999"} },
		"orphan ownership":            func(m *Migration) { m.Deals[0].DiscordMessageApplicationIDs = map[string]string{"9999": "1001"} },
		"invalid thread":              func(m *Migration) { m.Deals[0].Threads[0].CommentCount = -1 },
		"duplicate thread":            func(m *Migration) { m.Deals[0].Threads = append(m.Deals[0].Threads, m.Deals[0].Threads[0]) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := openMigrationStore(t)
			migration := migrationFixture()
			mutate(migration)
			if _, err := s.ImportMigration(context.Background(), migration, ImportOptions{"1001", "1002"}); err == nil {
				t.Fatal("invalid migration accepted")
			}
			assertMigrationEmpty(t, s)
		})
	}
}

func TestMigrationRequiresEmptyDestinationOrCompatibleBinding(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"deal", "subscription", "setting", "wrong binding", "matching binding"} {
		t.Run(kind, func(t *testing.T) {
			s := openMigrationStore(t)
			migration := migrationFixture()
			var err error
			switch kind {
			case "deal":
				err = s.TryCreateDeal(ctx, migration.Deals[0])
			case "subscription":
				err = s.SaveSubscription(ctx, migration.Subscriptions[0])
			case "setting":
				err = s.UpdateGeminiQuotaStatus(ctx, models.GeminiQuotaStatus{})
			case "wrong binding":
				err = s.BindApplication(ctx, "1003")
			case "matching binding":
				err = s.BindApplication(ctx, "1002")
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ImportMigration(ctx, migration, ImportOptions{"1001", "1002"})
			if (kind == "matching binding") != (err == nil) {
				t.Fatalf("unexpected import outcome: %v", err)
			}
		})
	}
}

func TestApplicationBindingRejectsUnownedHistoryAndInvalidID(t *testing.T) {
	s := openMigrationStore(t)
	ctx := context.Background()
	if err := s.BindApplication(ctx, "not-a-snowflake"); err == nil {
		t.Fatal("invalid application ID accepted")
	}
	assertMigrationEmpty(t, s)
	if err := s.TryCreateDeal(ctx, migrationFixture().Deals[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.BindApplication(ctx, "1002"); err == nil {
		t.Fatal("silently assigned ownership to existing unbound history")
	}
}

func TestEmptyMigrationCannotBeImportedTwice(t *testing.T) {
	s := openMigrationStore(t)
	migration := migrationFixture()
	migration.Deals = []models.DealInfo{}
	migration.Subscriptions = []models.Subscription{}
	options := ImportOptions{"1001", "1002"}
	if _, err := s.ImportMigration(context.Background(), migration, options); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportMigration(context.Background(), migration, options); err == nil {
		t.Fatal("migration metadata was ignored on retry")
	}
}

func TestMigrationPreservesLegacyUsernameAttribution(t *testing.T) {
	s := openMigrationStore(t)
	migration := migrationFixture()
	migration.Subscriptions[0].AddedBy = "legacy-operator"
	ctx := context.Background()
	if _, err := s.ImportMigration(ctx, migration, ImportOptions{"1001", "1002"}); err != nil {
		t.Fatal(err)
	}
	subscriptions, err := s.GetAllSubscriptions(ctx)
	if err != nil || len(subscriptions) != 1 || subscriptions[0].AddedBy != "legacy-operator" {
		t.Fatalf("lost legacy attribution: %+v %v", subscriptions, err)
	}
}

func TestDecodeMigrationRejectsSchemaLossAndTrailingDocuments(t *testing.T) {
	data, err := json.Marshal(migrationFixture())
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeMigration(bytes.NewReader(data))
	if err != nil || decoded.Deals[0].DiscordMessageIDs["3001"] != "5001" || !decoded.ExportedAt.Equal(migrationFixture().ExportedAt) {
		t.Fatalf("could not decode valid export: %+v %v", decoded, err)
	}
	for _, bad := range []string{
		strings.Replace(string(data), `"version":1`, `"version":1,"new_schema_field":true`, 1),
		strings.Replace(string(data), `"Title":"Example deal"`, `"Title":"Example deal","UnknownReceiptFormat":{}`, 1),
		string(data) + " {}",
		string(data) + " garbage",
	} {
		if _, err = DecodeMigration(strings.NewReader(bad)); err == nil {
			t.Fatal("accepted unknown schema fields or trailing data")
		}
	}
}
