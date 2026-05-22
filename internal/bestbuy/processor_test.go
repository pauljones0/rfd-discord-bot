package bestbuy

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type bestBuyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bestBuyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type bestBuyTestStore struct {
	sellers       []Seller
	products      map[string]AnalyzedProduct
	saved         []AnalyzedProduct
	refreshed     []AnalyzedProduct
	subs          []models.Subscription
	soldCompCache map[string]SoldCompSnapshot
}

func (s *bestBuyTestStore) GetActiveBestBuySellers(context.Context) ([]Seller, error) {
	return s.sellers, nil
}

func (s *bestBuyTestStore) SeedBestBuySellers(context.Context) (bool, error) {
	return false, nil
}

func (s *bestBuyTestStore) GetBestBuyProduct(_ context.Context, sku, source string) (AnalyzedProduct, bool, error) {
	product, ok := s.products[sku+"_"+source]
	return product, ok, nil
}

func (s *bestBuyTestStore) SaveBestBuyProduct(_ context.Context, product AnalyzedProduct) error {
	if s.products == nil {
		s.products = make(map[string]AnalyzedProduct)
	}
	s.saved = append(s.saved, product)
	s.products[product.SKU+"_"+product.Source] = product
	return nil
}

func (s *bestBuyTestStore) RefreshBestBuyProduct(_ context.Context, product AnalyzedProduct) error {
	if s.products == nil {
		s.products = make(map[string]AnalyzedProduct)
	}
	s.refreshed = append(s.refreshed, product)
	s.products[product.SKU+"_"+product.Source] = product
	return nil
}

func (s *bestBuyTestStore) PruneBestBuyProducts(context.Context, int, int) error {
	return nil
}

func (s *bestBuyTestStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

func (s *bestBuyTestStore) GetBestBuySoldCompSnapshot(_ context.Context, key string) (SoldCompSnapshot, bool, error) {
	snapshot, ok := s.soldCompCache[key]
	return snapshot, ok, nil
}

func (s *bestBuyTestStore) SaveBestBuySoldCompSnapshot(_ context.Context, key string, snapshot SoldCompSnapshot) error {
	if s.soldCompCache == nil {
		s.soldCompCache = make(map[string]SoldCompSnapshot)
	}
	s.soldCompCache[key] = snapshot
	return nil
}

type bestBuyTestNotifier struct {
	sent []AnalyzedProduct
	subs [][]models.Subscription
}

func (n *bestBuyTestNotifier) SendBestBuyDeal(_ context.Context, product AnalyzedProduct, subs []models.Subscription) error {
	n.sent = append(n.sent, product)
	n.subs = append(n.subs, subs)
	return nil
}

type bestBuyTestAnalyzer struct {
	screenCalls   int
	analyzeCalls  int
	analyzeInputs [][]Product
	topDeals      map[string]bool
	warm          map[string]bool
	hot           map[string]bool
}

func (a *bestBuyTestAnalyzer) ScreenBestBuyBatch(_ context.Context, products []Product) ([]BatchScreenResult, error) {
	a.screenCalls++
	results := make([]BatchScreenResult, 0, len(products))
	for _, product := range products {
		results = append(results, BatchScreenResult{
			SKU:        product.SKU,
			CleanTitle: product.Name,
			IsTopDeal:  a.topDeals == nil || a.topDeals[product.SKU],
		})
	}
	return results, nil
}

func (a *bestBuyTestAnalyzer) AnalyzeBestBuyBatch(_ context.Context, products []Product) ([]BatchAnalyzeResult, error) {
	a.analyzeCalls++
	a.analyzeInputs = append(a.analyzeInputs, append([]Product(nil), products...))
	results := make([]BatchAnalyzeResult, 0, len(products))
	for _, product := range products {
		results = append(results, BatchAnalyzeResult{
			SKU:        product.SKU,
			CleanTitle: product.Name,
			IsWarm:     a.warm[product.SKU],
			IsLavaHot:  a.hot[product.SKU],
			Summary:    "AI summary",
		})
	}
	return results, nil
}

func (a *bestBuyTestAnalyzer) AnalyzeBestBuyProduct(_ context.Context, product Product) (*AnalyzeResult, error) {
	a.analyzeCalls++
	a.analyzeInputs = append(a.analyzeInputs, []Product{product})
	return &AnalyzeResult{
		CleanTitle: product.Name,
		IsWarm:     a.warm[product.SKU],
		IsLavaHot:  a.hot[product.SKU],
		Summary:    "AI summary",
	}, nil
}

func TestProcessBestBuyDeals_PostsEveryNewSellerListingOnce(t *testing.T) {
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: make(map[string]AnalyzedProduct),
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_new", ChannelID: "chan"}},
	}
	notifier := &bestBuyTestNotifier{}
	client := NewClient()
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{
			"currentPage":1,
			"total":2,
			"totalPages":1,
			"pageSize":48,
			"products":[
				{"sku":"111","name":"Refurbished Laptop","productUrl":"/en-ca/product/111","regularPrice":500,"salePrice":350,"seller":"Tech Outlet Center"},
				{"sku":"222","name":"Open Box Monitor","productUrl":"/en-ca/product/222","regularPrice":300,"salePrice":250,"seller":"Tech Outlet Center"}
			]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	processor := NewProcessor(store, client, nil, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent = %d, want 2", len(notifier.sent))
	}
	if notifier.sent[0].Source != "seller:591375" {
		t.Fatalf("source = %q, want seller source", notifier.sent[0].Source)
	}

	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("second ProcessBestBuyDeals() error = %v", err)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent after duplicate run = %d, want still 2", len(notifier.sent))
	}
}

