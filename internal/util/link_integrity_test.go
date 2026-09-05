package util

import (
	"net/url"
	"testing"
)

func TestCleanReferralLinkDecodesWrapperOnce(t *testing.T) {
	target := "https://store.example/products/A%2FB?query=C%2B%2B&filter=a%26b"
	for _, wrapper := range []string{"https://go.redirectingat.com/?url=", "https://click.linksynergy.com/?murl="} {
		raw := wrapper + url.QueryEscape(target)
		got, changed := CleanReferralLink(raw, "", "")
		if got != target || !changed {
			t.Fatalf("encoded destination changed: got %q want %q (changed=%v)", got, target, changed)
		}
	}
}

func TestCleanProductURLPreservesSearchIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, key, value string }{
		{"https://www.amazon.ca/s?k=coffee&tag=tracking&utm_source=fixture", "k", "coffee"},
		{"https://www.ebay.ca/sch/i.html?_nkw=coffee&_trksid=tracking&utm_source=fixture", "_nkw", "coffee"},
		{"https://www.bestbuy.ca/en-ca/search?search=coffee&cmp=tracking&utm_source=fixture", "search", "coffee"},
		{"https://www.bestbuy.com/site/searchpage.jsp?st=coffee&cmp=tracking", "st", "coffee"},
	} {
		got := CleanProductURL(tc.raw)
		u, err := url.Parse(got)
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get(tc.key) != tc.value || len(u.Query()) != 1 {
			t.Errorf("search identity/tracking mismatch: %q -> %q", tc.raw, got)
		}
	}
	for _, raw := range []string{
		"https://www.amazon.ca/s?filter=a%26b&k=C%2B%2B",
		"https://store.example/amazon.ca/search?product=coffee",
		"https://amazon.ca.store.example/dp/example?product=coffee",
		"https://notbestbuy.ca/search?product=coffee",
	} {
		if got := CleanProductURL(raw); got != raw {
			t.Errorf("destination changed: %q -> %q", raw, got)
		}
	}
}
