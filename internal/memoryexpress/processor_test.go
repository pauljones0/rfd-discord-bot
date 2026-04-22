package memoryexpress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type testStore struct {
	subs             []models.Subscription
	existing         map[string]struct{}
	existenceQueries [][]Product
	saved            []AnalyzedProduct
}

func (s *testStore) GetExistingMemExpressProductIDs(_ context.Context, products []Product) (map[string]struct{}, error) {
	cloned := append([]Product(nil), products...)
	s.existenceQueries = append(s.existenceQueries, cloned)
	return s.existing, nil
}

func (s *testStore) SaveMemExpressProduct(_ context.Context, product AnalyzedProduct) error {
	s.saved = append(s.saved, product)
	return nil
}

func (s *testStore) PruneMemExpressProducts(context.Context, int, int) error {
	return nil
}

func (s *testStore) GetMemExpressSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

type testAnalyzer struct {
	screenResults []BatchScreenResult
	batchResults  []BatchAnalyzeResult
	batchErr      error
	singleResults map[string]*AnalyzeResult
	singleErrs    map[string]error
	batchInputs   [][]Product
	singleCalls   []string
}

func (a *testAnalyzer) ScreenMemExpressBatch(context.Context, []Product) ([]BatchScreenResult, error) {
	return a.screenResults, nil
}

func (a *testAnalyzer) AnalyzeMemExpressBatch(_ context.Context, products []Product) ([]BatchAnalyzeResult, error) {
	cloned := append([]Product(nil), products...)
	a.batchInputs = append(a.batchInputs, cloned)
	return a.batchResults, a.batchErr
}

func (a *testAnalyzer) AnalyzeMemExpressProduct(_ context.Context, product Product) (*AnalyzeResult, error) {
	a.singleCalls = append(a.singleCalls, product.SKU)
	if err := a.singleErrs[product.SKU]; err != nil {
		return nil, err
	}
	return a.singleResults[product.SKU], nil
}

func TestProcessMemExpressDeals_UsesBatchExistenceCheck(t *testing.T) {
	store := &testStore{
		subs: []models.Subscription{
			{SubscriptionType: "memoryexpress", StoreCode: "SKST", DealType: "me_warm_hot"},
		},
		existing: map[string]struct{}{
			DocID("SKU-2", "SKST"): {},
		},
	}

	p := NewProcessor(store, nil, nil)
	p.scrape = func(context.Context, string) ([]Product, error) {
		return []Product{
			{SKU: "SKU-1", Title: "First", StoreCode: "SKST", StoreName: "Store"},
			{SKU: "SKU-2", Title: "Second", StoreCode: "SKST", StoreName: "Store"},
			{SKU: "SKU-3", Title: "Third", StoreCode: "SKST", StoreName: "Store"},
		}, nil
	}

	if err := p.ProcessMemExpressDeals(context.Background()); err != nil {
		t.Fatalf("ProcessMemExpressDeals returned error: %v", err)
	}

	if len(store.existenceQueries) != 1 {
		t.Fatalf("existenceQueries = %d, want 1", len(store.existenceQueries))
	}
	if got := len(store.existenceQueries[0]); got != 3 {
		t.Fatalf("batch existence query size = %d, want 3", got)
	}
	if got := len(store.saved); got != 2 {
		t.Fatalf("saved products = %d, want 2", got)
	}
	for _, product := range store.saved {
		if product.SKU == "SKU-2" {
			t.Fatal("existing product should not be saved again")
		}
	}
}

func TestProcessMemExpressDeals_SkipsWithoutSubscriptions(t *testing.T) {
	store := &testStore{}

	p := NewProcessor(store, nil, nil)
	scrapeCalled := false
	p.scrape = func(context.Context, string) ([]Product, error) {
		scrapeCalled = true
		return nil, nil
	}

	if err := p.ProcessMemExpressDeals(context.Background()); err != nil {
		t.Fatalf("ProcessMemExpressDeals returned error: %v", err)
	}

	if scrapeCalled {
		t.Fatal("scrape should not run when there are no Memory Express subscriptions")
	}
	if len(store.existenceQueries) != 0 {
		t.Fatalf("existenceQueries = %d, want 0", len(store.existenceQueries))
	}
}

