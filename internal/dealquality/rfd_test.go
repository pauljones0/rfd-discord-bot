package dealquality

import (
	"testing"

	"github.com/pauljones0/rfd-discord-standalone/internal/models"
)

func TestEvaluateRFDWarmHotDiscount(t *testing.T) {
	tests := []struct {
		name string
		deal models.DealInfo
		want bool
	}{
		{
			name: "structured price below original",
			deal: models.DealInfo{Price: "$119.99", OriginalPrice: "$149.99"},
			want: true,
		},
		{
			name: "structured savings percent",
			deal: models.DealInfo{Title: "Headphones", Savings: "Save 38%"},
			want: true,
		},
		{
			name: "title price drop",
			deal: models.DealInfo{Title: "Samsung SSD price drop"},
			want: true,
		},
		{
			name: "popular high price without discount evidence",
			deal: models.DealInfo{Title: "New console at launch price", Price: "$999.99"},
			want: false,
		},
		{
			name: "current price not below original",
			deal: models.DealInfo{Price: "$999.99", OriginalPrice: "$799.99", Savings: "Save 10%"},
			want: false,
		},
		{
			name: "overpriced language blocks discount-looking text",
			deal: models.DealInfo{Title: "GPU clearance but overpriced", Savings: "Save 50%"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EvaluateRFDWarmHotDiscount(tt.deal)
			if got.Eligible != tt.want {
				t.Fatalf("Eligible = %v, want %v; evidence = %+v", got.Eligible, tt.want, got)
			}
		})
	}
}
