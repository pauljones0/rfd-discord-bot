package bestbuy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestFetchSellerProductsWithAlgoliaBackend(t *testing.T) {
	client := NewClient()
	client.SetBackends([]string{BackendAlgolia})
	requests := 0
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
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
		isRecentSweep := params.Get("filters") != ""

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
				"inStock":true,
				"isVisible":true,
				"brandName":"Lenovo",
				"modelNumber":"T14",
				"primaryUPC":"123456789012",
				"onlineOnly":true,
				"searchEndDate":253402214400000,
				"seller":{"sellerId":"591375","sellerName":"Tech Outlet Center","marketplace":true},
				"price":{"regularPrice":500,"currentPrice":350},
				"rating":{"customerRating":4.2}
			}]
		}`
		if isRecentSweep {
			response = `{
				"page":0,
				"nbHits":1,
				"nbPages":1,
				"hits":[
					{
						"objectID":"123456",
						"sku":"123456",
						"title":"Refurbished Laptop",
						"seller":{"sellerId":"591375","sellerName":"Tech Outlet Center","marketplace":true},
						"price":{"regularPrice":500,"currentPrice":350}
					},
					{
						"objectID":"789012",
						"sku":"789012",
						"title":"Newly Indexed Monitor",
						"seoText":"newly-indexed-monitor",
						"lastIndex":"2026-05-10T17:23:21Z",
						"indexTimestamp":1778433804372,
						"searchStartDate":1770413455000,
						"inStock":true,
						"isVisible":true,
						"seller":{"sellerId":"591375","sellerName":"Tech Outlet Center","marketplace":true},
						"price":{"regularPrice":300,"currentPrice":200}
					}
				]
			}`
		}
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
	if requests != 2 {
		t.Fatalf("requests = %d, want broad sweep plus recent-index sweep", requests)
	}
	if len(products) != 2 {
		t.Fatalf("products = %d, want 2", len(products))
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
	if !product.InStockKnown || !product.InStock || !product.VisibilityKnown || !product.IsVisible || !product.OnlineOnly {
		t.Fatalf("availability fields not mapped: %#v", product)
	}
	if product.BrandName != "Lenovo" || product.ModelNumber != "T14" || product.PrimaryUPC != "123456789012" {
		t.Fatalf("identity fields not mapped: %#v", product)
	}
	if product.Source != "seller:591375" {
		t.Fatalf("Source = %q", product.Source)
	}
	recent := products[1]
	if recent.SKU != "789012" || recent.IndexTimestamp != 1778433804372 || recent.LastIndex == "" {
		t.Fatalf("recent sweep product not mapped: %#v", recent)
	}
}

func TestValidateSellerOfferRejectsExpiredOffer(t *testing.T) {
	client := NewClient()
	client.SetBackends([]string{BackendAlgolia})
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/offers") {
			return bestBuyJSONResponse(req, `[{
				"sku":"17389711",
				"sellerId":"1247543",
				"sellerNameEn":"Parts Search",
				"regularPrice":259.99,
				"salePrice":89.99,
				"offerEndDate":"2023-10-11T17:00:05Z",
				"isMarketplace":true
			}]`), nil
		}
		return bestBuyJSONResponse(req, `{
			"page":0,
			"nbHits":1,
			"nbPages":1,
			"hits":[{
				"objectID":"17389711",
				"sku":"17389711",
				"title":"Refurbished AMD Ryzen 5 5600X",
				"inStock":true,
				"isVisible":true,
				"searchEndDate":253402214400000,
				"seller":{"sellerId":"1247543","sellerName":"Parts Search","marketplace":true},
				"price":{"regularPrice":259.99,"currentPrice":89.99}
			}]
		}`), nil
	})}

	validation, err := client.ValidateSellerOffer(context.Background(), Product{
		SKU:          "17389711",
		Name:         "Refurbished AMD Ryzen 5 5600X",
		RegularPrice: 259.99,
		SalePrice:    89.99,
		SellerID:     "1247543",
		SellerName:   "Parts Search",
		Source:       "seller:1247543",
	}, time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ValidateSellerOffer() error = %v", err)
	}
	if validation.Valid || validation.Reason != "seller_offer_expired" {
		t.Fatalf("validation = %#v, want expired rejection", validation)
	}
}

func TestValidateSellerOfferRejectsPriceIncrease(t *testing.T) {
	client := NewClient()
	client.SetBackends([]string{BackendAlgolia})
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/offers") {
			return bestBuyJSONResponse(req, `[{
				"sku":"111",
				"sellerId":"591375",
				"sellerNameEn":"Tech Outlet Center",
				"regularPrice":500,
				"salePrice":450,
				"offerEndDate":"9999-12-31T00:00:00Z",
				"isMarketplace":true
			}]`), nil
		}
		return bestBuyJSONResponse(req, `{
			"page":0,
			"nbHits":1,
			"nbPages":1,
			"hits":[{
				"objectID":"111",
				"sku":"111",
				"title":"Refurbished Laptop",
				"inStock":true,
				"isVisible":true,
				"searchEndDate":253402214400000,
				"seller":{"sellerId":"591375","sellerName":"Tech Outlet Center","marketplace":true},
				"price":{"regularPrice":500,"currentPrice":450}
			}]
		}`), nil
	})}

	validation, err := client.ValidateSellerOffer(context.Background(), Product{
		SKU:          "111",
		Name:         "Refurbished Laptop",
		RegularPrice: 500,
		SalePrice:    300,
		SellerID:     "591375",
		SellerName:   "Tech Outlet Center",
		Source:       "seller:591375",
	}, time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ValidateSellerOffer() error = %v", err)
	}
	if validation.Valid || validation.Reason != "price_increased" {
		t.Fatalf("validation = %#v, want price increase rejection", validation)
	}
}

func TestEnrichComparablesUsesOffersAndAlgolia(t *testing.T) {
	client := NewClient()
	client.SetBackends([]string{BackendAlgolia})
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/offers") {
			return bestBuyJSONResponse(req, `[
				{"sku":"19867564","sellerId":"other","sellerNameEn":"Other Seller","regularPrice":499,"salePrice":450,"offerEndDate":"9999-12-31T00:00:00Z","isMarketplace":true},
				{"sku":"19867564","sellerId":"1247543","sellerNameEn":"Parts Search","regularPrice":499,"salePrice":300,"offerEndDate":"9999-12-31T00:00:00Z","isMarketplace":true}
			]`), nil
		}
		return bestBuyJSONResponse(req, `{
			"page":0,
			"nbHits":1,
			"nbPages":1,
			"hits":[{
				"objectID":"999",
				"sku":"999",
				"title":"Refurbished (Good) Apple MacBook Air M1 512GB",
				"inStock":true,
				"isVisible":true,
				"searchEndDate":253402214400000,
				"seller":{"sellerId":"seller2","sellerName":"Seller 2","marketplace":true},
				"price":{"regularPrice":599,"currentPrice":500}
			}]
		}`), nil
	})}

	product, err := client.EnrichComparables(context.Background(), Product{
		SKU:          "19867564",
		Name:         "Refurbished (Good) Apple MacBook Air M1 16GB 512GB",
		RegularPrice: 499,
		SalePrice:    300,
		SellerID:     "1247543",
		SellerName:   "Parts Search",
		Source:       "seller:1247543",
		PrimaryUPC:   "upc",
	}, time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EnrichComparables() error = %v", err)
	}
	if product.ComparableCount != 2 {
		t.Fatalf("ComparableCount = %d, want 2; summary=%q", product.ComparableCount, product.ComparableSummary)
	}
	if product.ComparableMedianPrice != 475 || product.ComparableLowestPrice != 450 {
		t.Fatalf("comparable prices median=%.2f low=%.2f", product.ComparableMedianPrice, product.ComparableLowestPrice)
	}
	if !strings.Contains(product.ComparableSummary, "$475.00 median") {
		t.Fatalf("ComparableSummary = %q", product.ComparableSummary)
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

func bestBuyJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
