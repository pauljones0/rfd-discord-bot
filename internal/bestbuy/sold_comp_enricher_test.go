package bestbuy

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

type soldCompMemoryStore struct {
	snapshots map[string]SoldCompSnapshot
	gets      int
	saves     int
}

func (s *soldCompMemoryStore) GetBestBuySoldCompSnapshot(_ context.Context, key string) (SoldCompSnapshot, bool, error) {
	s.gets++
	snapshot, ok := s.snapshots[key]
	return snapshot, ok, nil
}

func (s *soldCompMemoryStore) SaveBestBuySoldCompSnapshot(_ context.Context, key string, snapshot SoldCompSnapshot) error {
	s.saves++
	if s.snapshots == nil {
		s.snapshots = make(map[string]SoldCompSnapshot)
	}
	s.snapshots[key] = snapshot
	return nil
}

func TestBuildBestBuySoldCompQueryAndEligibility(t *testing.T) {
	product := Product{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 349}
	query := buildBestBuySoldCompQuery(product)
	if query != "Sony WH-1000XM5" {
		t.Fatalf("query = %q, want brand/model", query)
	}
	if !eligibleForBestBuySoldComps(product, query) {
		t.Fatal("expected product to be eligible")
	}
	if eligibleForBestBuySoldComps(Product{Name: "USB-C Cable", SalePrice: 149}, "USB-C Cable") {
		t.Fatal("accessory should be ineligible")
	}
	if eligibleForBestBuySoldComps(Product{Name: "Sony Headphones", SalePrice: 99.99}, "Sony Headphones") {
		t.Fatal("sub-$100 item should be ineligible")
	}
	if eligibleForBestBuySoldComps(Product{Name: "Mystery", SalePrice: 500}, "") {
		t.Fatal("empty query should be ineligible")
	}
}

func TestBuildBestBuySoldCompQueryUsesAppleWatchFamily(t *testing.T) {
	product := Product{
		Name:         "Refurbished (Fair) - Apple Watch Series 8 (GPS) 41mm Silver Aluminum Case with White Sport Band - Small / Medium",
		CategoryName: "Apple Watch",
		BrandName:    "APPLE",
		ModelNumber:  "MP6K3VC/A",
		SalePrice:    229.99,
		Specs: map[string]string{
			"custom0watchseries":    "Series 8 (GPS)",
			"custom0casediametermm": "41",
		},
	}
	if got := buildBestBuySoldCompQuery(product); got != "Apple Watch Series 8 GPS 41mm" {
		t.Fatalf("query = %q, want Apple Watch family query", got)
	}
}

func TestScoreBestBuySoldCompsSummarizesMedianAndExamples(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	product := Product{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 300}
	listings := []ebay.SoldListing{
		{Title: "Sony WH-1000XM5 Wireless Noise Cancelling Headphones", Price: 420},
		{Title: "Sony WH1000XM5 Headphones Black", Price: 460},
		{Title: "Sony WH-1000XM4 Headphones", Price: 250},
	}
	snapshot := scoreBestBuySoldComps(product, listings, "Sony WH-1000XM5", "http", "key", now)
	if snapshot.Count != 2 {
		t.Fatalf("Count = %d, want 2", snapshot.Count)
	}
	if snapshot.Median != 440 || snapshot.P25 != 430 {
		t.Fatalf("median/p25 = %.2f/%.2f, want 440/430", snapshot.Median, snapshot.P25)
	}
	var enriched Product
	applySoldCompSnapshot(&enriched, snapshot)
	if enriched.SoldCompCount != 2 || !strings.Contains(enriched.SoldCompSummary, "eBay sold comps") {
		t.Fatalf("enriched product missing sold summary: %#v", enriched)
	}
	if len(enriched.SoldCompExamples) != 2 {
		t.Fatalf("examples = %d, want 2", len(enriched.SoldCompExamples))
	}
}

func TestSoldCompEnricherUsesFreshCache(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	product := Product{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 300}
	query := buildBestBuySoldCompQuery(product)
	key := soldCompCacheKey(query)
	store := &soldCompMemoryStore{snapshots: map[string]SoldCompSnapshot{key: {Key: key, Query: query, Verdict: ebaySoldVerdictPass, Count: 2, Median: 450, P25: 425, GapAmount: 150, GapPct: 33, CheckedAt: now}}}
	fetches := 0
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{Enabled: true, Store: store, FetchHTML: func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult {
		fetches++
		return scrapebackend.FetchResult{}
	}})
	enriched, err := enricher.EnrichProducts(context.Background(), []Product{product}, now.Add(time.Hour), nil)
	if err != nil {
		t.Fatalf("EnrichProducts() error = %v", err)
	}
	if fetches != 0 {
		t.Fatalf("fetches = %d, want 0 cache hit", fetches)
	}
	if enriched[0].SoldCompMedianPrice != 450 {
		t.Fatalf("SoldCompMedianPrice = %.2f, want cached 450", enriched[0].SoldCompMedianPrice)
	}
}

