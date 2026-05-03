package ebay

import "testing"

func TestExtractPageCoupon(t *testing.T) {
	tests := []struct {
		name      string
		html      string
		basePrice float64
		want      float64
		wantCode  string
	}{
		{
			name:      "fixed dollar coupon",
			html:      `<html><body><div>Save C$40.00 with coupon code SAVE40</div></body></html>`,
			basePrice: 500,
			want:      40,
			wantCode:  "SAVE40",
		},
		{
			name:      "percentage coupon",
			html:      `<html><body>Extra 15% off with code SAVE15</body></html>`,
			basePrice: 200,
			want:      30,
			wantCode:  "SAVE15",
		},
		{
			name:      "capped percentage coupon",
			html:      `<html><body>Extra 20% off up to C$25 with code SAVE20</body></html>`,
			basePrice: 200,
			want:      25,
			wantCode:  "SAVE20",
		},
		{
			name:      "no coupon",
			html:      `<html><body>Ships free. Seller refurbished.</body></html>`,
			basePrice: 200,
		},
		{
			name:      "invalid coupon text",
			html:      `<html><body>Use code soon for possible savings.</body></html>`,
			basePrice: 200,
		},
		{
			name:      "listing price is not a coupon",
			html:      `<html><body><h1>Server</h1><div>C$1,476.64</div><div>Free shipping</div></body></html>`,
			basePrice: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractPageCoupon(tt.html, tt.basePrice)
			if got.DiscountAmount != tt.want {
				t.Fatalf("discount = %v, want %v", got.DiscountAmount, tt.want)
			}
			if got.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", got.Code, tt.wantCode)
			}
		})
	}
}
