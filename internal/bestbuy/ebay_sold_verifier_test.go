package bestbuy

import (
	"strings"
	"testing"
)

func TestParseEbaySoldListings(t *testing.T) {
	html := `
<ul>
  <li class="s-item">
    <div class="s-item__title">New Listing Dell Precision 5820 Xeon W-2133 32GB RAM Quadro P4000</div>
    <span class="s-item__price">C $2,499.99</span>
  </li>
  <li class="s-item">
    <div class="s-item__title">HP Z440 Workstation</div>
    <span class="s-item__price">US $899.00</span>
  </li>
  <li class="s-item">
    <div class="s-item__title">Shop on eBay</div>
    <span class="s-item__price">C $1.00</span>
  </li>
</ul>`

	listings, err := ParseEbaySoldListings(html)
	if err != nil {
		t.Fatalf("ParseEbaySoldListings() error = %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("listings = %d, want 1: %#v", len(listings), listings)
	}
	if listings[0].Title != "Dell Precision 5820 Xeon W-2133 32GB RAM Quadro P4000" {
		t.Fatalf("title = %q", listings[0].Title)
	}
	if listings[0].Price != 2499.99 {
		t.Fatalf("price = %.2f, want 2499.99", listings[0].Price)
	}
}

func TestParseEbaySoldListingsNewCardLayout(t *testing.T) {
	html := `
<ul>
  <li>
    <div class="su-card-container">
      <a>
        <div class="s-card__title">
          <span class="su-styled-text primary default">Dell Precision 5820, Xeon W-2133, 32GB RAM, 512GB NVMe, P4000, Win 11 Pro</span>
        </div>
      </a>
      <span class="su-styled-text primary bold large-1 s-card__price">C $619.00</span>
    </div>
  </li>
  <li>
    <div class="su-card-container">
      <a><div class="s-card__title"><span class="su-styled-text primary default">Shop on eBay</span></div></a>
      <span class="s-card__price">$20.00</span>
    </div>
  </li>
</ul>`

	listings, err := ParseEbaySoldListings(html)
	if err != nil {
		t.Fatalf("ParseEbaySoldListings() error = %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("listings = %d, want 1: %#v", len(listings), listings)
	}
	if listings[0].Title != "Dell Precision 5820, Xeon W-2133, 32GB RAM, 512GB NVMe, P4000, Win 11 Pro" {
		t.Fatalf("title = %q", listings[0].Title)
	}
	if listings[0].Price != 619 {
		t.Fatalf("price = %.2f, want 619", listings[0].Price)
	}
}

func TestEbaySoldVerificationPassesBelowSoldMedian(t *testing.T) {
	observation := soldVerifierObservation(650)
	listings := []ebaySoldListing{
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000", Price: 2400},
		{Title: "Dell Precision 5820 Workstation Xeon W-2133 32GB RAM Quadro P4000", Price: 2500},
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM Quadro P4000", Price: 2600},
	}

	got := scoreEbaySoldVerification(observation, listings, 3, 25, 100)
	if !got.Pass {
		t.Fatalf("Pass = false, verdict=%s error=%s", got.Verdict, got.Error)
	}
	if got.ComparableCount != 3 {
		t.Fatalf("ComparableCount = %d, want 3", got.ComparableCount)
	}
	if got.MedianPrice != 2500 {
		t.Fatalf("MedianPrice = %.2f, want 2500", got.MedianPrice)
	}
}

func TestEbaySoldVerificationFailsWeakGap(t *testing.T) {
	observation := soldVerifierObservation(2300)
	listings := []ebaySoldListing{
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000", Price: 2400},
		{Title: "Dell Precision 5820 Workstation Xeon W-2133 32GB RAM Quadro P4000", Price: 2500},
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM Quadro P4000", Price: 2600},
	}

	got := scoreEbaySoldVerification(observation, listings, 3, 25, 100)
	if got.Pass {
		t.Fatalf("Pass = true, want false")
	}
	if got.Verdict != ebaySoldVerdictFail {
		t.Fatalf("Verdict = %q, want %q", got.Verdict, ebaySoldVerdictFail)
	}
}

func TestEbaySoldListingRequiresGPUMatch(t *testing.T) {
	observation := soldVerifierObservation(650)
	listings := []ebaySoldListing{
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro K2000", Price: 2500},
		{Title: "Dell Precision 5820 Workstation Xeon W-2133 32GB RAM no graphics card", Price: 2600},
		{Title: "Dell Precision 5820 Xeon W-2133 32GB RAM", Price: 2700},
	}

	got := scoreEbaySoldVerification(observation, listings, 3, 25, 100)
	if got.Pass {
		t.Fatalf("Pass = true, want false")
	}
	if got.ComparableCount != 0 {
		t.Fatalf("ComparableCount = %d, want 0", got.ComparableCount)
	}
}

func TestEbaySoldVerificationAllowsLowerRAMForExtremeServers(t *testing.T) {
	product := Product{
		SKU:       "extreme",
		Name:      "Dell PowerEdge R740 768GB RAM 24 Core Xeon Server",
		SalePrice: 1,
		Source:    "seller:test",
	}
	observation := ComputeObservation{Product: product, Spec: ParseComputeSpec(product)}
	listings := []ebaySoldListing{
		{Title: "Dell PowerEdge R740 128GB RAM 24 Core Xeon Server", Price: 1500},
		{Title: "Dell PowerEdge R740 256GB RAM 24 Core Xeon Server", Price: 2000},
		{Title: "Dell PowerEdge R740 384GB RAM 24 Core Xeon Server", Price: 2500},
	}

	got := scoreEbaySoldVerification(observation, listings, 3, 25, 100)
	if !got.Pass {
		t.Fatalf("Pass = false for extreme lower-RAM floor comps, verdict=%s error=%s count=%d", got.Verdict, got.Error, got.ComparableCount)
	}
	if len(got.Comparables) != 3 {
		t.Fatalf("Comparables = %d, want stored parsed comparables", len(got.Comparables))
	}
}

func TestBuildEbaySoldQueryUsesStructuredSpec(t *testing.T) {
	observation := soldVerifierObservation(650)
	query := buildEbaySoldQuery(observation)

	for _, want := range []string{"Dell", "Precision", "5820", "32GB RAM", "P4000"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query %q does not contain %q", query, want)
		}
	}
	if strings.Contains(strings.ToLower(query), "open box") {
		t.Fatalf("query %q should not include condition filler", query)
	}
}

func soldVerifierObservation(price float64) ComputeObservation {
	product := Product{
		SKU:       "sold-test",
		Name:      "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice: price,
		Source:    "seller:test",
	}
	return ComputeObservation{
		Product: product,
		Spec:    ParseComputeSpec(product),
	}
}
