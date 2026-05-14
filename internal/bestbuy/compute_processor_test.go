package bestbuy

import (
	"context"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type computeTestStore struct {
	observations map[string]ComputeObservation
	subs         []models.Subscription
	saved        []ComputeObservation
}

func (s *computeTestStore) SaveBestBuyComputeObservation(_ context.Context, observation ComputeObservation) error {
	if s.observations == nil {
		s.observations = make(map[string]ComputeObservation)
	}
	s.saved = append(s.saved, observation)
	s.observations[computeObservationKey(observation.Product)] = observation
	return nil
}

func (s *computeTestStore) ListBestBuyComputeObservations(context.Context) ([]ComputeObservation, error) {
	out := make([]ComputeObservation, 0, len(s.observations))
	for _, observation := range s.observations {
		out = append(out, observation)
	}
	return out, nil
}

func (s *computeTestStore) PruneBestBuyComputeObservations(context.Context, int, int) error {
	return nil
}

func (s *computeTestStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

type computeTestClient struct {
	products []Product
}

func (c computeTestClient) FetchComputeProducts(context.Context) ([]Product, error) {
	return c.products, nil
}

func (c computeTestClient) ValidateSellerOffer(_ context.Context, product Product, now time.Time) (OfferValidation, error) {
	product.AvailabilityCheckedAt = now
	return OfferValidation{Product: product, Valid: true}, nil
}

type computeTestNotifier struct {
	sent []AnalyzedProduct
}

func (n *computeTestNotifier) SendBestBuyDeal(_ context.Context, product AnalyzedProduct, _ []models.Subscription) error {
	n.sent = append(n.sent, product)
	return nil
}

func TestComputeProcessorBaselinesFirstSeenWithoutAlert(t *testing.T) {
	products := append([]Product{computeCandidate("candidate", "seller-a", 650)}, computeCompsForProcessor()...)
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
		subs:         []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}},
	}
	notifier := &computeTestNotifier{}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want first run baseline without alerts", len(notifier.sent))
	}
	if len(store.observations) != len(products) {
		t.Fatalf("observations = %d, want %d", len(store.observations), len(products))
	}
}

func TestComputeProcessorAlertsExistingOutlierOnce(t *testing.T) {
	products := append([]Product{computeCandidate("candidate", "seller-a", 650)}, computeCompsForProcessor()...)
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
		subs:         []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}},
	}
	for _, product := range products {
		store.observations[computeObservationKey(product)] = ComputeObservation{
			Product:   product,
			Spec:      ParseComputeSpec(product),
			FirstSeen: time.Now().Add(-time.Hour),
			LastSeen:  time.Now().Add(-time.Hour),
		}
	}
	notifier := &computeTestNotifier{}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d, want candidate alert once", len(notifier.sent))
	}
	if notifier.sent[0].AlertKind != AlertKindComputeOutlier {
		t.Fatalf("AlertKind = %q, want compute outlier", notifier.sent[0].AlertKind)
	}
	if !notifier.sent[0].IsWarm {
		t.Fatalf("sent product is not warm: %#v", notifier.sent[0])
	}

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("second ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent after duplicate run = %d, want still 1", len(notifier.sent))
	}
}

func computeCandidate(sku, sellerID string, price float64) Product {
	return Product{
		SKU:        sku,
		Name:       "Dell Precision 5820 Xeon W-2133 32GB RAM 512GB NVMe Quadro P4000",
		SalePrice:  price,
		SellerID:   sellerID,
		SellerName: sellerID,
		Source:     "seller:" + sellerID,
		URL:        "https://www.bestbuy.ca/en-ca/product/" + sku,
	}
}

func computeCompsForProcessor() []Product {
	return []Product{
		computeCandidate("c1", "seller-b", 1100),
		computeCandidate("c2", "seller-c", 1200),
		computeCandidate("c3", "seller-d", 1250),
		computeCandidate("c4", "seller-e", 1300),
	}
}
