package scraper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
)

func TestParseDealFromSelection_FullDeal(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/great-deal-12345">Great Deal Title</a>
		<div class="thread_image"><img src="https://example.com/image.jpg" /></div>
		<div class="thread_inner_footer">
			<span class="author_info">
				<time datetime="2025-01-15T10:30:00Z">Jan 15, 2025</time>
			</span>
			<span class="votes">+42</span>
			<span class="posts">15</span>
			<span class="views">1,234</span>
		</div>
	</li>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("Failed to parse test HTML: %v", err)
	}

	defaults := DefaultSelectors()
	c := &Client{selectors: defaults, config: &config.Config{
		AllowedDomains: []string{"forums.redflagdeals.com"},
		RFDBaseURL:     "https://forums.redflagdeals.com",
	}}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), defaults.HotDealsList.Elements)

	if deal.Title != "Great Deal Title" {
		t.Errorf("Title = %q, want %q", deal.Title, "Great Deal Title")
	}
	if !strings.HasSuffix(deal.PostURL, "/great-deal-12345") {
		t.Errorf("PostURL = %q, want suffix /great-deal-12345", deal.PostURL)
	}
	if deal.ThreadImageURL != "https://example.com/image.jpg" {
		t.Errorf("ThreadImageURL = %q, want %q", deal.ThreadImageURL, "https://example.com/image.jpg")
	}
	if deal.Threads[0].LikeCount != 42 {
		t.Errorf("LikeCount = %d, want 42", deal.Threads[0].LikeCount)
	}
	if deal.Threads[0].CommentCount != 15 {
		t.Errorf("CommentCount = %d, want 15", deal.Threads[0].CommentCount)
	}
	if deal.Threads[0].ViewCount != 1234 {
		t.Errorf("ViewCount = %d, want 1234", deal.Threads[0].ViewCount)
	}
	if deal.PublishedTimestamp.IsZero() {
		t.Error("PublishedTimestamp should be parsed, but was zero")
	}
}

func TestParseDealFromSelection_MinimalDeal(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/some-deal-999">Minimal Deal</a>
	</li>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}

	defaults := DefaultSelectors()
	c := &Client{selectors: defaults, config: &config.Config{
		AllowedDomains: []string{"forums.redflagdeals.com"},
		RFDBaseURL:     "https://forums.redflagdeals.com",
	}}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), defaults.HotDealsList.Elements)

	if deal.Title != "Minimal Deal" {
		t.Errorf("Title = %q, want %q", deal.Title, "Minimal Deal")
	}
	if deal.Threads[0].LikeCount != 0 {
		t.Errorf("LikeCount = %d, want 0", deal.Threads[0].LikeCount)
	}
	if deal.Threads[0].CommentCount != 0 {
		t.Errorf("CommentCount = %d, want 0", deal.Threads[0].CommentCount)
	}
	if deal.Threads[0].ViewCount != 0 {
		t.Errorf("ViewCount = %d, want 0", deal.Threads[0].ViewCount)
	}
}

func TestParseDealFromSelection_NegativeLikes(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/bad-deal-1">Bad Deal</a>
		<div class="thread_inner_footer">
			<span class="votes">-5</span>
		</div>
	</li>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}

	defaults := DefaultSelectors()
	c := &Client{selectors: defaults, config: &config.Config{
		AllowedDomains: []string{"forums.redflagdeals.com"},
		RFDBaseURL:     "https://forums.redflagdeals.com",
	}}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), defaults.HotDealsList.Elements)

	if deal.Threads[0].LikeCount != -5 {
		t.Errorf("LikeCount = %d, want -5", deal.Threads[0].LikeCount)
	}
}

func TestParseDealFromSelection_DataURIImageFiltered(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/deal-with-data-uri">Deal With Data URI</a>
		<div class="thread_image"><img src="data:image/png;base64,iVBORw0KGgoAAAANSUhEUg==" /></div>
	</li>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}

	defaults := DefaultSelectors()
	c := &Client{selectors: defaults, config: &config.Config{
		AllowedDomains: []string{"forums.redflagdeals.com"},
		RFDBaseURL:     "https://forums.redflagdeals.com",
	}}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), defaults.HotDealsList.Elements)

	if deal.ThreadImageURL != "" {
		t.Errorf("ThreadImageURL should be empty for data: URI, got %q", deal.ThreadImageURL)
	}
}