func TestSoldCompEnricherCapsUncachedFetchesPerRun(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	store := &soldCompMemoryStore{snapshots: map[string]SoldCompSnapshot{}}
	fetches := 0
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{
		Enabled:   true,
		Store:     store,
		MaxPerRun: 1,
		Sleep:     func(context.Context, time.Duration) error { return nil },
		FetchHTML: func(_ context.Context, opts scrapebackend.FetchOptions) scrapebackend.FetchResult {
			fetches++
			return scrapebackend.FetchResult{HTML: ebaySoldHTML(
				SoldCompListing{Title: "Sony WH-1000XM5 Wireless Noise Cancelling Headphones", Price: 420},
				SoldCompListing{Title: "Sony WH1000XM5 Headphones Black", Price: 460},
			)}
		},
	})
	enricher.BeginRun()
	products := []Product{
		{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 300, ComparableCount: 3, ComparableMedianPrice: 600, ComparableLowestPrice: 420},
		{Name: "Apple iPad Pro 11 M2", BrandName: "Apple", ModelNumber: "MNXD3VC/A", SalePrice: 500},
	}
	enriched, err := enricher.EnrichProducts(context.Background(), products, now, nil)
	if err != nil {
		t.Fatalf("EnrichProducts() error = %v", err)
	}
	if fetches != 1 {
		t.Fatalf("fetches = %d, want cap of 1", fetches)
	}
	if enriched[0].SoldCompCount == 0 || enriched[1].SoldCompCount != 0 {
		t.Fatalf("enriched counts = %d/%d, want first only", enriched[0].SoldCompCount, enriched[1].SoldCompCount)
	}
}

func TestSoldCompEnricherDelaysBetweenBackendAttempts(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	fetches := 0
	delays := 0
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{
		Enabled:    true,
		Backends:   []string{scrapebackend.BackendHTTP, scrapebackend.BackendCamoufox},
		MaxPerRun:  5,
		QueryDelay: 7 * time.Second,
		Sleep: func(context.Context, time.Duration) error {
			delays++
			return nil
		},
		FetchHTML: func(_ context.Context, opts scrapebackend.FetchOptions) scrapebackend.FetchResult {
			fetches++
			if fetches == 1 {
				return scrapebackend.FetchResult{Error: "blocked"}
			}
			return scrapebackend.FetchResult{HTML: ebaySoldHTML(
				SoldCompListing{Title: "Sony WH-1000XM5 Wireless Noise Cancelling Headphones", Price: 420},
				SoldCompListing{Title: "Sony WH1000XM5 Headphones Black", Price: 460},
			)}
		},
	})
	enricher.BeginRun()
	_, err := enricher.EnrichProducts(context.Background(), []Product{{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 300}}, now, nil)
	if err != nil {
		t.Fatalf("EnrichProducts() error = %v", err)
	}
	if fetches != 2 || delays != 1 {
		t.Fatalf("fetches/delays = %d/%d, want 2/1", fetches, delays)
	}
}

func TestSoldCompEnricherCachesNoComps(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	store := &soldCompMemoryStore{snapshots: map[string]SoldCompSnapshot{}}
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{Enabled: true, Store: store, MaxPerRun: 5, Sleep: func(context.Context, time.Duration) error { return nil }, FetchHTML: func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult {
		return scrapebackend.FetchResult{HTML: "<html></html>"}
	}})
	products := []Product{{Name: "Sony WH-1000XM5 Noise Cancelling Headphones", BrandName: "Sony", ModelNumber: "WH-1000XM5", SalePrice: 300}}
	enriched, err := enricher.EnrichProducts(context.Background(), products, now, nil)
	if err != nil {
		t.Fatalf("EnrichProducts() error = %v", err)
	}
	if enriched[0].SoldCompCount != 0 {
		t.Fatalf("SoldCompCount = %d, want 0 for no comps", enriched[0].SoldCompCount)
	}
	if store.saves != 1 {
		t.Fatalf("cache saves = %d, want 1 no-comps snapshot", store.saves)
	}
	for _, snapshot := range store.snapshots {
		if snapshot.Verdict != ebaySoldVerdictNoComps {
			t.Fatalf("cached verdict = %q, want no_comps", snapshot.Verdict)
		}
	}
}

func TestNewSoldCompEnricherDefaultCapIsTopTen(t *testing.T) {
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{})
	if enricher.maxPerRun != 10 {
		t.Fatalf("maxPerRun = %d, want 10", enricher.maxPerRun)
	}
}

func TestEbaySoldBackendsSkipPaidTrialWhenDisabled(t *testing.T) {
	backends := ebaySoldBackends([]string{scrapebackend.BackendHTTP, scrapebackend.BackendPaidTrial, scrapebackend.BackendAICrawler}, false)
	want := []string{scrapebackend.BackendHTTP, scrapebackend.BackendAICrawler}
	if !reflect.DeepEqual(backends, want) {
		t.Fatalf("backends = %#v, want %#v", backends, want)
	}

	backends = ebaySoldBackends([]string{scrapebackend.BackendHTTP, scrapebackend.BackendPaidTrial, scrapebackend.BackendAICrawler}, true)
	want = []string{scrapebackend.BackendHTTP, scrapebackend.BackendAICrawler, scrapebackend.BackendPaidTrial}
	if !reflect.DeepEqual(backends, want) {
		t.Fatalf("paid-enabled backends = %#v, want %#v", backends, want)
	}
}

