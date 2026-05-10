package bestbuy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type bestBuyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f bestBuyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type bestBuyTestStore struct {
	sellers   []Seller
	products  map[string]AnalyzedProduct
	saved     []AnalyzedProduct
	refreshed []AnalyzedProduct
	subs      []models.Subscription
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
	screenCalls  int
	analyzeCalls int
	topDeals     map[string]bool
	warm         map[string]bool
	hot          map[string]bool
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
