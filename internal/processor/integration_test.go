//go:build integration

package processor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

// Integration test that wires up a real scraper with a mock HTTP server,
// a mock store, and a mock notifier to test the full pipeline.

func TestIntegration_FullPipeline(t *testing.T) {
	originalTransport := http.DefaultTransport
	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}
	defer func() {
		if transport, ok := http.DefaultTransport.(*http.Transport); ok {
			transport.CloseIdleConnections()
		}
		http.DefaultTransport = originalTransport
	}()

	// Serve a canned HTML page that mimics the RFD hot deals page
	hotDealsHTML := `<!DOCTYPE html>
<html>
<body>
	<li class="topic-card topic">
		<a class="topic-card-info thread_info" href="/integration-deal-001">
			<div class="thread_main">
				<div class="thread_info">
					<div class="thread_info_block">
						<h3 class="thread_title">Integration Test Deal</h3>
						<div class="thread_footer">
							<time class="topic_time" datetime="2025-06-01T12:00:00Z">Jun 1, 2025</time>
						</div>
					</div>
				</div>
			</div>
		</a>
		<div class="thread_image"><img src="https://example.com/thumb.jpg" /></div>
		<div class="thread_extra_info">
			<span class="votes">+25</span>
			<span class="posts">10</span>
			<span class="views">500</span>
		</div>
	</li>
	<li class="topic-card topic sticky">
		<a class="topic-card-info thread_info" href="/sticky-deal">
			<h3 class="thread_title">Sticky Deal (should be ignored)</h3>
		</a>
	</li>
	<li class="topic-card topic">
		<a class="topic-card-info thread_info" href="/integration-deal-002">
			<div class="thread_main">
				<div class="thread_info">
					<div class="thread_info_block">
						<h3 class="thread_title">Second Deal</h3>
						<div class="thread_footer">
							<time class="topic_time" datetime="2025-06-02T14:00:00Z">Jun 2, 2025</time>
						</div>
					</div>
				</div>
			</div>
		</a>
		<div class="thread_extra_info">
			<span class="votes">-3</span>
			<span class="posts">2</span>
			<span class="views">100</span>
		</div>
	</li>
</body>
</html>`

	// Detail page for deal-001 with a product link
	detailHTML001 := `<!DOCTYPE html>
<html><body>
	<a class="deal_link" href="https://amazon.ca/dp/B001?tag=other-tag">Get Deal</a>
</body></html>`

	// Detail page for deal-002 with no product link
	detailHTML002 := `<!DOCTYPE html>
<html><body>
	<p>This deal has no external link.</p>
</body></html>`

	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hot-deals":
			fmt.Fprint(w, hotDealsHTML)
		case "/integration-deal-001":
			fmt.Fprint(w, detailHTML001)
		case "/integration-deal-002":
			fmt.Fprint(w, detailHTML002)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	parsedURL, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}

	cfg := &config.Config{
		DiscordUpdateInterval: 10 * time.Minute,
		MaxStoredDeals:        500,
		AmazonAffiliateTag:    "test-affiliate-20",
		AllowedDomains:        []string{parsedURL.Hostname()},
		RFDBaseURL:            srv.URL,
	}

	// Create a real scraper pointed at our test server.
	// The canned fixture still includes the historical list-page view count node.
	selectors := scraper.DefaultSelectors()
	selectors.HotDealsList.Elements.ViewCount = ".thread_extra_info .views"
	s := scraper.NewWithBaseURL(cfg, selectors, srv.URL)

	store := newMockStore()
	notif := newMockNotifier()
	p := New(store, notif, s, validator.New(), cfg, &mockDealAnalyzer{})

	err = p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("ProcessDeals() error = %v", err)
	}

	// Verify we got deals (sticky should be filtered out)
	if len(store.deals) != 2 {
		t.Errorf("Expected 2 deals in store (sticky excluded), got %d", len(store.deals))
	}

	// Verify notifications were sent
	if len(notif.sentDeals) != 2 {
		t.Errorf("Expected 2 notifications, got %d", len(notif.sentDeals))
	}

	// Verify deal data was parsed correctly
	for _, deal := range store.deals {
		if deal.Title == "Integration Test Deal" {
			if len(deal.Threads) != 1 {
				t.Fatalf("expected 1 thread for Integration Test Deal, got %d", len(deal.Threads))
			}
			if deal.Threads[0].LikeCount != 25 {
				t.Errorf("Deal 1 LikeCount = %d, want 25", deal.Threads[0].LikeCount)
			}
			if deal.Threads[0].CommentCount != 10 {
				t.Errorf("Deal 1 CommentCount = %d, want 10", deal.Threads[0].CommentCount)
			}
			if deal.Threads[0].ViewCount != 500 {
				t.Errorf("Deal 1 ViewCount = %d, want 500", deal.Threads[0].ViewCount)
			}
		}
	}

	// Verify trim was called (new deals were added)
	if !store.trimCalled {
		t.Error("Expected TrimOldDeals to be called")
	}

	// --- Second run with same data: no new deals, no further notifications ---
	notif.sentDeals = nil
	store.trimCalled = false

	err = p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("Second ProcessDeals() error = %v", err)
	}

	if len(notif.sentDeals) != 0 {
		t.Errorf("Expected 0 new notifications on second run, got %d", len(notif.sentDeals))
	}
	if store.trimCalled {
		t.Error("TrimOldDeals should not be called when no new deals are added")
	}
}

// mockScraper for non-integration tests is defined in processor_test.go.
// For the integration test, we use the real scraper with NewWithBaseURL.

// Verify that the mock types satisfy the interfaces.
var _ DealStore = (*mockStore)(nil)
var _ DealNotifier = (*mockNotifier)(nil)
var _ DealScraper = (*mockScraper)(nil)

// Dummy test to verify mock DealInfo fields.
func TestIntegration_MockStoreRoundtrip(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()

	deal := models.DealInfo{
		DocumentID: "test-id",
		Title:      "Test Deal",
		Threads: []models.ThreadContext{
			{LikeCount: 10},
		},
	}

	if err := store.TryCreateDeal(ctx, deal); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetDealByID(ctx, "test-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Test Deal" {
		t.Errorf("Title = %q, want %q", got.Title, "Test Deal")
	}
}
