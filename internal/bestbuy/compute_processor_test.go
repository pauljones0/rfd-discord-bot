package bestbuy

import (
	"context"
	"log/slog"
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
	sent   []AnalyzedProduct
	issues []ComputeIssue
}

func (n *computeTestNotifier) SendBestBuyDeal(_ context.Context, product AnalyzedProduct, _ []models.Subscription) error {
	n.sent = append(n.sent, product)
	return nil
}

func (n *computeTestNotifier) SendBestBuyComputeIssue(_ context.Context, issue ComputeIssue, _ []models.Subscription) error {
	n.issues = append(n.issues, issue)
	return nil
}

type computeTestSoldVerifier struct {
	verification EbaySoldVerification
	beginRuns    int
	calls        []ComputeObservation
}

func (v *computeTestSoldVerifier) BeginRun() {
	v.beginRuns++
}

func (v *computeTestSoldVerifier) Verify(_ context.Context, observation ComputeObservation, _ ComputeObservation, now time.Time, _ *slog.Logger) EbaySoldVerification {
	v.calls = append(v.calls, observation)
	verification := v.verification
	if verification.AlertKey == "" {
		verification.AlertKey = computeAlertKey(observation)
	}
	if verification.CheckedAt.IsZero() {
		verification.CheckedAt = now
	}
	if verification.Verdict == "" {
		if verification.Pass {
			verification.Verdict = ebaySoldVerdictPass
		} else {
			verification.Verdict = ebaySoldVerdictFail
		}
	}
	return verification
}

func TestComputeProcessorBaselinesFirstSeenWithoutAlert(t *testing.T) {
	products := append([]Product{computeCandidate("candidate", "seller-a", 650)}, computeCompsForProcessor()...)
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
		subs:         []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}},
	}
	notifier := &computeTestNotifier{}
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:            true,
		Verdict:         ebaySoldVerdictPass,
		Query:           "Dell Precision 5820",
		ComparableCount: 5,
		MedianPrice:     2500,
		GapPct:          74,
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want first run baseline without alerts", len(notifier.sent))
	}
	if len(store.observations) != len(products) {
		t.Fatalf("observations = %d, want %d", len(store.observations), len(products))
	}

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("second ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent after baseline repeat = %d, want no delayed first-seen alert", len(notifier.sent))
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
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:            true,
		Verdict:         ebaySoldVerdictPass,
		Query:           "Dell Precision 5820",
		ComparableCount: 5,
		MedianPrice:     2500,
		GapPct:          74,
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)
	processor.SetSoldVerifier(verifier)

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

func TestComputeProcessorMissingSoldVerifierSendsIssueInsteadOfDeal(t *testing.T) {
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
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want no deal without eBay verifier", len(notifier.sent))
	}
	if len(notifier.issues) != 1 {
		t.Fatalf("issues = %d, want 1 verifier issue", len(notifier.issues))
	}
	if notifier.issues[0].Verification.Verdict != "disabled" {
		t.Fatalf("issue verdict = %q, want disabled", notifier.issues[0].Verification.Verdict)
	}
	saved := store.observations[computeObservationKey(products[0])]
	if saved.LastAlertKey != "" {
		t.Fatalf("LastAlertKey = %q, want empty so deal can retry when eBay is configured", saved.LastAlertKey)
	}
	if saved.LastIssueAlertKey == "" {
		t.Fatalf("LastIssueAlertKey is empty after issue alert")
	}
}

func TestComputeProcessorSoldVerifierRejectsNotification(t *testing.T) {
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
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:            false,
		Verdict:         ebaySoldVerdictFail,
		Query:           "Dell Precision 5820",
		ComparableCount: 2,
		Error:           "not enough matching sold comps",
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want 0 after sold verifier rejection", len(notifier.sent))
	}
	if verifier.beginRuns != 1 || len(verifier.calls) != 1 {
		t.Fatalf("verifier begin/calls = %d/%d, want 1/1", verifier.beginRuns, len(verifier.calls))
	}
	saved := store.observations[computeObservationKey(products[0])]
	if saved.EbaySoldVerdict != ebaySoldVerdictFail {
		t.Fatalf("EbaySoldVerdict = %q, want %q", saved.EbaySoldVerdict, ebaySoldVerdictFail)
	}
	if saved.LastAlertKey != "" {
		t.Fatalf("LastAlertKey = %q, want empty so rejected item can be retried after cache TTL", saved.LastAlertKey)
	}
}

func TestComputeProcessorSoldVerifierAllowsNotification(t *testing.T) {
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
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:            true,
		Verdict:         ebaySoldVerdictPass,
		Query:           "Dell Precision 5820",
		ComparableCount: 4,
		MedianPrice:     2500,
		GapPct:          74,
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d, want 1 after sold verifier pass", len(notifier.sent))
	}
	saved := store.observations[computeObservationKey(products[0])]
	if saved.EbaySoldVerdict != ebaySoldVerdictPass {
		t.Fatalf("EbaySoldVerdict = %q, want %q", saved.EbaySoldVerdict, ebaySoldVerdictPass)
	}
	if saved.LastAlertKey == "" {
		t.Fatalf("LastAlertKey is empty after successful send")
	}
}

