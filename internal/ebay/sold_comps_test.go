package ebay

import "testing"

func TestVerifyDeal(t *testing.T) {
	listings := []SoldListing{
		{Title: "iPhone 15 Pro Max 256GB", Price: 1200},
		{Title: "iPhone 15 Pro Max Blue 256GB", Price: 1250},
		{Title: "iPhone 15 Pro Max Unlocked", Price: 1150},
	}

	tests := []struct {
		name         string
		title        string
		currentPrice float64
		wantGood     bool
	}{
		{
			name:         "Good deal - significantly below median",
			title:        "iPhone 15 Pro Max",
			currentPrice: 1000,
			wantGood:     true,
		},
		{
			name:         "Not a good deal - near median",
			title:        "iPhone 15 Pro Max",
			currentPrice: 1150,
			wantGood:     false,
		},
		{
			name:         "Not enough comps",
			title:        "Non-existent item",
			currentPrice: 500,
			wantGood:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := VerifyDeal(tt.title, tt.currentPrice, listings)
			if res.IsGoodDeal != tt.wantGood {
				t.Errorf("VerifyDeal() IsGoodDeal = %v, want %v", res.IsGoodDeal, tt.wantGood)
			}
		})
	}
}

func TestSoldListingMatches(t *testing.T) {
	tests := []struct {
		target    string
		candidate string
		wantMatch bool
	}{
		{"iPhone 15 Pro Max", "iPhone 15 Pro Max Blue", true},
		{"iPhone 15 Pro Max", "Samsung Galaxy S24", false},
		{"Sony WH-1000XM5", "Sony Headphones WH1000XM5", true},
	}

	for _, tt := range tests {
		if got := soldListingMatches(tt.target, tt.candidate); got != tt.wantMatch {
			t.Errorf("soldListingMatches(%q, %q) = %v, want %v", tt.target, tt.candidate, got, tt.wantMatch)
		}
	}
}
