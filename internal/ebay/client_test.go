package ebay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

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

func TestBrowseSearchResponse_UnmarshalsCouponFlag(t *testing.T) {
	var resp BrowseSearchResponse
	raw := []byte(`{"itemSummaries":[{"itemId":"v1|111|0","availableCoupons":true,"itemHref":"https://api.ebay.com/buy/browse/v1/item/v1%7C111%7C0"}]}`)

	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(resp.ItemSummaries) != 1 {
		t.Fatalf("len(itemSummaries) = %d, want 1", len(resp.ItemSummaries))
	}
	if !resp.ItemSummaries[0].AvailableCoupons {
		t.Fatalf("availableCoupons = false, want true")
	}
	if resp.ItemSummaries[0].ItemHref == "" {
		t.Fatalf("itemHref = empty, want populated")
	}
}

func TestTotalCouponSnapshot_SumsUniqueCodes(t *testing.T) {
	coupon := totalCouponSnapshot([]AvailableCoupon{
		{
			DiscountAmount: &Price{Value: "15.00", Currency: "CAD"},
			RedemptionCode: "SAVE15",
			Message:        "Save $15",
		},
		{
			DiscountAmount: &Price{Value: "40.00", Currency: "CAD"},
			RedemptionCode: "SAVE40",
			Message:        "Save $40",
		},
		{
			DiscountAmount: &Price{Value: "10.00", Currency: "CAD"},
			RedemptionCode: "SAVE15", // Duplicate code with smaller amount
			Message:        "Smaller Save $15",
		},
		{
			DiscountAmount: &Price{Value: "5.00", Currency: "CAD"},
			RedemptionCode: "", // Automatic discount
			Message:        "Auto $5",
		},
	})

	if coupon.DiscountAmount != 60 { // 40 + 15 + 5
		t.Fatalf("discount = %v, want 60", coupon.DiscountAmount)
	}
	if coupon.Code != "SAVE15+SAVE40" {
		t.Fatalf("code = %q, want SAVE15+SAVE40", coupon.Code)
	}
	if coupon.Message != "Auto $5; Save $15; Save $40" {
		t.Fatalf("message = %q, want 'Auto $5; Save $15; Save $40'", coupon.Message)
	}
}

func TestEbayCouponExternalCommandPrefersSiteSpecificEnv(t *testing.T) {
	t.Setenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND", "global-camoufox")
	t.Setenv("EBAY_COUPON_EXTERNAL_STEALTH_COMMAND", "ebay-camoufox")

	if got := ebayCouponExternalCommand(); got != "ebay-camoufox" {
		t.Fatalf("ebayCouponExternalCommand() = %q, want site-specific command", got)
	}
}

func TestEbayCouponExternalCommandFallsBackToScrapeLabEnv(t *testing.T) {
	t.Setenv("EBAY_COUPON_EXTERNAL_STEALTH_COMMAND", "")
	t.Setenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND", "global-camoufox")

	if got := ebayCouponExternalCommand(); got != "global-camoufox" {
		t.Fatalf("ebayCouponExternalCommand() = %q, want global fallback", got)
	}
}

func TestEbayCouponPaidCommandPrefersSiteSpecificEnv(t *testing.T) {
	t.Setenv("SCRAPELAB_PAID_TRIAL_COMMAND", "global-browserless")
	t.Setenv("EBAY_COUPON_PAID_TRIAL_COMMAND", "ebay-browserless")

	if got := ebayCouponPaidCommand(); got != "ebay-browserless" {
		t.Fatalf("ebayCouponPaidCommand() = %q, want site-specific command", got)
	}
}

func TestEbayCouponPaidCommandFallsBackToScrapeLabEnv(t *testing.T) {
	t.Setenv("EBAY_COUPON_PAID_TRIAL_COMMAND", "")
	t.Setenv("SCRAPELAB_PAID_TRIAL_COMMAND", "global-browserless")

	if got := ebayCouponPaidCommand(); got != "global-browserless" {
		t.Fatalf("ebayCouponPaidCommand() = %q, want global fallback", got)
	}
}

func TestBrowseItemDetailURL_FallsBackToEncodedItemID(t *testing.T) {
	got := browseItemDetailURL(BrowseAPIItem{ItemID: "v1|111|0"})
	want := ebayBrowseItemURL + "/v1%7C111%7C0"
	if got != want {
		t.Fatalf("browseItemDetailURL() = %q, want %q", got, want)
	}
}

func TestSearchSellerListings_ReturnsErrorWhenCategoryFetchFails(t *testing.T) {
	client := &Client{
		clientID:     "id",
		clientSecret: "secret",
		httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dns lookup failed")
		})},
	}

	_, err := client.SearchSellerListings(context.Background(), []EbaySeller{
		{Username: "seller", CategoryIDs: []string{"58058"}},
	}, time.Time{})
	if err == nil {
		t.Fatalf("SearchSellerListings() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "failed to fetch") || !strings.Contains(err.Error(), "dns lookup failed") {
		t.Fatalf("SearchSellerListings() error = %q, want fetch failure with original cause", err)
	}
}

func TestEbaySellerEffectiveCategoryIDs(t *testing.T) {
	seller := EbaySeller{}

	if got := seller.EffectiveCategoryIDs(); !reflect.DeepEqual(got, browseTechCategoryIDs) {
		t.Fatalf("default category ids = %v, want %v", got, browseTechCategoryIDs)
	}

	seller.CategoryIDs = []string{"58058", "", "293", "58058"}
	if got := seller.EffectiveCategoryIDs(); !reflect.DeepEqual(got, []string{"58058", "293"}) {
		t.Fatalf("custom category ids = %v, want %v", got, []string{"58058", "293"})
	}
}

func TestBuildMarketplaceCategoryGroups_UsesSellerCategoryScopes(t *testing.T) {
	groups := buildMarketplaceCategoryGroups([]EbaySeller{
		{Username: "seller-ca-a", CategoryIDs: []string{"58058", "293"}},
		{Username: "seller-ca-b", CategoryIDs: []string{"293"}},
		{Username: "seller-us", Marketplace: "EBAY_US", CategoryIDs: []string{"1249"}},
	})

	if len(groups) != 2 {
		t.Fatalf("len(groups) = %d, want 2", len(groups))
	}

	if groups[0].marketplace != "EBAY_CA" {
		t.Fatalf("groups[0].marketplace = %q, want %q", groups[0].marketplace, "EBAY_CA")
	}
	if got := groups[0].categorySellers["58058"]; !reflect.DeepEqual(got, []string{"seller-ca-a"}) {
		t.Fatalf("EBAY_CA 58058 sellers = %v, want %v", got, []string{"seller-ca-a"})
	}
	if got := groups[0].categorySellers["293"]; !reflect.DeepEqual(got, []string{"seller-ca-a", "seller-ca-b"}) {
		t.Fatalf("EBAY_CA 293 sellers = %v, want %v", got, []string{"seller-ca-a", "seller-ca-b"})
	}

	if groups[1].marketplace != "EBAY_US" {
		t.Fatalf("groups[1].marketplace = %q, want %q", groups[1].marketplace, "EBAY_US")
	}
	if got := groups[1].categorySellers["1249"]; !reflect.DeepEqual(got, []string{"seller-us"}) {
		t.Fatalf("EBAY_US 1249 sellers = %v, want %v", got, []string{"seller-us"})
	}
}