func TestPrimeBaselineSavesInventoryWithoutNotifications(t *testing.T) {
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: make(map[string]AnalyzedProduct),
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_new", ChannelID: "chan"}},
	}
	notifier := &bestBuyTestNotifier{}
	client := NewClient()
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{
			"currentPage":1,
			"total":1,
			"totalPages":1,
			"pageSize":48,
			"products":[
				{"sku":"111","name":"Refurbished Laptop","productUrl":"/en-ca/product/111","regularPrice":500,"salePrice":350,"seller":"Tech Outlet Center"}
			]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	processor := NewProcessor(store, client, nil, notifier, "")
	stats, err := processor.PrimeBaseline(context.Background())
	if err != nil {
		t.Fatalf("PrimeBaseline() error = %v", err)
	}
	if stats.Saved != 1 {
		t.Fatalf("Saved = %d, want 1", stats.Saved)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved records = %d, want 1", len(store.saved))
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0 during baseline", len(notifier.sent))
	}

	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent after baseline duplicate run = %d, want 0", len(notifier.sent))
	}
}

func TestProcessBestBuyDeals_PollsWithoutSubscriptions(t *testing.T) {
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: make(map[string]AnalyzedProduct),
	}
	notifier := &bestBuyTestNotifier{}
	client := NewClient()
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{
			"currentPage":1,
			"total":1,
			"totalPages":1,
			"pageSize":48,
			"products":[
				{"sku":"333","name":"Open Box Desktop","productUrl":"/en-ca/product/333","regularPrice":900,"salePrice":700,"seller":"Tech Outlet Center"}
			]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	processor := NewProcessor(store, client, nil, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved records = %d, want 1", len(store.saved))
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0 without subscriptions", len(notifier.sent))
	}
}

func TestProcessBestBuyDeals_SkipsExpiredSellerOfferBeforeAI(t *testing.T) {
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "1247543", Name: "Parts Search", SearchPath: "sellerName:Parts Search", IsActive: true}},
		products: make(map[string]AnalyzedProduct),
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"17389711": true}, hot: map[string]bool{"17389711": true}}
	notifier := &bestBuyTestNotifier{}
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
				"imageUrl":"https://example.com/cpu.jpg",
				"categoryName":"CPU / Computer Processors",
				"inStock":true,
				"isVisible":true,
				"searchEndDate":253402214400000,
				"seller":{"sellerId":"1247543","sellerName":"Parts Search","marketplace":true},
				"price":{"regularPrice":259.99,"currentPrice":89.99}
			}]
		}`), nil
	})}

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if analyzer.screenCalls != 0 || analyzer.analyzeCalls != 0 {
		t.Fatalf("expired offer should be skipped before AI, screen=%d analyze=%d", analyzer.screenCalls, analyzer.analyzeCalls)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0", len(notifier.sent))
	}
	if len(store.saved) != 0 {
		t.Fatalf("saved = %d, want 0 for stale expired offer", len(store.saved))
	}
}

func TestProcessBestBuyDeals_ExistingNoPriceChangeOnlyRefreshes(t *testing.T) {
	existing := bestBuyFallback(Product{
		SKU:          "111",
		Name:         "Refurbished Laptop",
		RegularPrice: 1000,
		SalePrice:    800,
		Source:       "seller:591375",
		SellerName:   "Tech Outlet Center",
	})
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: map[string]AnalyzedProduct{"111_seller:591375": existing},
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true}}
	notifier := &bestBuyTestNotifier{}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":1,
		"totalPages":1,
		"pageSize":48,
		"products":[{"sku":"111","name":"Refurbished Laptop","productUrl":"/en-ca/product/111","regularPrice":1000,"salePrice":800,"seller":"Tech Outlet Center"}]
	}`)

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if analyzer.screenCalls != 0 || analyzer.analyzeCalls != 0 {
		t.Fatalf("AI was called for unchanged existing product: screen=%d analyze=%d", analyzer.screenCalls, analyzer.analyzeCalls)
	}
	if len(store.refreshed) != 1 {
		t.Fatalf("refreshed = %d, want 1", len(store.refreshed))
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0", len(notifier.sent))
	}
}

