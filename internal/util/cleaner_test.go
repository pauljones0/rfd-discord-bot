package util

import (
	"testing"
)

func TestCleanProductURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		// Amazon Tests
		{
			name:     "Amazon simple dp tracking",
			input:    "https://www.amazon.ca/dp/B07YF3JQF8/ref=pd_bxgy_d_sccl_1/130-0878020-0818067?pd_rd_w=Xo1Zs&content-id=amzn1.sym",
			expected: "https://www.amazon.ca/dp/B07YF3JQF8",
		},
		{
			name:     "Amazon with product name",
			input:    "https://www.amazon.com/Apple-AirPods-Pro-2nd-Gen/dp/B0D1XD1ZV3/ref=sr_1_1?crid=123",
			expected: "https://www.amazon.com/dp/B0D1XD1ZV3",
		},
		{
			name:     "Amazon gp format",
			input:    "https://amazon.ca/gp/product/B07YF3JQF8/ref=ox_sc_act_title_1?smid=A1M4A2O2C9P4N4&psc=1",
			expected: "https://amazon.ca/dp/B07YF3JQF8?psc=1&smid=A1M4A2O2C9P4N4", // Keep psc and smid
		},
		{
			name:     "Amazon with th (variant)",
			input:    "https://www.amazon.com/dp/B08P2H15Y?th=1&psc=1&ref_=nav_em",
			expected: "https://www.amazon.com/dp/B08P2H15Y?psc=1&th=1", // keep th, psc
		},
		
		// eBay Tests
		{
			name:     "eBay simple itm",
			input:    "https://www.ebay.ca/itm/134954474751?_skw=laptop&_trkparms=ispr%3D1&hash=item1f6bf870ff:g:abc",
			expected: "https://www.ebay.ca/itm/134954474751",
		},
		{
			name:     "eBay with product name",
			input:    "https://www.ebay.com/itm/Apple-MacBook-Pro-16-inch-M3-Pro/123456789012?amdata=enc%3A123",
			expected: "https://www.ebay.com/itm/123456789012",
		},
		{
			name:     "eBay with Product ID p/",
			input:    "https://www.ebay.com/p/12345?iid=134954474751&thm=1000",
			expected: "https://www.ebay.com/p/12345",
		},

		// BestBuy Tests
		{
			name:     "BestBuy Canada",
			input:    "https://www.bestbuy.ca/en-ca/product/apple-airpods-pro-2nd-generation-with-magsafe-charging-case-usb-c/17395420?cmp=seo-17395420&irclickid=abc",
			expected: "https://www.bestbuy.ca/en-ca/product/apple-airpods-pro-2nd-generation-with-magsafe-charging-case-usb-c/17395420",
		},
		{
			name:     "BestBuy US",
			input:    "https://www.bestbuy.com/site/apple-airpods-pro-2nd-generation/6536962.p?loc=137454&cmp=RMX&ref=199",
			expected: "https://www.bestbuy.com/site/apple-airpods-pro-2nd-generation/6536962.p",
		},

		// Other Unsupported URLs
		{
			name:     "Other domain",
			input:    "https://www.homedepot.ca/product/dewalt-20v-max/10001234?custom=123",
			expected: "https://www.homedepot.ca/product/dewalt-20v-max/10001234?custom=123", // unchanged
		},
		{
			name:     "Malformed URL",
			input:    "://bad-url",
			expected: "://bad-url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanProductURL(tt.input)
			if got != tt.expected {
				t.Errorf("CleanProductURL() = %v, want %v", got, tt.expected)
			}
		})
	}
}
