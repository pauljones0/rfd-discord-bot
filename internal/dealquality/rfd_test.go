package dealquality

import (
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
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

func TestFreeFormPercentagesRequireDiscountContext(t *testing.T) {
	for _, title := range []string{"100% cotton bath towels", "99% alcohol cleaner", "100% organic oats"} {
		for _, inSummary := range []bool{false, true} {
			deal := models.DealInfo{Title: title, Price: "$50"}
			if inSummary {
				deal.Title, deal.Summary = "Product", title
			}
			if evidence := EvaluateRFDWarmHotDiscount(deal); evidence.Eligible {
				t.Errorf("product attribute qualified without discount evidence: %+v evidence=%+v", deal, evidence)
			}
			deal.OriginalPrice = "$100"
			if evidence := EvaluateRFDWarmHotDiscount(deal); !evidence.Eligible {
				t.Errorf("product attribute blocked a real structured discount: %+v evidence=%+v", deal, evidence)
			}
		}
	}
	for _, title := range []string{"20% off towels", "20%off towels", "Save 20% on towels", "Towels with 20% cashback", "Towels with 20% coupon", "Towels with a 20% discount", "100% cotton towels, now 20% off"} {
		if evidence := EvaluateRFDWarmHotDiscount(models.DealInfo{Title: title}); !evidence.Eligible || evidence.DiscountPercent != 20 {
			t.Errorf("explicit discount percentage was lost: title=%q evidence=%+v", title, evidence)
		}
	}
	for _, savings := range []string{"20%", "$20"} {
		if evidence := EvaluateRFDWarmHotDiscount(models.DealInfo{Title: "100% cotton bath towels", Savings: savings}); !evidence.Eligible {
			t.Errorf("structured savings stopped qualifying: savings=%q evidence=%+v", savings, evidence)
		}
	}
}