func TestProcessBestBuyDeals_SmallPriceDropsDoNotRunAI(t *testing.T) {
	first := bestBuyFallback(Product{SKU: "111", Name: "Small Dollar Drop", RegularPrice: 200, SalePrice: 200, Source: "seller:591375"})
	second := bestBuyFallback(Product{SKU: "222", Name: "Small Percent Drop", RegularPrice: 1000, SalePrice: 1000, Source: "seller:591375"})
	store := &bestBuyTestStore{
		sellers: []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: map[string]AnalyzedProduct{
			"111_seller:591375": first,
			"222_seller:591375": second,
		},
		subs: []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true, "222": true}}
	notifier := &bestBuyTestNotifier{}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":2,
		"totalPages":1,
		"pageSize":48,
		"products":[
			{"sku":"111","name":"Small Dollar Drop","productUrl":"/en-ca/product/111","regularPrice":200,"salePrice":160,"seller":"Tech Outlet Center"},
			{"sku":"222","name":"Small Percent Drop","productUrl":"/en-ca/product/222","regularPrice":1000,"salePrice":850,"seller":"Tech Outlet Center"}
		]
	}`)

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if analyzer.screenCalls != 0 || analyzer.analyzeCalls != 0 {
		t.Fatalf("AI was called for non-meaningful drops: screen=%d analyze=%d", analyzer.screenCalls, analyzer.analyzeCalls)
	}
	if len(store.refreshed) != 2 {
		t.Fatalf("refreshed = %d, want 2", len(store.refreshed))
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0", len(notifier.sent))
	}
}

func TestProcessBestBuyDeals_WarmPriceDropExcludesBBNewAndHot(t *testing.T) {
	existing := bestBuyFallback(Product{SKU: "111", Name: "Gaming Monitor", RegularPrice: 1000, SalePrice: 1000, Source: "seller:591375"})
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: map[string]AnalyzedProduct{"111_seller:591375": existing},
		subs: []models.Subscription{
			{SubscriptionType: "bestbuy", DealType: "bb_new", ChannelID: "new"},
			{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"},
			{SubscriptionType: "bestbuy", DealType: "bb_hot", ChannelID: "hot"},
		},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true}, hot: map[string]bool{"111": false}}
	notifier := &bestBuyTestNotifier{}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":1,
		"totalPages":1,
		"pageSize":48,
		"products":[{"sku":"111","name":"Gaming Monitor","productUrl":"/en-ca/product/111","regularPrice":1000,"salePrice":750,"seller":"Tech Outlet Center"}]
	}`)

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(notifier.sent))
	}
	if notifier.sent[0].AlertKind != AlertKindPriceDrop {
		t.Fatalf("AlertKind = %q, want price drop", notifier.sent[0].AlertKind)
	}
	if len(notifier.subs[0]) != 1 || notifier.subs[0][0].DealType != "bb_warm_hot" {
		t.Fatalf("price drop sent to wrong subscriptions: %#v", notifier.subs[0])
	}
	persisted := store.products["111_seller:591375"]
	if persisted.LastPriceDropAlertPrice != 750 || persisted.LastPriceDropAlertKey == "" {
		t.Fatalf("alert state not persisted: %#v", persisted)
	}
}

