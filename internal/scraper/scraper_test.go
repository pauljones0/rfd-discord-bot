package scraper

import (
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

func TestParseDealFromSelection_FullDeal(t *testing.T) {
	html := `<li class="topic">
		<a class="thread_title_link" href="/great-deal-12345">Great Deal Title</a>
		<div class="thread_image"><img src="https://example.com/image.jpg" /></div>
		<div class="thread_inner_footer">
			<span class="author_info">
				<time datetime="2025-01-15T10:30:00Z">Jan 15, 2025</time>
				<a class="author" href="/users/testuser"><span class="author_name">TestUser</span></a>
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

	c := &Client{selectors: DefaultSelectors}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), DefaultSelectors.HotDealsList.Elements)

	if deal.Title != "Great Deal Title" {
		t.Errorf("Title = %q, want %q", deal.Title, "Great Deal Title")
	}
	if !strings.HasSuffix(deal.PostURL, "/great-deal-12345") {
		t.Errorf("PostURL = %q, want suffix /great-deal-12345", deal.PostURL)
	}
	if deal.ThreadImageURL != "https://example.com/image.jpg" {
		t.Errorf("ThreadImageURL = %q, want %q", deal.ThreadImageURL, "https://example.com/image.jpg")
	}
	if deal.LikeCount != 42 {
		t.Errorf("LikeCount = %d, want 42", deal.LikeCount)
	}
	if deal.CommentCount != 15 {
		t.Errorf("CommentCount = %d, want 15", deal.CommentCount)
	}
	if deal.ViewCount != 1234 {
		t.Errorf("ViewCount = %d, want 1234", deal.ViewCount)
	}
	if deal.AuthorName != "TestUser" {
		t.Errorf("AuthorName = %q, want %q", deal.AuthorName, "TestUser")
	}
	if deal.PostedTime != "2025-01-15T10:30:00Z" {
		t.Errorf("PostedTime = %q, want %q", deal.PostedTime, "2025-01-15T10:30:00Z")
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

	c := &Client{selectors: DefaultSelectors}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), DefaultSelectors.HotDealsList.Elements)

	if deal.Title != "Minimal Deal" {
		t.Errorf("Title = %q, want %q", deal.Title, "Minimal Deal")
	}
	if deal.LikeCount != 0 {
		t.Errorf("LikeCount = %d, want 0", deal.LikeCount)
	}
	if deal.CommentCount != 0 {
		t.Errorf("CommentCount = %d, want 0", deal.CommentCount)
	}
	if deal.ViewCount != 0 {
		t.Errorf("ViewCount = %d, want 0", deal.ViewCount)
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

	c := &Client{selectors: DefaultSelectors}
	deal := c.parseDealFromSelection(doc.Find("li.topic").First(), DefaultSelectors.HotDealsList.Elements)

	if deal.LikeCount != -5 {
		t.Errorf("LikeCount = %d, want -5", deal.LikeCount)
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := goquery.NewDocumentFromReader(strings.NewReader(tt.html))
			if err != nil {
				t.Fatal(err)
			}
			href, text := resolveLink(doc.Selection, tt.selector)
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
				"author_link": ".author",
				"author_name": ".name",
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
