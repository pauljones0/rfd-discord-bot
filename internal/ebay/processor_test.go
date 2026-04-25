package ebay

import "testing"

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