func TestParseDealFromSelection_RelativeImageFiltered(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/deal-relative-img">Deal With Relative Image</a>
		<div class="thread_image"><img src="/images/placeholder.jpg" /></div>
	</li>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}

	defaults := DefaultSelectors()
	c := &Client{selectors: defaults, config: &config.Config{
		AllowedDomains: []string{"forums.redflagdeals.com"},
		RFDBaseURL:     "https://forums.redflagdeals.com",
	}}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), defaults.HotDealsList.Elements)

	if deal.ThreadImageURL != "" {
		t.Errorf("ThreadImageURL should be empty for relative URL, got %q", deal.ThreadImageURL)
	}
}

func TestResolveLink(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		selector string
		wantHref string
		wantText string
	}{
		{
			name:     "Direct link",
			html:     `<div><a class="link" href="/page">Click</a></div>`,
			selector: ".link",
			wantHref: "https://forums.redflagdeals.com/page",
			wantText: "Click",
		},
		{
			name:     "Nested link",
			html:     `<div><span class="wrapper"><a href="https://example.com">External</a></span></div>`,
			selector: ".wrapper",
			wantHref: "https://example.com",
			wantText: "External",
		},
		{
			name:     "Missing selector",
			html:     `<div><a href="/page">Link</a></div>`,
			selector: ".nonexistent",
			wantHref: "",
			wantText: "",
		},
	}

	cfg := &config.Config{
		RFDBaseURL: "https://forums.redflagdeals.com",
	}
	c := &Client{config: cfg}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tt.html))
			if err != nil {
				t.Fatal(err)
			}
			href, text := c.resolveLink(doc.Selection, tt.selector)
			if href != tt.wantHref {
				t.Errorf("href = %q, want %q", href, tt.wantHref)
			}
			if text != tt.wantText {
				t.Errorf("text = %q, want %q", text, tt.wantText)
			}
		})
	}
}

func TestLoadSelectorsFromBytes(t *testing.T) {
	jsonData := []byte(`{
		"hot_deals_list": {
			"container": {"item": "li.deal", "ignore_modifier": ".ad"},
			"elements": {
				"title_link": ".title",
				"posted_time": ".time",
				"thread_image": ".img",
				"like_count": ".likes",
				"comment_count": ".comments",
				"comment_count_fallback": ".comments_alt",
				"view_count": ".views"
			}
		},
		"deal_details": {
			"primary_link": ".button",
			"fallback_link": ".link"
		}
	}`)

	cfg, err := LoadSelectorsFromBytes(jsonData)
	if err != nil {
		t.Fatalf("LoadSelectorsFromBytes() error = %v", err)
	}

	if cfg.HotDealsList.Container.Item != "li.deal" {
		t.Errorf("Container.Item = %q, want %q", cfg.HotDealsList.Container.Item, "li.deal")
	}
	if cfg.DealDetails.PrimaryLink != ".button" {
		t.Errorf("PrimaryLink = %q, want %q", cfg.DealDetails.PrimaryLink, ".button")
	}
}

func TestLoadSelectorsFromBytes_InvalidJSON(t *testing.T) {
	_, err := LoadSelectorsFromBytes([]byte(`{invalid`))
	if err == nil {
		t.Error("Expected error from invalid JSON")
	}
}

func TestDefaultSelectors(t *testing.T) {
	sel := DefaultSelectors()
	if sel.HotDealsList.Container.Item != "li.topic" {
		t.Errorf("Default Container.Item = %q, want %q", sel.HotDealsList.Container.Item, "li.topic")
	}
	if sel.DealDetails.PrimaryLink != ".deal_link a" {
		t.Errorf("Default PrimaryLink = %q, want %q", sel.DealDetails.PrimaryLink, ".deal_link a")
	}
}

func TestScrapeDealDetailPage_PrimaryLink(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
	<div class="deal_link"><a href="https://amazon.ca/dp/B001">Get Deal</a></div>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AllowedDomains: []string{"127.0.0.1"},
	}
	c := NewWithBaseURL(cfg, DefaultSelectors(), srv.URL)

	url, _, _, _, _, _, _, _, err := c.scrapeDealDetailPage(context.Background(), srv.URL+"/deal-page")
	if err != nil {
		t.Fatalf("scrapeDealDetailPage() error = %v", err)
	}
	if url != "https://amazon.ca/dp/B001" {
		t.Errorf("url = %q, want %q", url, "https://amazon.ca/dp/B001")
	}
}

