package scraper

import (
	"encoding/json"
	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"os"
	"strings"
	"testing"
)

func TestExportRustParity(t *testing.T) {
	if os.Getenv("RUST_PARITY_OUTPUT") == "" {
		t.Skip("offline golden export requires RUST_PARITY_OUTPUT")
	}

	raw, e := os.ReadFile("../../testdata/mock_snippets.html")
	if e != nil {
		t.Fatal(e)
	}
	doc, e := goquery.NewDocumentFromReader(strings.NewReader(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	out := []map[string]any{}
	cfg := DefaultSelectors()
	c := &Client{selectors: cfg, config: &config.Config{AllowedDomains: []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com"}, RFDBaseURL: "https://forums.redflagdeals.com"}}
	for _, id := range []string{"full-deal", "minimal-deal", "negative-likes", "data-uri-image", "relative-image"} {
		s := doc.Find("#" + id)
		if s.Length() == 0 {
			continue
		}
		html, e := s.Html()
		if e != nil {
			t.Fatal(e)
		}
		d := c.parseDealFromSelection(s.Find("li.topic").First(), cfg.HotDealsList.Elements)
		out = append(out, map[string]any{"name": id, "html": html, "kind": "list", "expected": d})
	}
	for _, id := range []string{"primary-link", "fallback-link", "no-link", "price-extraction", "json-ld-fallback"} {
		s := doc.Find("#" + id)
		if s.Length() == 0 {
			continue
		}
		html, e := s.Html()
		if e != nil {
			t.Fatal(e)
		}
		doc, e := goquery.NewDocumentFromReader(strings.NewReader(html))
		if e != nil {
			t.Fatal(e)
		}
		d, e := c.parseDetailPage(doc)
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, map[string]any{"name": id, "html": html, "kind": "detail", "expected": d})
	}
	for _, file := range []string{"page.html"} {
		raw, e := os.ReadFile("../../testdata/" + file)
		if e != nil {
			t.Fatal(e)
		}
		doc, e := goquery.NewDocumentFromReader(strings.NewReader(string(raw)))
		if e != nil {
			t.Fatal(e)
		}
		d, e := c.parseDetailPage(doc)
		if e != nil {
			t.Fatal(e)
		}
		out = append(out, map[string]any{"name": file, "html": string(raw), "kind": "detail", "expected": d})
	}
	raw, e = json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("RUST_PARITY_OUTPUT"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
