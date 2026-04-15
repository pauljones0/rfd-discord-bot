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
