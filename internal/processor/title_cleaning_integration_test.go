//go:build integration

package processor_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

type fixtureTitleAnalyzer struct{ calls int }

func (a *fixtureTitleAnalyzer) CleanTitles(_ context.Context, requests []models.TitleRequest) (map[int]string, error) {
	a.calls++
	results := make(map[int]string, len(requests))
	for _, request := range requests {
		results[request.Index] = "Cleaned: " + request.Title
	}
	return results, nil
}

func (*fixtureTitleAnalyzer) DrainTokens() (int, int) { return 0, 0 }

func TestTitleCleanupBackfillSurvivesRestartWithoutEditingForeignReceipts(t *testing.T) {
	published := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hot-deals":
			fmt.Fprintf(w, `<li class="topic-card topic"><a class="topic-card-info thread_info" href="/deal-123456"><h3 class="thread_title">Example SSD deal</h3><time class="topic_time" datetime="%s"></time></a><div class="thread_extra_info"><span class="votes">42</span><span class="posts">10</span><span class="views">1000</span></div></li>`, published.Format(time.RFC3339))
		case "/deal-123456":
			fmt.Fprint(w, `<div class="deal_link"><a href="https://example.com/ssd">Product</a></div><div class="retailer_badge">Example Shop</div><div class="thread_category">Computers &amp; Electronics</div><dl><dt>Price:</dt><dd>$50</dd></dl>`)
		default:
			// Cleanup must not repost the imported receipt or attempt to edit a
			// message authored by the original application.
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	originalTransport := http.DefaultTransport
	http.DefaultTransport = localOnlyTransport{target: target, base: server.Client().Transport}
	defer func() { http.DefaultTransport = originalTransport }()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rfd.sqlite")
	hash := sha256.Sum256([]byte(published.Format(time.RFC3339Nano)))
	id := hex.EncodeToString(hash[:])
	deal := models.DealInfo{
		DocumentID: id, Title: "Example SSD deal", PublishedTimestamp: published,
		PostURL: server.URL + "/deal-123456", ActualDealURL: "https://example.com/ssd",
		DiscordMessageIDs:      map[string]string{"400": "600"},
		DiscordLastUpdatedTime: time.Now().Add(-time.Hour),
	}
	store, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ImportMigration(ctx, &storage.Migration{
		Version: 1, SourceApplicationID: "100", ExportedAt: time.Now().UTC(), Deals: []models.DealInfo{deal},
		Subscriptions: []models.Subscription{{GuildID: "300", ChannelID: "400", DealType: "rfd_all", SubscriptionType: "rfd"}},
	}, storage.ImportOptions{SourceApplicationID: "100", TargetApplicationID: "200"})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DiscordAppID: "200", RFDBaseURL: server.URL, AllowedDomains: []string{target.Hostname()}, MaxStoredDeals: 100, DiscordUpdateInterval: 10 * time.Minute}
	analyzer := &fixtureTitleAnalyzer{}
	for iteration := range 2 {
		store, err = storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		p := processor.New(store, notifier.New("fixture-token", cfg.DiscordAppID), scraper.NewWithBaseURL(cfg, scraper.DefaultSelectors(), server.URL), validator.New(), cfg, analyzer)
		if err = p.ProcessDeals(ctx); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
		retained, readErr := store.GetDealByID(ctx, id)
		if readErr != nil || retained == nil || !retained.AIProcessed || retained.CleanTitle != "Cleaned: Example SSD deal" || retained.DiscordMessageIDs["400"] != "600" || retained.DiscordMessageApplicationIDs["400"] != "100" {
			_ = store.Close()
			t.Fatalf("iteration %d did not retain cleanup and foreign ownership: %+v, %v", iteration, retained, readErr)
		}
		if err = store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if analyzer.calls != 1 {
		t.Fatalf("persisted cleanup was repeated after restart: %d calls", analyzer.calls)
	}
}