func TestComputeProcessorSoldVerifierFetchErrorSendsIssueInsteadOfDeal(t *testing.T) {
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
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:    true,
		Verdict: ebaySoldVerdictFetchError,
		Query:   "Dell Precision 5820",
		Error:   "all backends blocked",
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: products}, notifier, "", false, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want no deal after fetch error", len(notifier.sent))
	}
	if len(notifier.issues) != 1 {
		t.Fatalf("issues = %d, want 1 fetch-error issue", len(notifier.issues))
	}
	if notifier.issues[0].Verification.Verdict != ebaySoldVerdictFetchError {
		t.Fatalf("issue verdict = %q, want fetch_error", notifier.issues[0].Verification.Verdict)
	}
	saved := store.observations[computeObservationKey(products[0])]
	if saved.EbaySoldVerdict != ebaySoldVerdictFetchError {
		t.Fatalf("EbaySoldVerdict = %q, want fetch_error", saved.EbaySoldVerdict)
	}
	if saved.LastAlertKey != "" {
		t.Fatalf("LastAlertKey = %q, want empty so deal can retry after eBay recovers", saved.LastAlertKey)
	}
	if saved.LastIssueAlertKey == "" {
		t.Fatalf("LastIssueAlertKey is empty after issue alert")
	}
}

func TestComputeProcessorExtremeSpecCanUseEbaySoldFallback(t *testing.T) {
	product := Product{
		SKU:       "extreme",
		Name:      "Dell PowerEdge R740 768GB RAM 24 Core Xeon Server",
		SalePrice: 1,
		SellerID:  "seller-a",
		Source:    "seller:seller-a",
		URL:       "https://www.bestbuy.ca/en-ca/product/extreme",
	}
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
		subs:         []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}},
	}
	notifier := &computeTestNotifier{}
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:            true,
		Verdict:         ebaySoldVerdictPass,
		Query:           "PowerEdge R740",
		ComparableCount: 3,
		MedianPrice:     2000,
		P25Price:        1500,
		GapPct:          99.95,
		GapAmount:       1999,
		Comparables: []ComputeExternalComparable{
			{Title: "Dell PowerEdge R740 256GB RAM 24 Core Xeon Server", Price: 1500, Source: "ebay-sold"},
		},
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: []Product{product}}, notifier, "", true, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent = %d, want 1 extreme fallback alert", len(notifier.sent))
	}
	if !notifier.sent[0].IsWarm || !notifier.sent[0].IsLavaHot {
		t.Fatalf("sent product warm/hot = %v/%v, want lava-hot", notifier.sent[0].IsWarm, notifier.sent[0].IsLavaHot)
	}
	saved := store.observations[computeObservationKey(product)]
	if saved.EbaySoldVerdict != ebaySoldVerdictPass {
		t.Fatalf("EbaySoldVerdict = %q, want pass", saved.EbaySoldVerdict)
	}
	if len(saved.EbaySoldComparables) != 1 {
		t.Fatalf("EbaySoldComparables = %d, want saved comparables", len(saved.EbaySoldComparables))
	}
}

func TestComputeProcessorExtremeFallbackDoesNotPromoteFetchError(t *testing.T) {
	product := Product{
		SKU:       "extreme",
		Name:      "Dell PowerEdge R740 768GB RAM 24 Core Xeon Server",
		SalePrice: 1,
		SellerID:  "seller-a",
		Source:    "seller:seller-a",
		URL:       "https://www.bestbuy.ca/en-ca/product/extreme",
	}
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
		subs:         []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}},
	}
	notifier := &computeTestNotifier{}
	verifier := &computeTestSoldVerifier{verification: EbaySoldVerification{
		Pass:    true,
		Verdict: ebaySoldVerdictFetchError,
		Query:   "PowerEdge R740",
		Error:   "all backends blocked",
	}}
	processor := NewComputeProcessor(store, computeTestClient{products: []Product{product}}, notifier, "", true, nil)
	processor.SetSoldVerifier(verifier)

	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent = %d, want no extreme fallback alert without decisive eBay comps", len(notifier.sent))
	}
	saved := store.observations[computeObservationKey(product)]
	if saved.EbaySoldVerdict != ebaySoldVerdictFetchError {
		t.Fatalf("EbaySoldVerdict = %q, want fetch_error", saved.EbaySoldVerdict)
	}
	if saved.IsWarm || saved.IsLavaHot {
		t.Fatalf("saved warm/hot = %v/%v, want false without promotion", saved.IsWarm, saved.IsLavaHot)
	}
}

func TestComputeProcessorNoSubscriptionBaselinesCurrentOutliers(t *testing.T) {
	products := append([]Product{computeCandidate("candidate", "seller-a", 650)}, computeCompsForProcessor()...)
	store := &computeTestStore{
		observations: make(map[string]ComputeObservation),
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
	if len(notifier.sent) != 0 {
		t.Fatalf("sent without subscriptions = %d, want 0", len(notifier.sent))
	}

	store.subs = []models.Subscription{{SubscriptionType: "bestbuy", DealType: "bb_compute", ChannelID: "chan"}}
	if err := processor.ProcessComputeOutliers(context.Background()); err != nil {
		t.Fatalf("second ProcessComputeOutliers() error = %v", err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("sent after subscription enabled = %d, want existing baseline suppressed", len(notifier.sent))
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
		computeCandidate("c1", "seller-b", 2500),
		computeCandidate("c2", "seller-c", 2600),
		computeCandidate("c3", "seller-d", 2700),
		computeCandidate("c4", "seller-e", 2800),
		computeCandidate("c5", "seller-f", 2900),
	}
}
