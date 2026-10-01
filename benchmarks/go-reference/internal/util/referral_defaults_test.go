package util

import "testing"

func TestNoPersonalTrackingByDefault(t *testing.T) {
	for input, want := range map[string]string{
		"https://amazon.ca/dp/123?tag=someone-20": "https://amazon.ca/dp/123",
		"https://amazon.ca/dp/123":                "https://amazon.ca/dp/123",
		"https://bestbuy.ca/product":              "https://bestbuy.ca/product",
		"https://bestbuyca.o93x.net/c/123/456/789?u=https%3A%2F%2Fbestbuy.ca%2Fproduct": "https://bestbuy.ca/product",
		"https://www.ebay.ca/itm/123456789012?campid=example":                           "https://www.ebay.ca/itm/123456789012",
	} {
		got, _ := CleanReferralLink(input, "", "")
		if got != want {
			t.Errorf("%s became %s, want %s", input, got, want)
		}
	}
}
