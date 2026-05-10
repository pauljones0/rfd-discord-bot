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
		{
			name: "price details stacked coupons",
			html: `<html><body>
				<section role="dialog" aria-label="Price details">
					<h2>Price details</h2>
					<div>Item price C$300.00</div>
					<div>Seller coupon SAVE20 -C$20.00</div>
					<div>Store promo -C$10.00</div>
					<div>Total C$270.00</div>
				</section>
			</body></html>`,
			basePrice: 300,
			want:      30,
			wantCode:  "SAVE20",
		},
		{
			name: "live ebay price details discount row",
			html: `<html><body>
				<div role="dialog" aria-label="Price details">
					<h2>Price details</h2>
					<div>Item price C $201.00</div>
					<div>Shipping FREE</div>
					<div>Discounts -C $20.10 MOTHERDAYS10 Automatically applied during checkout</div>
					<div>Estimated total C $180.90</div>
					<div>You save: C $20.10</div>
				</div>
			</body></html>`,
			basePrice: 201,
			want:      20.10,
			wantCode:  "MOTHERDAYS10",
		},
		{
			name: "price transparency discounted price fallback",
			html: `<html><body>
				<div class="x-price-transparency">
					<span>C $180.90 with coupon code</span>
					<button>Price details</button>
				</div>
			</body></html>`,
			basePrice: 201,
			want:      20.10,
		},
		{
			name: "price details does not treat final price as coupon",
			html: `<html><body>
				<section role="dialog" aria-label="Price details">
					<h2>Price details</h2>
					<div>Item price C$300.00</div>
					<div>Shipping Free</div>
					<div>Total C$300.00</div>
				</section>
			</body></html>`,
			basePrice: 300,
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