func TestProcessBestBuyDeals_LavaHotPriceDropNotifiesWarmHotAndHot(t *testing.T) {
	existing := bestBuyFallback(Product{SKU: "111", Name: "OLED TV", RegularPrice: 1000, SalePrice: 1000, Source: "seller:591375"})
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: map[string]AnalyzedProduct{"111_seller:591375": existing},
		subs: []models.Subscription{
			{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"},
			{SubscriptionType: "bestbuy", DealType: "bb_hot", ChannelID: "hot"},
		},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true}, hot: map[string]bool{"111": true}}
	notifier := &bestBuyTestNotifier{}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":1,
		"totalPages":1,
		"pageSize":48,
		"products":[{"sku":"111","name":"OLED TV","productUrl":"/en-ca/product/111","regularPrice":1000,"salePrice":700,"seller":"Tech Outlet Center"}]
	}`)

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(notifier.sent))
	}
	if len(notifier.subs[0]) != 2 {
		t.Fatalf("eligible subs = %d, want 2: %#v", len(notifier.subs[0]), notifier.subs[0])
	}
}

func TestProcessBestBuyDeals_DuplicatePriceDropAlertSuppressed(t *testing.T) {
	existing := bestBuyFallback(Product{SKU: "111", Name: "Gaming Laptop", RegularPrice: 1000, SalePrice: 800, Source: "seller:591375"})
	existing.LastPriceDropAlertPrice = 750
	existing.LastPriceDropAlertKey = priceDropAlertKey("111", "seller:591375", 750)
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: map[string]AnalyzedProduct{"111_seller:591375": existing},
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}},
	}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true}}
	notifier := &bestBuyTestNotifier{}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":1,
		"totalPages":1,
		"pageSize":48,
		"products":[{"sku":"111","name":"Gaming Laptop","productUrl":"/en-ca/product/111","regularPrice":1000,"salePrice":750,"seller":"Tech Outlet Center"}]
	}`)

	processor := NewProcessor(store, client, analyzer, notifier, "")
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if analyzer.screenCalls != 0 || len(notifier.sent) != 0 {
		t.Fatalf("duplicate drop should be suppressed; screen=%d sent=%d", analyzer.screenCalls, len(notifier.sent))
	}
}

type bestBuyTestSoldCompEnricher struct {
	beginRuns int
	calls     int
	inputs    [][]Product
	mutate    func([]Product) []Product
}

func (e *bestBuyTestSoldCompEnricher) BeginRun() {
	e.beginRuns++
}

func (e *bestBuyTestSoldCompEnricher) EnrichProducts(_ context.Context, products []Product, _ time.Time, _ *slog.Logger) ([]Product, error) {
	e.calls++
	e.inputs = append(e.inputs, append([]Product(nil), products...))
	if e.mutate != nil {
		return e.mutate(products), nil
	}
	return products, nil
}

func TestProcessBestBuyDeals_SoldCompEnricherRunsAfterTier1Only(t *testing.T) {
	store := &bestBuyTestStore{
		sellers:  []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		products: make(map[string]AnalyzedProduct),
		subs:     []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}},
	}
	analyzer := &bestBuyTestAnalyzer{topDeals: map[string]bool{"111": true, "222": false}, warm: map[string]bool{"111": true}}
	enricher := &bestBuyTestSoldCompEnricher{mutate: func(products []Product) []Product {
		out := append([]Product(nil), products...)
		for i := range out {
			out[i].SoldCompSummary = "eBay sold comps: useful evidence"
			out[i].SoldCompCount = 2
			out[i].SoldCompMedianPrice = 500
		}
		return out
	}}
	client := bestBuyTestClient(`{
		"currentPage":1,
		"total":2,
		"totalPages":1,
		"pageSize":48,
		"products":[
			{"sku":"111","name":"Sony Headphones","productUrl":"/en-ca/product/111","regularPrice":500,"salePrice":300,"seller":"Tech Outlet Center"},
			{"sku":"222","name":"USB Cable","productUrl":"/en-ca/product/222","regularPrice":200,"salePrice":150,"seller":"Tech Outlet Center"}
		]
	}`)
	processor := NewProcessor(store, client, analyzer, &bestBuyTestNotifier{}, "")
	processor.SetSoldCompEnricher(enricher)

	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if enricher.beginRuns != 1 || enricher.calls != 1 {
		t.Fatalf("enricher begin/calls = %d/%d, want 1/1", enricher.beginRuns, enricher.calls)
	}
	if len(enricher.inputs) != 1 || len(enricher.inputs[0]) != 1 || enricher.inputs[0][0].SKU != "111" {
		t.Fatalf("enricher inputs = %#v, want only tier-1 product 111", enricher.inputs)
	}
	if len(analyzer.analyzeInputs) == 0 || analyzer.analyzeInputs[0][0].SoldCompSummary == "" {
		t.Fatalf("tier-2 analyze inputs missing sold comps: %#v", analyzer.analyzeInputs)
	}
	if saved := store.products["111_seller:591375"]; saved.SoldCompSummary == "" || saved.SoldCompMedianPrice != 500 {
		t.Fatalf("saved product missing sold comps: %#v", saved.Product)
	}
}

