package bestbuy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestFetchSellerProductsWithAlgoliaBackend(t *testing.T) {
	client := NewClient()
	client.SetBackends([]string{BackendAlgolia})
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("X-Algolia-Application-Id"); got == "" {
			t.Fatal("missing Algolia application header")
		}
		rawBody, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("ReadAll() error = %v", err)
		}
		var body struct {
			Params string `json:"params"`
		}
		if err := json.Unmarshal(rawBody, &body); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		params, err := url.ParseQuery(body.Params)
		if err != nil {
			t.Fatalf("Algolia params are invalid: %v", err)
		}
		if got := params.Get("facetFilters"); got != `["seller.sellerName:Tech Outlet Center"]` {
			t.Fatalf("facetFilters = %q", got)
		}
		if got := params.Get("page"); got != "0" {
			t.Fatalf("page = %q, want zero-based Algolia page", got)
		}

		response := `{
			"page":0,
			"nbHits":1,
			"nbPages":1,
			"hits":[{
				"objectID":"123456",
				"sku":"123456",
				"title":"Refurbished Laptop",
				"imageUrl":"https://example.com/image.jpg",
				"categoryName":"Laptops",
				"seoText":"refurbished-laptop",
				"clearance":true,
				"seller":{"sellerId":"591375","sellerName":"Tech Outlet Center","marketplace":true},
				"price":{"regularPrice":500,"currentPrice":350},
				"rating":{"customerRating":4.2}
			}]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})}

	products, err := client.FetchSellerProducts(context.Background(), Seller{
		ID:         "591375",
		Name:       "Tech Outlet Center",
		SearchPath: "sellerName:Tech Outlet Center",
	})
	if err != nil {
		t.Fatalf("FetchSellerProducts() error = %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("products = %d, want 1", len(products))
	}
	product := products[0]
	if product.SKU != "123456" || product.Name != "Refurbished Laptop" {
		t.Fatalf("unexpected product: %#v", product)
	}
	if product.URL != "https://www.bestbuy.ca/en-ca/product/refurbished-laptop/123456" {
		t.Fatalf("URL = %q", product.URL)
	}
	if product.SalePrice != 350 || product.RegularPrice != 500 {
		t.Fatalf("prices = sale %.2f regular %.2f", product.SalePrice, product.RegularPrice)
	}
	if product.SellerID != "591375" || product.SellerName != "Tech Outlet Center" || !product.IsMarketplace {
		t.Fatalf("seller fields not mapped: %#v", product)
	}
	if product.Source != "seller:591375" {
		t.Fatalf("Source = %q", product.Source)
	}
}

func TestAlgoliaFacetFilterFromPath(t *testing.T) {
	tests := map[string]string{
		"sellerName:Parts Search":        "seller.sellerName:Parts Search",
		"seller.sellerName:Parts Search": "seller.sellerName:Parts Search",
		"sellerId:1247543":               "seller.sellerId:1247543",
		"category:Computers":             "category:Computers",
	}

	for input, want := range tests {
		if got := algoliaFacetFilterFromPath(input); got != want {
			t.Fatalf("algoliaFacetFilterFromPath(%q) = %q, want %q", input, got, want)
		}
	}
}
