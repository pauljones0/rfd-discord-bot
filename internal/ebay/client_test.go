package ebay

import (
	"strings"
	"testing"
	"time"
)

func TestBuildBrowseQueryParams_UsesTechCategoryAndSinceTime(t *testing.T) {
	since := time.Date(2026, time.April, 16, 12, 34, 56, 0, time.UTC)

	params := buildBrowseQueryParams("58058", "seller1|seller2", 400, since)

	if got := params.Get("category_ids"); got != "58058" {
		t.Fatalf("category_ids = %q, want %q", got, "58058")
	}
	if got := params.Get("offset"); got != "400" {
		t.Fatalf("offset = %q, want %q", got, "400")
	}
	filter := params.Get("filter")
	if !strings.Contains(filter, "sellers:{seller1|seller2}") {
		t.Fatalf("filter = %q, want sellers clause", filter)
	}
	if !strings.Contains(filter, "buyingOptions:{FIXED_PRICE}") {
		t.Fatalf("filter = %q, want fixed-price clause", filter)
	}
	if !strings.Contains(filter, "itemStartDate:[2026-04-16T12:34:56Z..]") {
		t.Fatalf("filter = %q, want sinceTime clause", filter)
	}
}

func TestAppendUniqueBrowseItems_DeduplicatesAcrossCategories(t *testing.T) {
	seen := map[string]struct{}{
		"v1|111|0": {},
	}
	initial := []BrowseAPIItem{
		{ItemID: "v1|111|0", Title: "first"},
	}
	items := []BrowseAPIItem{
		{ItemID: "v1|111|0", Title: "duplicate"},
		{ItemID: "v1|222|0", Title: "second"},
	}

	result := appendUniqueBrowseItems(initial, items, seen)

	if len(result) != 2 {
		t.Fatalf("len(result) = %d, want 2", len(result))
	}
	if result[0].ItemID != "v1|111|0" {
		t.Fatalf("first item = %q, want %q", result[0].ItemID, "v1|111|0")
	}
	if result[1].ItemID != "v1|222|0" {
		t.Fatalf("second item = %q, want %q", result[1].ItemID, "v1|222|0")
	}
}
