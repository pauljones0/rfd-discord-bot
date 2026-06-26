package crux

import "testing"

func TestParseCompaniesPage_MainListOnly(t *testing.T) {
	html := `<!doctype html><html><body>
<div class="splide"><a class="company-card is-vertical" href="/companies/nope"><div class="cc_content"><div class="cc_title-text">Company Name</div><div class="body">Carousel Co</div></div><div class="cc_content"><div class="cc_title-text">Ticker</div><div class="body">TSXV:NOPE</div></div></a></div>
<div class="comp-list_col-list-wrap w-dyn-list"><div fs-cmsload-element="list" class="comp-list_article-grid w-dyn-items">
<a class="company-card w-inline-block" href="/companies/santacruz-silver-mining">
<div class="cc_content"><div class="cc_title-text">Company Name</div><div class="body body-small">Santacruz Silver Mining</div></div>
<div class="cc_content"><div class="cc_title-text">Crux Investor Index</div><div class="crux-score"><div fs-cmssort-field="crux">8</div></div></div>
<div class="cc_content"><div class="cc_title-text">Ticker</div><div class="body body-small">TSXV:SCZ</div></div>
<div class="cc_content"><div class="cc_title-text">Development Stage</div><div class="body body-small">Production</div></div>
<div class="cc_content"><div class="cc_title-text">Primary Commodity</div><div class="body body-small">Silver</div></div>
</a>
</div><div class="w-page-count" aria-label="Page 3 of 78">3 / 78</div></div>
</body></html>`

	companies, totalPages, err := ParseCompaniesPage(html, "https://www.cruxinvestor.com/companies?97d0d7a7_page=3")
	if err != nil {
		t.Fatalf("ParseCompaniesPage() error = %v", err)
	}
	if totalPages != 78 {
		t.Fatalf("totalPages = %d, want 78", totalPages)
	}
	if len(companies) != 1 {
		t.Fatalf("len(companies) = %d, want 1", len(companies))
	}
	company := companies[0]
	if company.Key != "TSXV:SCZ" || company.Name != "Santacruz Silver Mining" || company.CruxScore != 8 || !company.HasCruxScore {
		t.Fatalf("company = %#v", company)
	}
	if company.URL != "https://www.cruxinvestor.com/companies/santacruz-silver-mining" {
		t.Fatalf("URL = %q", company.URL)
	}
}