func TestSoldCompEnricherRanksTopTenUncachedCandidatesAndSkipsCached(t *testing.T) {
	now := time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC)
	products := make([]Product, 0, 12)
	for i := 0; i < 12; i++ {
		model := fmt.Sprintf("MODEL%02d", i)
		median := 900 + float64(i)*100
		products = append(products, Product{
			SKU:                   fmt.Sprintf("sku-%02d", i),
			Name:                  fmt.Sprintf("Brand %s Deal", model),
			BrandName:             "Brand",
			ModelNumber:           model,
			SalePrice:             500,
			ComparableCount:       3,
			ComparableMedianPrice: median,
			ComparableLowestPrice: median * 0.8,
			ComparableDiscountPct: (median - 500) / median * 100,
		})
	}

	cachedQuery := buildBestBuySoldCompQuery(products[11])
	cachedKey := soldCompCacheKey(cachedQuery)
	store := &soldCompMemoryStore{snapshots: map[string]SoldCompSnapshot{
		cachedKey: {Key: cachedKey, Query: cachedQuery, Verdict: ebaySoldVerdictPass, Count: 2, Median: 1600, P25: 1500, GapAmount: 1100, GapPct: 69, CheckedAt: now},
	}}
	fetchedQueries := []string{}
	enricher := NewSoldCompEnricher(SoldCompEnricherOptions{
		Enabled:   true,
		Store:     store,
		MaxPerRun: 10,
		Sleep:     func(context.Context, time.Duration) error { return nil },
		FetchHTML: func(_ context.Context, opts scrapebackend.FetchOptions) scrapebackend.FetchResult {
			parsed, err := url.Parse(opts.URL)
			if err != nil {
				t.Fatalf("parse fetch URL: %v", err)
			}
			query := parsed.Query().Get("_nkw")
			fetchedQueries = append(fetchedQueries, query)
			return scrapebackend.FetchResult{HTML: ebaySoldHTML(
				SoldCompListing{Title: query, Price: 1200},
				SoldCompListing{Title: query + " excellent", Price: 1300},
			)}
		},
	})
	enricher.BeginRun()
	enriched, err := enricher.EnrichProducts(context.Background(), products, now, nil)
	if err != nil {
		t.Fatalf("EnrichProducts() error = %v", err)
	}
	if len(fetchedQueries) != 10 {
		t.Fatalf("fetches = %d, want top 10 uncached candidates", len(fetchedQueries))
	}
	if containsString(fetchedQueries, buildBestBuySoldCompQuery(products[0])) {
		t.Fatalf("lowest-ranked query was fetched: %v", fetchedQueries)
	}
	if containsString(fetchedQueries, cachedQuery) {
		t.Fatalf("cached query should not be fetched: %v", fetchedQueries)
	}
	if !containsString(fetchedQueries, buildBestBuySoldCompQuery(products[10])) {
		t.Fatalf("high-ranked uncached query missing from fetches: %v", fetchedQueries)
	}
	if enriched[11].SoldCompMedianPrice != 1600 {
		t.Fatalf("cached product median = %.2f, want 1600", enriched[11].SoldCompMedianPrice)
	}
	if enriched[0].SoldCompCount != 0 {
		t.Fatalf("lowest-ranked product should not be enriched, got count %d", enriched[0].SoldCompCount)
	}
}

func TestSoldCompMarketVerdictThresholdsByValueBand(t *testing.T) {
	tests := []struct {
		name    string
		current float64
		median  float64
		p25     float64
		want    string
	}{
		{name: "low value warm", current: 210, median: 260, p25: 240, want: soldCompMarketWarm},
		{name: "low value hot", current: 90, median: 260, p25: 200, want: soldCompMarketHot},
		{name: "mid value warm", current: 700, median: 900, p25: 850, want: soldCompMarketWarm},
		{name: "high value hot", current: 1100, median: 2000, p25: 1700, want: soldCompMarketHot},
		{name: "high value weak", current: 1850, median: 2000, p25: 1700, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := soldCompMarketVerdictFromSummary(tt.current, 3, tt.median, tt.p25, 3)
			if got.Label != tt.want {
				t.Fatalf("Label = %q, want %q; verdict=%#v", got.Label, tt.want, got)
			}
		})
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func ebaySoldHTML(items ...SoldCompListing) string {
	var b strings.Builder
	b.WriteString("<ul>")
	for _, item := range items {
		b.WriteString(fmt.Sprintf(`<li class="s-item"><div class="s-item__title">%s</div><span class="s-item__price">C $%.2f</span></li>`, item.Title, item.Price))
	}
	b.WriteString("</ul>")
	return b.String()
}
