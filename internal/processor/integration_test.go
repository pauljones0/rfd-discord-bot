//go:build integration

package processor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
)

// Integration test that wires up a real scraper with a mock HTTP server,
// a mock store, and a mock notifier to test the full pipeline.

func TestIntegration_FullPipeline(t *testing.T) {
	// Serve a canned HTML page that mimics the RFD hot deals page
	hotDealsHTML := `<!DOCTYPE html>
<html>
<body>
<ul>
	<li class="topic">
		<a class="thread_title_link" href="/integration-deal-001">Integration Test Deal</a>
		<div class="thread_image"><img src="https://example.com/thumb.jpg" /></div>
		<div class="thread_inner_footer">
			<span class="author_info">
				<time datetime="2025-06-01T12:00:00Z">Jun 1, 2025</time>
				<a class="author" href="/users/integrationuser"><span class="author_name">IntegrationUser</span></a>
			</span>
			<span class="votes">+25</span>
			<span class="posts">10</span>
			<span class="views">500</span>
		</div>
	</li>
	<li class="topic sticky">
		<a class="thread_title_link" href="/sticky-deal">Sticky Deal (should be ignored)</a>
	</li>
	<li class="topic">
		<a class="thread_title_link" href="/integration-deal-002">Second Deal</a>
		<div class="thread_inner_footer">
			<span class="votes">-3</span>
			<span class="posts">2</span>
			<span class="views">100</span>
		</div>
	</li>
</ul>
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

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	cfg := &config.Config{
		DiscordUpdateInterval: "10m",
		AmazonAffiliateTag:    "test-affiliate-20",
		AllowedDomains:        []string{"127.0.0.1"},
	}

	// Create a real scraper pointed at our test server
	s := scraper.NewWithBaseURL(cfg, scraper.DefaultSelectors, srv.URL)

	store := newMockStore()
	notif := newMockNotifier()
	p := New(store, notif, s, cfg)

	err := p.ProcessDeals(context.Background())
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
			if deal.LikeCount != 25 {
				t.Errorf("Deal 1 LikeCount = %d, want 25", deal.LikeCount)
			}
			if deal.CommentCount != 10 {
				t.Errorf("Deal 1 CommentCount = %d, want 10", deal.CommentCount)
			}
			if deal.ViewCount != 500 {
				t.Errorf("Deal 1 ViewCount = %d, want 500", deal.ViewCount)
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
var _ scraper.Scraper = (*mockScraper)(nil)

// Dummy test to verify mock DealInfo fields.
func TestIntegration_MockStoreRoundtrip(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()

	deal := models.DealInfo{
		FirestoreID: "test-id",
		Title:       "Test Deal",
		LikeCount:   10,
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
