package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestFetchHTMLContentValidatesEveryRedirect(t *testing.T) {
	var destinationHits atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHits.Add(1)
		fmt.Fprint(w, `<div>destination body</div>`)
	}))
	defer destination.Close()
	forbiddenURL := strings.Replace(destination.URL, "127.0.0.1", "localhost", 1)
	credentialURL, _ := url.Parse(destination.URL)
	credentialURL.User = url.UserPassword("fixture-user", "fixture-secret")
	var originHits atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originHits.Add(1)
		switch r.URL.Path {
		case "/start":
			http.Redirect(w, r, "/second-hop", http.StatusFound)
		case "/second-hop":
			http.Redirect(w, r, forbiddenURL, http.StatusFound)
		case "/credentials":
			http.Redirect(w, r, credentialURL.String(), http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		default:
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}
	}))
	defer origin.Close()
	c := New(&config.Config{AllowedDomains: []string{"127.0.0.1"}}, DefaultSelectors())
	// The boundary also applies when a caller supplies its own HTTP client.
	c.httpClient = origin.Client()
	if _, err := c.fetchHTMLContent(context.Background(), forbiddenURL); err == nil {
		t.Fatal("direct forbidden URL was accepted")
	}
	if _, err := c.fetchHTMLContent(context.Background(), origin.URL+"/start"); err == nil {
		t.Fatal("off-allowlist second redirect was accepted")
	}
	if destinationHits.Load() != 0 || originHits.Load() != 2 {
		t.Fatalf("redirect boundary: destination hits=%d origin hits=%d", destinationHits.Load(), originHits.Load())
	}
	if _, err := c.fetchHTMLContent(context.Background(), origin.URL+"/credentials"); err == nil {
		t.Fatal("URL credentials in redirect were accepted")
	} else if strings.Contains(err.Error(), "fixture-secret") {
		t.Fatal("rejected redirect exposed URL credentials in error")
	}
	if destinationHits.Load() != 0 {
		t.Fatal("credential-bearing redirect reached destination")
	}
	if _, err := c.fetchHTMLContent(context.Background(), origin.URL+"/allowed"); err != nil {
		t.Fatalf("valid same-allowlist redirect rejected: %v", err)
	}
	if destinationHits.Load() != 1 {
		t.Fatal("valid redirect did not reach destination")
	}
	before := originHits.Load()
	if _, err := c.fetchHTMLContent(context.Background(), origin.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "10 redirects") {
		t.Fatalf("redirect limit not enforced: %v", err)
	}
	if originHits.Load()-before != 10 {
		t.Fatalf("loop requests=%d, want 10", originHits.Load()-before)
	}
	before = originHits.Load()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.fetchHTMLContent(ctx, origin.URL+"/allowed"); !errors.Is(err, context.Canceled) {
		t.Fatalf("context cancellation lost: %v", err)
	}
	if originHits.Load() != before {
		t.Fatal("canceled fetch contacted fixture server")
	}
}

func TestFetchHTMLContentPreservesCustomRedirectPolicy(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/destination", http.StatusFound)
	}))
	defer server.Close()
	c := New(&config.Config{AllowedDomains: []string{"127.0.0.1"}}, DefaultSelectors())
	c.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, err := c.fetchHTMLContent(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "status code 302") || hits.Load() != 1 {
		t.Fatalf("custom redirect policy lost: hits=%d err=%v", hits.Load(), err)
	}
}

func TestParseDetailPageFindsFirstUsableExternalLink(t *testing.T) {
	for _, tc := range []struct{ name, html, want string }{
		{"skip forum reference", `<a class="postlink" href="https://forums.redflagdeals.com/old-thread">Earlier thread</a><a class="postlink" href="https://store.example/product">Buy</a>`, "https://store.example/product"},
		{"skip placeholder", `<a class="postlink" href="javascript:void(0)">Expand</a><a class="postlink" href="https://store.example/product">Buy</a>`, "https://store.example/product"},
		{"primary precedence", `<a class="postlink" href="https://store.example/other">Other</a><div class="deal_link"><a href="https://store.example/product">Buy</a></div>`, "https://store.example/product"},
		{"later primary", `<div class="deal_link"><a href="#">Expand</a><a href="https://store.example/product">Buy</a></div>`, "https://store.example/product"},
		{"external domain containing RFD name", `<a class="postlink" href="https://redflagdeals.com.store.example/product">Buy</a>`, "https://redflagdeals.com.store.example/product"},
		{"only forum links", `<a class="postlink" href="https://redflagdeals.com/old">Forum</a><a class="postlink" href="https://forums.redflagdeals.com/old">Thread</a>`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(&config.Config{}, DefaultSelectors())
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tc.html))
			if err != nil {
				t.Fatal(err)
			}
			detail, err := c.parseDetailPage(doc)
			if err != nil {
				t.Fatal(err)
			}
			if detail.DealLink != tc.want {
				t.Fatalf("deal link=%q, want %q", detail.DealLink, tc.want)
			}
		})
	}
}

func TestParseDetailPageAcceptsObjectAndArrayJSONLD(t *testing.T) {
	posting := `{"@context":"https://schema.org","@type":"DiscussionForumPosting","text":"<p>Detailed deal description</p>","datePublished":"2026-09-01T12:00:00Z","comment":[{"@type":"Comment","text":"Available online"}],"about":{"@type":"Product","brand":{"name":"Fixture Store"},"offers":{"@type":"Offer","price":"99.99","priceCurrency":"CAD"}}}`
	for _, array := range []bool{true, false} {
		t.Run(fmt.Sprintf("array=%v", array), func(t *testing.T) {
			payload := posting
			if array {
				payload = "[" + payload + "]"
			}
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(`<script type="application/ld+json">invalid</script><script type="application/ld+json">` + payload + `</script>`))
			if err != nil {
				t.Fatal(err)
			}
			c := New(&config.Config{}, DefaultSelectors())
			detail, err := c.parseDetailPage(doc)
			if err != nil {
				t.Fatal(err)
			}
			if detail.Description != "Detailed deal description" || detail.Comments != "- Available online" || detail.Price != "$99.99" || detail.Retailer != "Fixture Store" {
				t.Fatalf("JSON-LD fields lost: %+v", detail)
			}
		})
	}
}

func TestFetchDealDetailsPreservesMissingCardPriceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<div class="deal_link"><a href="https://store.example/product">Product</a></div>`)
		if r.URL.Path == "/priced" {
			fmt.Fprint(w, `<dl><dt>Price:</dt><dd>$69.99</dd><dt>Original Price:</dt><dd>$89.99</dd><dt>Savings:</dt><dd>$20</dd></dl>`)
		}
	}))
	defer server.Close()
	for _, tc := range []struct{ path, price, original, savings string }{
		{"/unpriced", "$79.99", "$99.99", "20%"},
		{"/priced", "$69.99", "$89.99", "$20"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			c := New(&config.Config{AllowedDomains: []string{"127.0.0.1"}}, DefaultSelectors())
			deal := models.DealInfo{PostURL: server.URL + tc.path, Price: "$79.99", OriginalPrice: "$99.99", Savings: "20%", Threads: []models.ThreadContext{{PostURL: server.URL + tc.path}}}
			stats := c.FetchDealDetails(context.Background(), []*models.DealInfo{&deal})
			if stats.Succeeded != 1 {
				t.Fatalf("fixture fetch failed: %+v", stats)
			}
			if deal.Price != tc.price || deal.OriginalPrice != tc.original || deal.Savings != tc.savings {
				t.Fatalf("incorrect prices: %+v", deal)
			}
		})
	}
}
