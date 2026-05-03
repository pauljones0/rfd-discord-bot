package scrapelab

import "testing"

func TestCountBestBuyProductsFromInitialState(t *testing.T) {
	html := `<html><script>window.__INITIAL_STATE__ = {"search":{"searchResult":{"products":[{"sku":"1"},{"sku":"2"}]}}};</script></html>`

	got, err := countBestBuyProducts(html)
	if err != nil {
		t.Fatalf("countBestBuyProducts returned error: %v", err)
	}
	if got != 2 {
		t.Fatalf("countBestBuyProducts = %d, want 2", got)
	}
}

func TestCountBestBuyProductsFromLoadedEmptySearchShell(t *testing.T) {
	html := `<html><div class="productsContainer_2xEUC"><div class="productListingContainer_1Iyio"></div></div></html>`

	got, err := countBestBuyProducts(html)
	if err != nil {
		t.Fatalf("countBestBuyProducts returned error for loaded empty shell: %v", err)
	}
	if got != 0 {
		t.Fatalf("countBestBuyProducts = %d, want 0", got)
	}
}

func TestCountBestBuyProductsRejectsUnrelatedBraces(t *testing.T) {
	html := `<html><script>var styles = {color: "red"};</script></html>`

	if _, err := countBestBuyProducts(html); err == nil {
		t.Fatal("expected error for unrelated page with braces")
	}
}

func TestBestBuySellerFromTargetUsesSearchPath(t *testing.T) {
	seller := bestBuySellerFromTarget(Target{
		Site: "bestbuy",
		URL:  "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search",
	})

	if seller.Name != "Parts Search" {
		t.Fatalf("Name = %q, want Parts Search", seller.Name)
	}
	if seller.SearchPath != "sellerName:Parts Search" {
		t.Fatalf("SearchPath = %q", seller.SearchPath)
	}
}