func TestScrapeDealDetailPage_FallbackLink(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
	<a class="postlink" href="https://bestbuy.ca/product">Buy Now</a>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AllowedDomains: []string{"127.0.0.1"},
	}
	c := NewWithBaseURL(cfg, DefaultSelectors(), srv.URL)

	url, _, _, _, _, _, _, _, err := c.scrapeDealDetailPage(context.Background(), srv.URL+"/deal-page")
	if err != nil {
		t.Fatalf("scrapeDealDetailPage() error = %v", err)
	}
	if url != "https://bestbuy.ca/product" {
		t.Errorf("url = %q, want %q", url, "https://bestbuy.ca/product")
	}
}

func TestScrapeDealDetailPage_NoLink(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
	<p>No deal link on this page</p>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AllowedDomains: []string{"127.0.0.1"},
	}
	c := NewWithBaseURL(cfg, DefaultSelectors(), srv.URL)

	_, _, _, _, _, _, _, _, err := c.scrapeDealDetailPage(context.Background(), srv.URL+"/deal-page")
	if err != ErrDealLinkNotFound {
		t.Errorf("Expected ErrDealLinkNotFound, got %v", err)
	}
}

func TestScrapeDealDetailPage_PriceExtraction(t *testing.T) {
	html := `<!DOCTYPE html>
<html><body>
	<div class="deal_link"><a href="https://amazon.ca/dp/B001">Get Deal</a></div>
	<dl>
		<dt>Price:</dt><dd>$79.99</dd>
		<dt>Original Price:</dt><dd>$129.99</dd>
		<dt>Savings:</dt><dd>$50.00</dd>
	</dl>
</body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, html)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AllowedDomains: []string{"127.0.0.1"},
	}
	c := NewWithBaseURL(cfg, DefaultSelectors(), srv.URL)

	_, _, _, _, price, originalPrice, savings, _, err := c.scrapeDealDetailPage(context.Background(), srv.URL+"/deal-page")
	if err != nil {
		t.Fatalf("scrapeDealDetailPage() error = %v", err)
	}
	if price != "$79.99" {
		t.Errorf("price = %q, want %q", price, "$79.99")
	}
	if originalPrice != "$129.99" {
		t.Errorf("originalPrice = %q, want %q", originalPrice, "$129.99")
	}
	if savings != "$50.00" {
		t.Errorf("savings = %q, want %q", savings, "$50.00")
	}
}

func TestScrapeDealDetailPage_OriginalPriceAndSavings_Soundcore(t *testing.T) {
	// A new test checking if we can parse the testdata/soundcore.html file correctly
	// Read testdata/soundcore.html
	htmlBytes, err := os.ReadFile("../../testdata/soundcore.html")
	if err != nil {
		t.Skipf("Skipping test because testdata/soundcore.html is missing: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(htmlBytes)
	}))
	defer srv.Close()

	cfg := &config.Config{
		AllowedDomains: []string{"127.0.0.1"},
	}
	c := NewWithBaseURL(cfg, DefaultSelectors(), srv.URL)

	dealLink, _, _, _, price, originalPrice, savings, retailer, err := c.scrapeDealDetailPage(context.Background(), srv.URL+"/deal-page")
	if err != nil {
		t.Fatalf("scrapeDealDetailPage() error = %v", err)
	}
	if price != "$79.99" {
		t.Errorf("price = %q, want %q", price, "$79.99")
	}
	if originalPrice != "$129.99" {
		t.Errorf("originalPrice = %q, want %q", originalPrice, "$129.99")
	}
	if savings != "Save 38%" {
		t.Errorf("savings = %q, want %q", savings, "Save 38%")
	}
	if retailer != "Amazon.ca" {
		t.Errorf("retailer = %q, want %q", retailer, "Amazon.ca")
	}
	if !strings.Contains(dealLink, "amazon.ca") {
		t.Errorf("dealLink = %q, want something containing amazon.ca", dealLink)
	}
}

func TestFetchHTMLContent_DomainAllowlist(t *testing.T) {
	cfg := &config.Config{
		AllowedDomains: []string{"redflagdeals.com"},
	}
	c := New(cfg, DefaultSelectors())

	// Disallowed domain should be rejected
	_, err := c.fetchHTMLContent(context.Background(), "https://evil.com/page")
	if err == nil {
		t.Fatal("Expected error for disallowed domain")
	}
	if !strings.Contains(err.Error(), "not in allowlist") {
		t.Errorf("Expected allowlist error, got: %v", err)
	}
}
