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
	sellers []Seller
	exists  map[string]bool
	saved   []AnalyzedProduct
	subs    []models.Subscription
}

func (s *bestBuyTestStore) GetActiveBestBuySellers(context.Context) ([]Seller, error) {
	return s.sellers, nil
}

func (s *bestBuyTestStore) SeedBestBuySellers(context.Context) (bool, error) {
	return false, nil
}

func (s *bestBuyTestStore) BestBuyProductExists(_ context.Context, sku, source string) (bool, error) {
	return s.exists[sku+"_"+source], nil
}

func (s *bestBuyTestStore) SaveBestBuyProduct(_ context.Context, product AnalyzedProduct) error {
	s.saved = append(s.saved, product)
	s.exists[product.SKU+"_"+product.Source] = true
	return nil
}

func (s *bestBuyTestStore) RefreshBestBuyProductLastSeen(_ context.Context, product Product) error {
	s.exists[product.SKU+"_"+product.Source] = true
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
}

func (n *bestBuyTestNotifier) SendBestBuyDeal(_ context.Context, product AnalyzedProduct, _ []models.Subscription) error {
	n.sent = append(n.sent, product)
	return nil
}

func TestProcessBestBuyDeals_PostsEveryNewSellerListingOnce(t *testing.T) {
	store := &bestBuyTestStore{
		sellers: []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		exists:  make(map[string]bool),
		subs:    []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_new", ChannelID: "chan"}},
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
		sellers: []Seller{{ID: "591375", Name: "Tech Outlet Center", SearchPath: "sellerName:Tech Outlet Center", IsActive: true}},
		exists:  make(map[string]bool),
		subs:    []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_new", ChannelID: "chan"}},
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