func TestProcessBestBuyDeals_SoldCompEnricherSkippedWithoutSubscriptions(t *testing.T) {
	store := &bestBuyTestStore{sellers: []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}}, products: make(map[string]AnalyzedProduct)}
	analyzer := &bestBuyTestAnalyzer{warm: map[string]bool{"111": true}}
	enricher := &bestBuyTestSoldCompEnricher{}
	processor := NewProcessor(store, bestBuyTestClient(`{"currentPage":1,"total":1,"totalPages":1,"pageSize":48,"products":[{"sku":"111","name":"Sony Headphones","productUrl":"/en-ca/product/111","regularPrice":500,"salePrice":300,"seller":"Tech Outlet Center"}]}`), analyzer, &bestBuyTestNotifier{}, "")
	processor.SetSoldCompEnricher(enricher)
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if enricher.calls != 0 {
		t.Fatalf("enricher calls = %d, want 0 without subscriptions", enricher.calls)
	}
}

func TestProcessBestBuyDeals_SoldCompEnricherSkippedForTier1Rejected(t *testing.T) {
	store := &bestBuyTestStore{sellers: []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}}, products: make(map[string]AnalyzedProduct), subs: []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_warm_hot", ChannelID: "warm"}}}
	analyzer := &bestBuyTestAnalyzer{topDeals: map[string]bool{"111": false}, warm: map[string]bool{"111": true}}
	enricher := &bestBuyTestSoldCompEnricher{}
	processor := NewProcessor(store, bestBuyTestClient(`{"currentPage":1,"total":1,"totalPages":1,"pageSize":48,"products":[{"sku":"111","name":"Sony Headphones","productUrl":"/en-ca/product/111","regularPrice":500,"salePrice":300,"seller":"Tech Outlet Center"}]}`), analyzer, &bestBuyTestNotifier{}, "")
	processor.SetSoldCompEnricher(enricher)
	if err := processor.ProcessBestBuyDeals(context.Background()); err != nil {
		t.Fatalf("ProcessBestBuyDeals() error = %v", err)
	}
	if enricher.calls != 0 {
		t.Fatalf("enricher calls = %d, want 0 for tier-1 rejected product", enricher.calls)
	}
}

func TestRefreshExistingBestBuyProduct_BaselinesLegacyRecordFromStoredPrice(t *testing.T) {
	existing := AnalyzedProduct{
		Product: Product{
			SKU:          "111",
			Source:       "seller:591375",
			RegularPrice: 1000,
			SalePrice:    900,
		},
	}
	refreshed, eval := refreshExistingBestBuyProduct(existing, Product{
		SKU:          "111",
		Source:       "seller:591375",
		RegularPrice: 1000,
		SalePrice:    700,
	}, existing.ProcessedAt)
	if !eval.Candidate {
		t.Fatal("expected legacy record to use stored price as baseline and detect meaningful drop")
	}
	if refreshed.InitialEffectivePrice != 900 {
		t.Fatalf("InitialEffectivePrice = %.2f, want 900", refreshed.InitialEffectivePrice)
	}
	if refreshed.PriceDropAmount != 200 {
		t.Fatalf("PriceDropAmount = %.2f, want 200", refreshed.PriceDropAmount)
	}
}

func TestBestBuyComparableGuardDowngradesOptimisticWarm(t *testing.T) {
	product := Product{
		SKU:                   "111",
		Name:                  "Refurbished Laptop",
		SalePrice:             900,
		ComparableCount:       3,
		ComparableMedianPrice: 1000,
		ComparableDiscountPct: 10,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:  true,
		Summary: "AI liked the seller discount",
	})

	if analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("optimistic comp-backed label was not downgraded: %#v", analyzed)
	}
	if !strings.Contains(analyzed.Summary, "Not enough below comps") {
		t.Fatalf("Summary = %q, want comp downgrade note", analyzed.Summary)
	}
}

