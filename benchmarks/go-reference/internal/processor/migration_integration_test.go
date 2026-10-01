//go:build integration

package processor_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
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

func TestMigratedHistoryPreventsReplayAndForeignEdits(t *testing.T) {
	var phase, oldPosts, newPosts, newEdits atomic.Int32
	published := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hot-deals":
			fmt.Fprintf(w, `<li class="topic-card topic"><a class="topic-card-info thread_info" href="/deal-123456"><h3 class="thread_title">Example SSD deal</h3><time class="topic_time" datetime="%s"></time></a><div class="thread_extra_info"><span class="votes">%d</span><span class="posts">10</span><span class="views">1000</span></div></li>`, published, 42+phase.Load())
		case r.URL.Path == "/deal-123456":
			fmt.Fprint(w, `<div class="deal_link"><a href="https://example.com/ssd">Product</a></div><div class="retailer_badge">Example Shop</div><div class="thread_category">Computers &amp; Electronics</div><dl><dt>Price:</dt><dd>$50</dd></dl>`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v10/channels/400/messages":
			oldPosts.Add(1)
			fmt.Fprint(w, `{"id":"600"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v10/channels/500/messages":
			newPosts.Add(1)
			fmt.Fprint(w, `{"id":"700"}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v10/channels/500/messages/700":
			newEdits.Add(1)
			fmt.Fprint(w, `{"id":"700"}`)
		default:
			t.Errorf("unexpected request, including any edit to the old bot's message: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	original := http.DefaultTransport
	http.DefaultTransport = localOnlyTransport{target: target, base: server.Client().Transport}
	defer func() { http.DefaultTransport = original }()
	ctx := context.Background()
	cfg := &config.Config{DiscordAppID: "100", RFDBaseURL: server.URL, AllowedDomains: []string{target.Hostname()}, MaxStoredDeals: 100, DiscordUpdateInterval: 10 * time.Minute}
	newProcessor := func(s *storage.Store, c *config.Config) *processor.DealProcessor {
		return processor.New(s, notifier.New("fixture-token", c.DiscordAppID), scraper.NewWithBaseURL(c, scraper.DefaultSelectors(), server.URL), validator.New(), c, nil)
	}
	oldStore, err := storage.Open(ctx, filepath.Join(t.TempDir(), "old.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer oldStore.Close()
	sub := models.Subscription{GuildID: "300", ChannelID: "400", DealType: "rfd_all", SubscriptionType: "rfd"}
	if err = oldStore.SaveSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	if err = newProcessor(oldStore, cfg).ProcessDeals(ctx); err != nil {
		t.Fatal(err)
	}
	deals, err := oldStore.GetRecentDeals(ctx, time.Hour)
	if err != nil || len(deals) != 1 || oldPosts.Load() != 1 {
		t.Fatalf("old pipeline: deals=%d posts=%d err=%v", len(deals), oldPosts.Load(), err)
	}
	deals[0].DiscordLastUpdatedTime = time.Now().Add(-time.Hour)
	path := filepath.Join(t.TempDir(), "new.sqlite")
	newStore, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = newStore.Close() }()
	_, err = newStore.ImportMigration(ctx, &storage.Migration{Version: 1, SourceApplicationID: "100", ExportedAt: time.Now().UTC(), Subscriptions: []models.Subscription{sub}, Deals: deals}, storage.ImportOptions{SourceApplicationID: "100", TargetApplicationID: "200"})
	if err != nil {
		t.Fatal(err)
	}
	cfg.DiscordAppID = "200"
	p := newProcessor(newStore, cfg)
	phase.Store(1)
	if err = p.ProcessDeals(ctx); err != nil {
		t.Fatal(err)
	}
	if oldPosts.Load() != 1 || newPosts.Load() != 0 {
		t.Fatal("migration replayed an existing notification")
	}
	sub.ChannelID = "500"
	if err = newStore.SaveSubscription(ctx, sub); err != nil {
		t.Fatal(err)
	}
	phase.Store(2)
	if err = p.ProcessDeals(ctx); err != nil {
		t.Fatal(err)
	}
	forUpdate, err := newStore.GetDealByID(ctx, deals[0].DocumentID)
	if err != nil || forUpdate == nil {
		t.Fatalf("read before next update: %v", err)
	}
	forUpdate.DiscordLastUpdatedTime = time.Now().Add(-time.Hour)
	if err = newStore.UpdateDeal(ctx, *forUpdate); err != nil {
		t.Fatal(err)
	}
	phase.Store(3)
	if err = p.ProcessDeals(ctx); err != nil {
		t.Fatal(err)
	}
	if oldPosts.Load() != 1 || newPosts.Load() != 1 || newEdits.Load() != 1 {
		t.Fatalf("unexpected notifications: old=%d new=%d edits=%d", oldPosts.Load(), newPosts.Load(), newEdits.Load())
	}
	if err = newStore.Close(); err != nil {
		t.Fatal(err)
	}
	newStore, err = storage.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = newStore.BindApplication(ctx, "200"); err != nil {
		t.Fatal(err)
	}
	retained, err := newStore.GetDealByID(ctx, deals[0].DocumentID)
	if err != nil || retained == nil {
		t.Fatalf("read migrated history: %v", err)
	}
	if retained.DiscordMessageIDs["400"] != "600" || retained.DiscordMessageApplicationIDs["400"] != "100" || retained.DiscordMessageIDs["500"] != "700" || retained.DiscordMessageApplicationIDs["500"] != "200" {
		t.Fatalf("receipt ownership did not survive processing and restart: %+v", retained.DiscordMessageApplicationIDs)
	}
}