func TestProcessMemExpressDeals_ReturnsErrorWhenAllStoreScrapesFail(t *testing.T) {
	store := &testStore{
		subs: []models.Subscription{
			{SubscriptionType: "memoryexpress", StoreCode: "SKST", DealType: "me_warm_hot"},
		},
	}

	p := NewProcessor(store, nil, nil)
	p.scrape = func(context.Context, string) ([]Product, error) {
		return nil, errors.New("blocked by cloudflare")
	}

	err := p.ProcessMemExpressDeals(context.Background())
	if err == nil {
		t.Fatal("ProcessMemExpressDeals() error = nil, want scrape failure")
	}
	if got, want := err.Error(), "failed to scrape all subscribed Memory Express stores"; got != want {
		t.Fatalf("ProcessMemExpressDeals() error = %q, want %q", got, want)
	}
}

func TestProcessBatch_CapsTier1CandidatesAndBatchesTier2(t *testing.T) {
	store := &testStore{}
	analyzer := &testAnalyzer{
		screenResults: []BatchScreenResult{
			{SKU: "SKU-1", CleanTitle: "Deal 1", IsTopDeal: true},
			{SKU: "SKU-2", CleanTitle: "Deal 2", IsTopDeal: true},
			{SKU: "SKU-3", CleanTitle: "Deal 3", IsTopDeal: true},
			{SKU: "SKU-4", CleanTitle: "Deal 4", IsTopDeal: true},
			{SKU: "SKU-5", CleanTitle: "Deal 5", IsTopDeal: true},
		},
		batchResults: []BatchAnalyzeResult{
			{SKU: "SKU-1", CleanTitle: "Deal 1", IsWarm: true, Summary: "warm"},
			{SKU: "SKU-2", CleanTitle: "Deal 2", IsWarm: true, Summary: "warm"},
			{SKU: "SKU-3", CleanTitle: "Deal 3", IsLavaHot: true, Summary: "hot"},
		},
	}
	p := NewProcessor(store, analyzer, nil)

	var batch []Product
	for i := 1; i <= 10; i++ {
		batch = append(batch, Product{
			SKU:       fmt.Sprintf("SKU-%d", i),
			Title:     "Product",
			StoreCode: "SKST",
			StoreName: "Store",
		})
	}

	results, t1Count, err := p.processBatch(context.Background(), batch, slog.Default())
	if err != nil {
		t.Fatalf("processBatch returned error: %v", err)
	}

	if t1Count != 3 {
		t.Fatalf("t1Count = %d, want 3", t1Count)
	}
	if len(analyzer.batchInputs) != 1 {
		t.Fatalf("batch tier-2 calls = %d, want 1", len(analyzer.batchInputs))
	}
	if got := len(analyzer.batchInputs[0]); got != 3 {
		t.Fatalf("batch tier-2 input size = %d, want 3", got)
	}
	if len(analyzer.singleCalls) != 0 {
		t.Fatalf("single tier-2 calls = %d, want 0", len(analyzer.singleCalls))
	}
	if len(results) != 3 {
		t.Fatalf("results = %d, want 3", len(results))
	}
	if len(store.saved) != 10 {
		t.Fatalf("saved products = %d, want 10", len(store.saved))
	}
}

func TestProcessBatch_FallsBackToIndividualTier2WhenBatchFails(t *testing.T) {
	store := &testStore{}
	analyzer := &testAnalyzer{
		screenResults: []BatchScreenResult{
			{SKU: "SKU-1", CleanTitle: "Deal 1", IsTopDeal: true},
			{SKU: "SKU-2", CleanTitle: "Deal 2", IsTopDeal: true},
		},
		batchErr: errors.New("temporary batch failure"),
		singleResults: map[string]*AnalyzeResult{
			"SKU-1": {CleanTitle: "Deal 1", IsWarm: true, Summary: "warm"},
			"SKU-2": {CleanTitle: "Deal 2", IsLavaHot: true, Summary: "hot"},
		},
	}
	p := NewProcessor(store, analyzer, nil)

	batch := []Product{
		{SKU: "SKU-1", Title: "Product 1", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-2", Title: "Product 2", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-3", Title: "Product 3", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-4", Title: "Product 4", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-5", Title: "Product 5", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-6", Title: "Product 6", StoreCode: "SKST", StoreName: "Store"},
		{SKU: "SKU-7", Title: "Product 7", StoreCode: "SKST", StoreName: "Store"},
	}

	results, t1Count, err := p.processBatch(context.Background(), batch, slog.Default())
	if err != nil {
		t.Fatalf("processBatch returned error: %v", err)
	}

	if t1Count != 2 {
		t.Fatalf("t1Count = %d, want 2", t1Count)
	}
	if len(analyzer.batchInputs) != 1 {
		t.Fatalf("batch tier-2 calls = %d, want 1", len(analyzer.batchInputs))
	}
	if len(analyzer.singleCalls) != 2 {
		t.Fatalf("single tier-2 calls = %d, want 2", len(analyzer.singleCalls))
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
}
