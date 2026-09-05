//go:build integration

package processor_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-standalone/internal/config"
	"github.com/pauljones0/rfd-discord-standalone/internal/models"
	"github.com/pauljones0/rfd-discord-standalone/internal/notifier"
	"github.com/pauljones0/rfd-discord-standalone/internal/processor"
	"github.com/pauljones0/rfd-discord-standalone/internal/scraper"
	"github.com/pauljones0/rfd-discord-standalone/internal/storage"
	"github.com/pauljones0/rfd-discord-standalone/internal/validator"
)

type localOnlyTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (tr localOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	u := *r.URL
	clone.URL = &u
	if u.Host == "discord.com" {
		clone.URL.Scheme = tr.target.Scheme
		clone.URL.Host = tr.target.Host
		clone.Host = tr.target.Host
	}
	if clone.URL.Host != tr.target.Host {
		return nil, fmt.Errorf("test blocked external network request")
	}
	return tr.base.RoundTrip(clone)
}

func TestStandaloneScrapeStoreNotifySurvivesRestart(t *testing.T) {
	var sent atomic.Int32
	published := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/hot-deals":
			fmt.Fprintf(w, `<html><body><li class="topic-card topic"><a class="topic-card-info thread_info" href="/deal-123456"><h3 class="thread_title">Example SSD deal</h3><time class="topic_time" datetime="%s"></time></a><div class="thread_extra_info"><span class="votes">42</span><span class="posts">10</span><span class="views">1000</span></div></li></body></html>`, published)
		case r.URL.Path == "/deal-123456":
			fmt.Fprint(w, `<html><body><div class="deal_link"><a href="https://example.com/ssd">Product</a></div><div class="retailer_badge">Example Shop</div><div class="thread_category">Computers &amp; Electronics</div><dl><dt>Price:</dt><dd>$50</dd></dl></body></html>`)
		case strings.HasSuffix(r.URL.Path, "/channels/channel/messages") && r.Method == http.MethodPost:
			sent.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"receipt"}`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	original := http.DefaultTransport
	http.DefaultTransport = localOnlyTransport{target: target, base: server.Client().Transport}
	defer func() { http.DefaultTransport = original }()
	cfg := &config.Config{RFDBaseURL: server.URL, AllowedDomains: []string{target.Hostname()}, DiscordUpdateInterval: time.Hour, MaxStoredDeals: 100}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rfd.sqlite")
	for iteration := range 2 {
		s, err := storage.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 0 {
			// Two eligible filters in one destination must still produce one message.
			for _, filter := range []string{"rfd_all", "rfd_tech"} {
				if err = s.SaveSubscription(ctx, models.Subscription{GuildID: "guild", ChannelID: "channel", DealType: filter}); err != nil {
					t.Fatal(err)
				}
			}
		}
		p := processor.New(s, notifier.New("fixture-token"), scraper.NewWithBaseURL(cfg, scraper.DefaultSelectors(), server.URL), validator.New(), cfg, nil)
		if err = p.ProcessDeals(ctx); err != nil {
			t.Fatal(err)
		}
		deals, err := s.GetRecentDeals(ctx, time.Hour)
		if err != nil || len(deals) != 1 || deals[0].DiscordMessageIDs["channel"] != "receipt" {
			t.Fatalf("missing persistent receipt: %+v %v", deals, err)
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if got := sent.Load(); got != 1 {
		t.Fatalf("expected one notification across restart and overlapping filters, got %d", got)
	}
}
