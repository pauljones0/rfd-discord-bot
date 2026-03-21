package facebook

import (
	"testing"
)

// Test data captured from real Facebook Marketplace scraping (Toronto, March 2026).
// The jsScrapeMarketplace JS returned these exact text arrays from the DOM.

func TestProcessRawAd_StandardListing(t *testing.T) {
	// 2022 VW Taos: standard listing with price, title, location, mileage
	adMap := map[string]interface{}{
		"id": "26252673021030748",
		"texts": []interface{}{
			"CA$19,980",
			"2022 Volkswagen taos trendline awd",
			"Toronto, ON",
			"65K km",
		},
	}

	sub := &FacebookScrapeConfig{}
	ad, id, ok := processRawAd(adMap, sub)

	if !ok {
		t.Fatal("expected processRawAd to succeed")
	}
	if id != "26252673021030748" {
		t.Errorf("expected id '26252673021030748', got %q", id)
	}
	if ad.Title != "2022 Volkswagen taos trendline awd" {
		t.Errorf("expected title '2022 Volkswagen taos trendline awd', got %q", ad.Title)
	}
	if ad.Price != 19980 {
		t.Errorf("expected price 19980, got %f", ad.Price)
	}
	if ad.Mileage != "65K km" {
		t.Errorf("expected mileage '65K km', got %q", ad.Mileage)
	}
	if ad.URL != "https://www.facebook.com/marketplace/item/26252673021030748/" {
		t.Errorf("unexpected URL: %q", ad.URL)
	}
}

func TestProcessRawAd_ReducedPriceListing(t *testing.T) {
	// Ford e-450: reduced price listing with TWO price elements before the title.
	// Real observation: reduced-price listings show [new price, old price, title, ...].
	adMap := map[string]interface{}{
		"id": "1334361137856575",
		"texts": []interface{}{
			"CA$22,000",
			"CA$24,500",
			"1990 Ford e-450",
			"Toronto, ON",
		},
	}

	sub := &FacebookScrapeConfig{}
	ad, _, ok := processRawAd(adMap, sub)

	if !ok {
		t.Fatal("expected processRawAd to succeed for reduced-price listing")
	}
	// The bot takes the FIRST price (new/reduced price)
	if ad.Price != 22000 {
		t.Errorf("expected first (reduced) price 22000, got %f", ad.Price)
	}
	if ad.Title != "1990 Ford e-450" {
		t.Errorf("expected title '1990 Ford e-450', got %q", ad.Title)
	}
}

func TestProcessRawAd_MotorcycleListing(t *testing.T) {
	// Suzuki sv650: motorcycle listing with standard format
	adMap := map[string]interface{}{
		"id": "1298991852146800",
		"texts": []interface{}{
			"CA$2,500",
			"2003 Suzuki sv650",
			"Burlington, ON",
			"138K km",
		},
	}

	sub := &FacebookScrapeConfig{}
	ad, _, ok := processRawAd(adMap, sub)

	if !ok {
		t.Fatal("expected processRawAd to succeed")
	}
	if ad.Price != 2500 {
		t.Errorf("expected price 2500, got %f", ad.Price)
	}
	if ad.Title != "2003 Suzuki sv650" {
		t.Errorf("expected title '2003 Suzuki sv650', got %q", ad.Title)
	}
}

func TestProcessRawAd_BrandFilter(t *testing.T) {
	adMap := map[string]interface{}{
		"id": "953797707048729",
		"texts": []interface{}{
			"CA$4,000",
			"2001 Porsche boxter s",
			"Markham, ON",
			"150K km",
		},
	}

	// Filter for Honda only — should reject Porsche
	sub := &FacebookScrapeConfig{FilterBrands: []string{"Honda", "Toyota"}}
	_, _, ok := processRawAd(adMap, sub)
	if ok {
		t.Error("expected processRawAd to reject Porsche when filtering for Honda/Toyota")
	}

	// Filter for Porsche — should accept
	sub = &FacebookScrapeConfig{FilterBrands: []string{"Porsche"}}
	ad, _, ok := processRawAd(adMap, sub)
	if !ok {
		t.Fatal("expected processRawAd to accept Porsche")
	}
	if ad.Title != "2001 Porsche boxter s" {
		t.Errorf("unexpected title: %q", ad.Title)
	}
}

func TestProcessRawAd_NoPriceSkipped(t *testing.T) {
	// Listing with no price indicator
	adMap := map[string]interface{}{
		"id":    "123",
		"texts": []interface{}{"2020 Honda Civic", "Toronto, ON"},
	}

	sub := &FacebookScrapeConfig{}
	_, _, ok := processRawAd(adMap, sub)
	if ok {
		t.Error("expected processRawAd to reject ad without price")
	}
}

func TestProcessRawAd_FreeListing(t *testing.T) {
	adMap := map[string]interface{}{
		"id":    "456",
		"texts": []interface{}{"Free", "Old car parts", "Toronto, ON"},
	}

	sub := &FacebookScrapeConfig{}
	// "Free" doesn't contain "$", so it won't match the price detection
	_, _, ok := processRawAd(adMap, sub)
	if ok {
		t.Error("expected processRawAd to reject 'Free' listing (no $ sign)")
	}
}

func TestProcessRawAd_TooFewTexts(t *testing.T) {
	adMap := map[string]interface{}{
		"id":    "789",
		"texts": []interface{}{"$5,000"},
	}

	sub := &FacebookScrapeConfig{}
	_, _, ok := processRawAd(adMap, sub)
	if ok {
		t.Error("expected processRawAd to reject ad with only 1 text element")
	}
}

func TestProcessRawAd_NoID(t *testing.T) {
	adMap := map[string]interface{}{
		"texts": []interface{}{"$5,000", "2020 Honda Civic"},
	}

	sub := &FacebookScrapeConfig{}
	_, _, ok := processRawAd(adMap, sub)
	if ok {
		t.Error("expected processRawAd to reject ad without id")
	}
}

func TestBuildMarketplaceURL(t *testing.T) {
	tests := []struct {
		city     string
		category string
		radius   int
		wantURL  string
		wantErr  bool
	}{
		{
			city:     "Toronto",
			category: "Vehicles",
			radius:   500,
			wantURL:  "https://www.facebook.com/marketplace/110941395597405/vehicles/?exact=false&radius=500",
		},
		{
			city:     "Vancouver",
			category: "Vehicles",
			radius:   100,
			wantURL:  "https://www.facebook.com/marketplace/114497808567786/vehicles/?exact=false&radius=100",
		},
		{
			city:     "Toronto",
			category: "Vehicles",
			radius:   0,
			wantURL:  "https://www.facebook.com/marketplace/110941395597405/vehicles/?exact=false&radius=500",
		},
		{
			city:    "Fake City",
			category: "Vehicles",
			radius:  500,
			wantErr: true,
		},
		{
			city:     "Toronto",
			category: "Fake Category",
			radius:   500,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.city+"_"+tt.category, func(t *testing.T) {
			got, err := BuildMarketplaceURL(tt.city, tt.category, tt.radius)
			if (err != nil) != tt.wantErr {
				t.Errorf("BuildMarketplaceURL(%q, %q, %d) error = %v, wantErr %v", tt.city, tt.category, tt.radius, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantURL {
				t.Errorf("BuildMarketplaceURL(%q, %q, %d) = %q, want %q", tt.city, tt.category, tt.radius, got, tt.wantURL)
			}
		})
	}
}
