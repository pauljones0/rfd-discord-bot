package ebay

import (
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestShouldNotifyPriceDrop(t *testing.T) {
	tests := []struct {
		name         string
		existing     TrackedItem
		newPrice     float64
		wantBaseline float64
		wantDrop     float64
		wantPercent  float64
		wantNotify   bool
	}{
		{
			name: "first alert uses original price baseline",
			existing: TrackedItem{
				Price:         390,
				OriginalPrice: 500,
			},
			newPrice:     380,
			wantBaseline: 500,
			wantDrop:     120,
			wantPercent:  24,
			wantNotify:   true,
		},
		{
			name: "legacy items fall back to tracked price baseline",
			existing: TrackedItem{
				Price: 500,
			},
			newPrice:     350,
			wantBaseline: 500,
			wantDrop:     150,
			wantPercent:  30,
			wantNotify:   true,
		},
		{
			name: "same previously alerted price is suppressed",
			existing: TrackedItem{
				Price:             500,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     350,
			wantBaseline: 350,
			wantDrop:     0,
			wantPercent:  0,
			wantNotify:   false,
		},
		{
			name: "higher than last alerted price is suppressed",
			existing: TrackedItem{
				Price:             500,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     360,
			wantBaseline: 350,
			wantDrop:     0,
			wantPercent:  0,
			wantNotify:   false,
		},
		{
			name: "deeper drop uses last alerted price baseline",
			existing: TrackedItem{
				Price:             340,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     280,
			wantBaseline: 350,
			wantDrop:     70,
			wantPercent:  20,
			wantNotify:   true,
		},
		{
			name: "deeper price without threshold does not notify",
			existing: TrackedItem{
				Price:             340,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     330,
			wantBaseline: 350,
			wantDrop:     20,
			wantPercent:  20.0 / 350.0 * 100.0,
			wantNotify:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBaseline, gotDrop, gotPercent, gotNotify := shouldNotifyPriceDrop(tt.existing, tt.newPrice)
			if gotBaseline != tt.wantBaseline {
				t.Fatalf("baseline = %v, want %v", gotBaseline, tt.wantBaseline)
			}
			if gotDrop != tt.wantDrop {
				t.Fatalf("drop = %v, want %v", gotDrop, tt.wantDrop)
			}
			if gotPercent != tt.wantPercent {
				t.Fatalf("percent = %v, want %v", gotPercent, tt.wantPercent)
			}
			if gotNotify != tt.wantNotify {
				t.Fatalf("notify = %v, want %v", gotNotify, tt.wantNotify)
			}
		})
	}
}

func TestCouponAdjustedPriceCountsAsPriceDrop(t *testing.T) {
	newPrice := effectiveItemPrice(500, 120)
	if newPrice != 380 {
		t.Fatalf("effective price = %v, want 380", newPrice)
	}

	baseline, drop, percent, notify := shouldNotifyPriceDrop(TrackedItem{
		Price:         500,
		OriginalPrice: 500,
	}, newPrice)
	if !notify {
		t.Fatalf("notify = false, want true")
	}
	if baseline != 500 || drop != 120 || percent != 24 {
		t.Fatalf("baseline/drop/percent = %v/%v/%v, want 500/120/24", baseline, drop, percent)
	}
}

func TestCouponIncreaseCountsAsDeeperPriceDrop(t *testing.T) {
	newPrice := effectiveItemPrice(500, 180)
	if newPrice != 320 {
		t.Fatalf("effective price = %v, want 320", newPrice)
	}

	baseline, drop, percent, notify := shouldNotifyPriceDrop(TrackedItem{
		Price:             380,
		OriginalPrice:     500,
		LastNotifiedPrice: 400,
	}, newPrice)
	if !notify {
		t.Fatalf("notify = false, want true")
	}
	if baseline != 400 || drop != 80 || percent != 20 {
		t.Fatalf("baseline/drop/percent = %v/%v/%v, want 400/80/20", baseline, drop, percent)
	}
}

func TestShouldFetchPageCouponOnlyAfterBaseOrAPIDrop(t *testing.T) {
	tests := []struct {
		name              string
		existing          TrackedItem
		basePrice         float64
		apiCouponDiscount float64
		want              bool
	}{
		{
			name: "base price drop triggers page coupon discovery",
			existing: TrackedItem{
				Price:     500,
				BasePrice: 500,
			},
			basePrice: 450,
			want:      true,
		},
		{
			name: "new page coupon alone does not trigger discovery path",
			existing: TrackedItem{
				Price:     500,
				BasePrice: 500,
			},
			basePrice: 500,
			want:      false,
		},
		{
			name: "larger api coupon that lowers effective price triggers",
			existing: TrackedItem{
				Price:          480,
				BasePrice:      500,
				CouponDiscount: 20,
			},
			basePrice:         500,
			apiCouponDiscount: 80,
			want:              true,
		},
		{
			name: "same api coupon does not trigger",
			existing: TrackedItem{
				Price:          480,
				BasePrice:      500,
				CouponDiscount: 20,
			},
			basePrice:         500,
			apiCouponDiscount: 20,
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFetchPageCoupon(tt.existing, tt.basePrice, tt.apiCouponDiscount); got != tt.want {
				t.Fatalf("shouldFetchPageCoupon() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPriorDropCount(t *testing.T) {
	tests := []struct {
		name     string
		existing TrackedItem
		want     int
	}{
		{
			name: "uses stored count when present",
			existing: TrackedItem{
				DropCount:         3,
				LastNotifiedPrice: 250,
			},
			want: 3,
		},
		{
			name: "legacy alerted item counts as at least one prior drop",
			existing: TrackedItem{
				LastNotifiedPrice: 250,
			},
			want: 1,
		},
		{
			name:     "never-alerted item starts at zero",
			existing: TrackedItem{},
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := priorDropCount(tt.existing); got != tt.want {
				t.Fatalf("priorDropCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsEbayEligible_MarketplaceFilters(t *testing.T) {
	caItem := EbayItem{Marketplace: "EBAY_CA"}
	usItem := EbayItem{Marketplace: "EBAY_US"}

	tests := []struct {
		name string
		item EbayItem
		sub  models.Subscription
		want bool
	}{
		{
			name: "canadian filter accepts canadian item",
			item: caItem,
			sub:  models.Subscription{DealType: "ebay_ca_price_drop"},
			want: true,
		},
		{
			name: "canadian filter rejects us item",
			item: usItem,
			sub:  models.Subscription{DealType: "ebay_ca_price_drop"},
			want: false,
		},
		{
			name: "us filter accepts us item",
			item: usItem,
			sub:  models.Subscription{DealType: "ebay_us_price_drop"},
			want: true,
		},
		{
			name: "legacy all ebay filter accepts canadian item",
			item: caItem,
			sub:  models.Subscription{DealType: "ebay_price_drop"},
			want: true,
		},
		{
			name: "marketplace falls back to item url",
			item: EbayItem{ItemURL: "https://www.ebay.com/itm/123"},
			sub:  models.Subscription{DealType: "ebay_us_price_drop"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEbayEligible(tt.item, tt.sub); got != tt.want {
				t.Fatalf("isEbayEligible() = %v, want %v", got, tt.want)
			}
		})
	}
}