func TestBestBuyComparableGuardDowngradesSmallSetWhenNotBelowLowestComp(t *testing.T) {
	product := Product{
		SKU:                   "16554105",
		Name:                  "Refurbished Apple Watch Series 8 GPS 41mm",
		SalePrice:             229.99,
		ComparableCount:       2,
		ComparableMedianPrice: 1111.495,
		ComparableP25Price:    667.74125,
		ComparableLowestPrice: 223.99,
		ComparableDiscountPct: 79.3,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsLavaHot: true,
		Summary:   "AI trusted the median comp gap",
	})

	if analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("small-set comp floor did not downgrade label: %#v", analyzed)
	}
	if !strings.Contains(analyzed.Summary, "Not enough below comps") {
		t.Fatalf("Summary = %q, want comp downgrade note", analyzed.Summary)
	}
}

func TestBestBuyComparableGuardKeepsWarmWhenEnoughBelowComps(t *testing.T) {
	product := Product{
		SKU:                   "111",
		Name:                  "Refurbished Laptop",
		SalePrice:             700,
		ComparableCount:       3,
		ComparableMedianPrice: 1000,
		ComparableDiscountPct: 30,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:  true,
		Summary: "Well below comps",
	})

	if !analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("warm comp-backed label changed unexpectedly: %#v", analyzed)
	}
}

func TestBestBuyComparableGuardDowngradesHotToWarm(t *testing.T) {
	product := Product{
		SKU:                   "111",
		Name:                  "Refurbished Laptop",
		SalePrice:             760,
		ComparableCount:       3,
		ComparableMedianPrice: 1000,
		ComparableDiscountPct: 24,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:    true,
		IsLavaHot: true,
		Summary:   "AI called it hot",
	})

	if !analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("hot label was not downgraded to warm: %#v", analyzed)
	}
	if !strings.Contains(analyzed.Summary, "Hot label downgraded by comps") {
		t.Fatalf("Summary = %q, want hot downgrade note", analyzed.Summary)
	}
}

func TestBestBuyComparableGuardKeepsLavaHotWhenDeepBelowComps(t *testing.T) {
	product := Product{
		SKU:                   "111",
		Name:                  "Refurbished Laptop",
		SalePrice:             500,
		ComparableCount:       3,
		ComparableMedianPrice: 1000,
		ComparableDiscountPct: 50,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:    true,
		IsLavaHot: true,
		Summary:   "Deeply below comps",
	})

	if !analyzed.IsWarm || !analyzed.IsLavaHot {
		t.Fatalf("lava-hot comp-backed label changed unexpectedly: %#v", analyzed)
	}
}

func TestSoldCompMarketGuardDowngradesWeakEbayEvidence(t *testing.T) {
	product := Product{
		SKU:                 "111",
		Name:                "Refurbished Laptop",
		SalePrice:           500,
		SoldCompCount:       3,
		SoldCompMedianPrice: 540,
		SoldCompP25Price:    520,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:    true,
		IsLavaHot: true,
		Summary:   "AI liked the seller discount",
	})

	if analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("weak eBay evidence did not downgrade: %#v", analyzed)
	}
	if !strings.Contains(analyzed.Summary, "Not enough below eBay sold comps") {
		t.Fatalf("Summary = %q, want eBay downgrade note", analyzed.Summary)
	}
}

func TestSoldCompMarketGuardFailsOpenWithoutEbayEvidence(t *testing.T) {
	product := Product{SKU: "111", Name: "Refurbished Laptop", SalePrice: 500}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		IsWarm:  true,
		Summary: "AI liked the seller discount",
	})

	if !analyzed.IsWarm || analyzed.IsLavaHot {
		t.Fatalf("missing eBay evidence should fail open to AI label: %#v", analyzed)
	}
}

func TestSoldCompMarketGuardCanUpgradeStrongEbayEvidence(t *testing.T) {
	product := Product{
		SKU:                 "111",
		Name:                "Refurbished Laptop",
		SalePrice:           500,
		SoldCompCount:       3,
		SoldCompMedianPrice: 1000,
		SoldCompP25Price:    900,
	}

	analyzed := bestBuyFromAnalysis(product, BatchScreenResult{}, BatchAnalyzeResult{
		Summary: "AI was unsure",
	})

	if !analyzed.IsWarm || !analyzed.IsLavaHot {
		t.Fatalf("strong eBay evidence should upgrade to hot: %#v", analyzed)
	}
	if !strings.Contains(analyzed.Summary, "Hot verified by eBay sold comps") {
		t.Fatalf("Summary = %q, want eBay verification note", analyzed.Summary)
	}
}

func bestBuyTestClient(body string) *Client {
	client := NewClient()
	client.httpClient = &http.Client{Transport: bestBuyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}
	return client
}
