package ebay

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type ebayProcessorTestStore struct {
	coupons      []StoreCoupon
	savedCoupons []StoreCoupon
	observations []CouponObservation
}

func (s *ebayProcessorTestStore) GetActiveEbaySellers(context.Context) ([]EbaySeller, error) {
	return nil, nil
}

func (s *ebayProcessorTestStore) SeedEbaySellers(context.Context) (bool, error) {
	return false, nil
}

func (s *ebayProcessorTestStore) GetEbayPollState(context.Context) (*EbayPollState, error) {
	return nil, nil
}

func (s *ebayProcessorTestStore) UpdateEbayPollState(context.Context, EbayPollState) error {
	return nil
}

func (s *ebayProcessorTestStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return nil, nil
}

func (s *ebayProcessorTestStore) GetTrackedEbayItems(context.Context) (map[string]TrackedItem, error) {
	return nil, nil
}

func (s *ebayProcessorTestStore) BulkUpsertTrackedEbayItems(context.Context, []TrackedItem) error {
	return nil
}

func (s *ebayProcessorTestStore) DeleteTrackedEbayItems(context.Context, []string) error {
	return nil
}

func (s *ebayProcessorTestStore) GetEbayStoreCoupons(context.Context, string, string) ([]StoreCoupon, error) {
	return append([]StoreCoupon(nil), s.coupons...), nil
}

func (s *ebayProcessorTestStore) SaveEbayStoreCoupon(_ context.Context, coupon StoreCoupon) error {
	s.savedCoupons = append(s.savedCoupons, coupon)
	return nil
}

func (s *ebayProcessorTestStore) GetEbayCouponObservations(context.Context, string, string, string) ([]CouponObservation, error) {
	return append([]CouponObservation(nil), s.observations...), nil
}

func (s *ebayProcessorTestStore) SaveEbayCouponObservation(_ context.Context, observation CouponObservation) error {
	s.observations = append(s.observations, observation)
	return nil
}

func TestShouldNotifyPriceDrop(t *testing.T) {
	tests := []struct {
		name         string
		existing     TrackedItem
		newPrice     float64
		wantBaseline float64
		wantDrop     float64
		wantPercent  float64
		wantNotify   bool
	}{
		{
			name: "first alert uses original price baseline",
			existing: TrackedItem{
				Price:         390,
				OriginalPrice: 500,
			},
			newPrice:     380,
			wantBaseline: 500,
			wantDrop:     120,
			wantPercent:  24,
			wantNotify:   true,
		},
		{
			name: "legacy items fall back to tracked price baseline",
			existing: TrackedItem{
				Price: 500,
			},
			newPrice:     350,
			wantBaseline: 500,
			wantDrop:     150,
			wantPercent:  30,
			wantNotify:   true,
		},
		{
			name: "same previously alerted price is suppressed",
			existing: TrackedItem{
				Price:             500,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     350,
			wantBaseline: 350,
			wantDrop:     0,
			wantPercent:  0,
			wantNotify:   false,
		},
		{
			name: "higher than last alerted price is suppressed",
			existing: TrackedItem{
				Price:             500,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     360,
			wantBaseline: 350,
			wantDrop:     0,
			wantPercent:  0,
			wantNotify:   false,
		},
		{
			name: "deeper drop uses last alerted price baseline",
			existing: TrackedItem{
				Price:             340,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     280,
			wantBaseline: 350,
			wantDrop:     70,
			wantPercent:  20,
			wantNotify:   true,
		},
		{
			name: "deeper price without threshold does not notify",
			existing: TrackedItem{
				Price:             340,
				OriginalPrice:     500,
				LastNotifiedPrice: 350,
			},
			newPrice:     330,
			wantBaseline: 350,
			wantDrop:     20,
			wantPercent:  20.0 / 350.0 * 100.0,
			wantNotify:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBaseline, gotDrop, gotPercent, gotNotify := shouldNotifyPriceDrop(tt.existing, tt.newPrice)
			if gotBaseline != tt.wantBaseline {
				t.Fatalf("baseline = %v, want %v", gotBaseline, tt.wantBaseline)
			}
			if gotDrop != tt.wantDrop {
				t.Fatalf("drop = %v, want %v", gotDrop, tt.wantDrop)
			}
			if gotPercent != tt.wantPercent {
				t.Fatalf("percent = %v, want %v", gotPercent, tt.wantPercent)
			}
			if gotNotify != tt.wantNotify {
				t.Fatalf("notify = %v, want %v", gotNotify, tt.wantNotify)
			}
		})
	}
}

func TestCouponAdjustedPriceCountsAsPriceDrop(t *testing.T) {
	newPrice := effectiveItemPrice(500, 120)
	if newPrice != 380 {
		t.Fatalf("effective price = %v, want 380", newPrice)
	}

	baseline, drop, percent, notify := shouldNotifyPriceDrop(TrackedItem{
		Price:         500,
		OriginalPrice: 500,
	}, newPrice)
	if !notify {
		t.Fatalf("notify = false, want true")
	}
	if baseline != 500 || drop != 120 || percent != 24 {
		t.Fatalf("baseline/drop/percent = %v/%v/%v, want 500/120/24", baseline, drop, percent)
	}
}

func TestCouponIncreaseCountsAsDeeperPriceDrop(t *testing.T) {
	newPrice := effectiveItemPrice(500, 180)
	if newPrice != 320 {
		t.Fatalf("effective price = %v, want 320", newPrice)
	}

	baseline, drop, percent, notify := shouldNotifyPriceDrop(TrackedItem{
		Price:             380,
		OriginalPrice:     500,
		LastNotifiedPrice: 400,
	}, newPrice)
	if !notify {
		t.Fatalf("notify = false, want true")
	}
	if baseline != 400 || drop != 80 || percent != 20 {
		t.Fatalf("baseline/drop/percent = %v/%v/%v, want 400/80/20", baseline, drop, percent)
	}
}

func TestShouldFetchPageCouponOnlyAfterBaseOrAPIDrop(t *testing.T) {
	tests := []struct {
		name              string
		existing          TrackedItem
		basePrice         float64
		apiCouponDiscount float64
		want              bool
	}{
		{
			name: "base price drop triggers page coupon discovery",
			existing: TrackedItem{
				Price:     500,
				BasePrice: 500,
			},
			basePrice: 450,
			want:      true,
		},
		{
			name: "new page coupon alone does not trigger discovery path",
			existing: TrackedItem{
				Price:     500,
				BasePrice: 500,
			},
			basePrice: 500,
			want:      false,
		},
		{
			name: "larger api coupon that lowers effective price triggers",
			existing: TrackedItem{
				Price:          480,
				BasePrice:      500,
				CouponDiscount: 20,
			},
			basePrice:         500,
			apiCouponDiscount: 80,
			want:              true,
		},
		{
			name: "same api coupon does not trigger",
			existing: TrackedItem{
				Price:          480,
				BasePrice:      500,
				CouponDiscount: 20,
			},
			basePrice:         500,
			apiCouponDiscount: 20,
			want:              false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldFetchPageCoupon(tt.existing, tt.basePrice, tt.apiCouponDiscount); got != tt.want {
				t.Fatalf("shouldFetchPageCoupon() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStoreCouponDiscountUsesInferredThreshold(t *testing.T) {
	coupon := StoreCoupon{
		FormulaType:     "threshold_flat",
		DiscountType:    "fixed",
		DiscountValue:   15,
		ThresholdAmount: 75,
	}
	if got := storeCouponDiscount(coupon, 74.99); got != 0 {
		t.Fatalf("discount below threshold = %v, want 0", got)
	}
	if got := storeCouponDiscount(coupon, 80); got != 15 {
		t.Fatalf("discount above threshold = %v, want 15", got)
	}
}

func TestBestCachedCouponSkipsLowConfidenceInferredCoupons(t *testing.T) {
	coupon := StoreCoupon{
		Active:        true,
		Scope:         "store",
		Confidence:    0.6,
		FormulaType:   "flat",
		DiscountType:  "fixed",
		DiscountValue: 50,
	}
	if got := bestCachedCoupon([]StoreCoupon{coupon}, 500); got.DiscountAmount != 0 {
		t.Fatalf("discount = %v, want 0 for low confidence coupon", got.DiscountAmount)
	}
}

func TestBestCachedCouponAppliesInferredPercentCap(t *testing.T) {
	coupon := StoreCoupon{
		Active:        true,
		Scope:         "store",
		Confidence:    0.9,
		FormulaType:   "percent_cap",
		DiscountType:  "percent",
		DiscountValue: 20,
		MaxDiscount:   100,
	}
	if got := bestCachedCoupon([]StoreCoupon{coupon}, 800); got.DiscountAmount != 100 {
		t.Fatalf("discount = %v, want 100", got.DiscountAmount)
	}
}

func TestStoreCouponReadyRequiresHighConfidenceAndLowError(t *testing.T) {
	now := time.Now()
	coupon := StoreCoupon{
		Active:                 true,
		Scope:                  "store",
		Confidence:             0.89,
		FormulaType:            "flat",
		DiscountType:           "fixed",
		DiscountValue:          30,
		InferenceMaxErrorCents: 0,
	}
	if storeCouponReadyForStoreWideUse(coupon, now) {
		t.Fatalf("coupon should not be store-wide ready below confidence threshold")
	}
	coupon.Confidence = 0.9
	coupon.InferenceMaxErrorCents = 3
	if storeCouponReadyForStoreWideUse(coupon, now) {
		t.Fatalf("coupon should not be store-wide ready above max error threshold")
	}
	coupon.InferenceMaxErrorCents = 2
	if !storeCouponReadyForStoreWideUse(coupon, now) {
		t.Fatalf("coupon should be store-wide ready at confidence/error thresholds")
	}
}

func TestStoreCouponFromObservationPromotesOnlyAfterEnoughEvidence(t *testing.T) {
	now := time.Now()
	p := NewProcessor(nil, nil, nil)
	pageCoupon := PageCoupon{
		DiscountAmount: 30,
		DiscountType:   "fixed",
		DiscountValue:  30,
		Message:        "Save C$30.00 with coupon",
		EvidenceText:   "Save C$30.00 with coupon",
		Scope:          "unknown",
		Signature:      "fixed|30.00",
		Confidence:     0.7,
	}

	one := []CouponObservation{{
		Marketplace:    "EBAY_CA",
		Seller:         "seller",
		Signature:      pageCoupon.Signature,
		ItemID:         "item-1",
		BasePrice:      100,
		DiscountAmount: 30,
		EvidenceText:   pageCoupon.EvidenceText,
		ObservedAt:     now,
	}}
	coupon := p.storeCouponFromObservation("EBAY_CA", "seller", pageCoupon, one, nil, now)
	if coupon.Active || coupon.Scope != "item" {
		t.Fatalf("one non-store observation active/scope = %v/%q, want inactive item", coupon.Active, coupon.Scope)
	}

	two := append(one, CouponObservation{
		Marketplace:    "EBAY_CA",
		Seller:         "seller",
		Signature:      pageCoupon.Signature,
		ItemID:         "item-2",
		BasePrice:      200,
		DiscountAmount: 30,
		EvidenceText:   pageCoupon.EvidenceText,
		ObservedAt:     now,
	})
	coupon = p.storeCouponFromObservation("EBAY_CA", "seller", pageCoupon, two, nil, now)
	if !coupon.Active || coupon.Scope != "store" {
		t.Fatalf("two matching observations active/scope = %v/%q, want active store", coupon.Active, coupon.Scope)
	}
	if coupon.FormulaType != "flat" || coupon.DiscountValue != 30 {
		t.Fatalf("formula/value = %q/%v, want flat/30", coupon.FormulaType, coupon.DiscountValue)
	}
}

func TestStoreCouponFromObservationDoesNotThrottleItemCouponUntilExpiry(t *testing.T) {
	now := time.Date(2026, time.May, 10, 12, 0, 0, 0, time.UTC)
	expires := now.Add(21 * 24 * time.Hour)
	p := NewProcessor(nil, nil, nil)
	pageCoupon := PageCoupon{
		DiscountAmount: 111.50,
		DiscountType:   "percent",
		DiscountValue:  10,
		Code:           "VIPOUTLETMAY26",
		Message:        "EXTRA 10% OFF",
		Signature:      "percent|10.00|vipoutletmay26",
		Scope:          "unknown",
		Confidence:     0.8,
		ExpiresAt:      expires,
	}
	observations := []CouponObservation{{
		Marketplace:    "EBAY_US",
		Seller:         "vipoutlet",
		Signature:      pageCoupon.Signature,
		ItemID:         "127579938370",
		BasePrice:      1115,
		DiscountAmount: 111.50,
		Code:           pageCoupon.Code,
		ObservedAt:     now,
		ExpiresAt:      expires,
	}}

	coupon := p.storeCouponFromObservation("EBAY_US", "vipoutlet", pageCoupon, observations, nil, now)
	if coupon.Active || coupon.Scope != "item" {
		t.Fatalf("active/scope = %v/%q, want inactive item coupon", coupon.Active, coupon.Scope)
	}
	if !coupon.NextCheckAt.Equal(now.Add(defaultCouponDiscoveryInterval)) {
		t.Fatalf("NextCheckAt = %s, want 6h throttle instead of expiry %s", coupon.NextCheckAt, expires)
	}
}

func TestNegativeCouponCheckThrottlesFutureChecks(t *testing.T) {
	now := time.Now()
	coupon := negativeCouponCheck("EBAY_CA", "seller", nil, now, 6*time.Hour)
	if coupon.Signature != "none" || coupon.Active {
		t.Fatalf("negative coupon signature/active = %q/%v, want none/false", coupon.Signature, coupon.Active)
	}
	if !coupon.NextCheckAt.Equal(now.Add(6 * time.Hour)) {
		t.Fatalf("NextCheckAt = %s, want %s", coupon.NextCheckAt, now.Add(6*time.Hour))
	}
}

func TestCouponCacheFreshIgnoresLegacyItemCouponExpiryThrottle(t *testing.T) {
	now := time.Date(2026, time.May, 10, 12, 0, 0, 0, time.UTC)
	legacyItemCoupon := StoreCoupon{
		Signature:   "percent|10.00|vipoutletmay26",
		Scope:       "item",
		Active:      false,
		LastChecked: now.Add(-6 * 24 * time.Hour),
		NextCheckAt: time.Date(2026, time.June, 1, 0, 15, 0, 0, time.UTC),
	}
	if fresh, coupon := couponCacheFresh([]StoreCoupon{legacyItemCoupon}, now, 6*time.Hour); fresh {
		t.Fatalf("fresh = true for legacy item coupon %#v; should not throttle seller until expiry", coupon)
	}

	recentItemCoupon := legacyItemCoupon
	recentItemCoupon.LastChecked = now
	recentItemCoupon.NextCheckAt = now.Add(6 * time.Hour)
	if fresh, _ := couponCacheFresh([]StoreCoupon{recentItemCoupon}, now, 6*time.Hour); !fresh {
		t.Fatalf("fresh = false for normal 6h item-coupon sampling throttle")
	}

	activeStoreCoupon := StoreCoupon{
		Signature:              "percent|10.00|vipoutletmay26",
		Scope:                  "store",
		Active:                 true,
		FormulaType:            "percent",
		DiscountType:           "percent",
		DiscountValue:          10,
		Confidence:             0.95,
		InferenceMaxErrorCents: 0,
		LastChecked:            now.Add(-6 * 24 * time.Hour),
		NextCheckAt:            time.Date(2026, time.June, 1, 0, 15, 0, 0, time.UTC),
	}
	if fresh, _ := couponCacheFresh([]StoreCoupon{activeStoreCoupon}, now, 6*time.Hour); !fresh {
		t.Fatalf("fresh = false for active store-wide coupon")
	}
}

func TestApplyCouponForPriceDropAppliesSingleItemPageCoupon(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><h1>Apple iPhone 17 Pro Max</h1><div>Save $120.00 with coupon code SAVE120</div></body></html>`))
	}))
	defer page.Close()

	store := &ebayProcessorTestStore{}
	client := &Client{
		httpClient:     page.Client(),
		couponBackends: []string{"http"},
	}
	processor := NewProcessor(store, client, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	apiItem := BrowseAPIItem{
		ItemID:      "v1|127846479361|0",
		Title:       "Apple iPhone 17 Pro Max 256GB Cosmic Orange LTE Cellular MFXH4LL/A",
		Price:       &Price{Value: "1199.00", Currency: "USD"},
		ItemWebURL:  page.URL,
		Seller:      &SellerInfo{Username: "vipoutlet"},
		Marketplace: "EBAY_US",
	}
	updated, newPrice, activated := processor.applyCouponForPriceDrop(context.Background(), apiItem, TrackedItem{
		ItemID:        "127846479361",
		Price:         4796,
		BasePrice:     4796,
		OriginalPrice: 4796,
		Seller:        "vipoutlet",
	}, 1199, 1199, map[string][]StoreCoupon{}, time.Now(), logger)

	if activated != nil {
		t.Fatalf("activated = %#v, want nil for one item-only observation", activated)
	}
	if updated.CouponDiscount != 120 || updated.CouponCode != "SAVE120" || updated.CouponSource != "page:http" {
		t.Fatalf("coupon fields = discount %.2f code %q source %q", updated.CouponDiscount, updated.CouponCode, updated.CouponSource)
	}
	if newPrice != 1079 {
		t.Fatalf("newPrice = %.2f, want 1079.00", newPrice)
	}
	if len(store.observations) != 1 {
		t.Fatalf("observations = %d, want 1", len(store.observations))
	}
	if len(store.savedCoupons) != 1 || store.savedCoupons[0].Active {
		t.Fatalf("saved coupon = %#v, want one inactive item coupon", store.savedCoupons)
	}
}

func TestRetroactiveCouponAlertsSendOncePerCouponSignature(t *testing.T) {
	now := time.Now()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := NewProcessor(nil, nil, nil)
	apiItems := []BrowseAPIItem{{
		ItemID:      "v1|123|0",
		Title:       "Retro item",
		Price:       &Price{Value: "500.00", Currency: "CAD"},
		ItemWebURL:  "https://www.ebay.ca/itm/123",
		Seller:      &SellerInfo{Username: "seller"},
		Condition:   "Used",
		Marketplace: "EBAY_CA",
	}}
	tracked := map[string]TrackedItem{
		"123": {
			ItemID:        "123",
			Title:         "Retro item",
			Price:         500,
			OriginalPrice: 500,
			BasePrice:     500,
			Seller:        "seller",
			Currency:      "CAD",
			ItemURL:       "https://www.ebay.ca/itm/123",
			FirstSeenAt:   now.Add(-time.Hour),
			LastSeenAt:    now.Add(-time.Hour),
		},
	}
	coupon := StoreCoupon{
		Marketplace:            "EBAY_CA",
		Seller:                 "seller",
		Signature:              "flat|120.00|SAVE120",
		Active:                 true,
		Scope:                  "store",
		Confidence:             0.95,
		FormulaType:            "flat",
		DiscountType:           "fixed",
		DiscountValue:          120,
		Code:                   "SAVE120",
		RawText:                "Save C$120.00 with coupon",
		InferenceMaxErrorCents: 0,
	}
	alerts, writes := p.retroactiveCouponAlerts(apiItems, tracked, map[string]StoreCoupon{
		sellerCouponKey("EBAY_CA", "seller"): coupon,
	}, map[string]bool{}, now, logger)
	if len(alerts) != 1 || len(writes) != 1 {
		t.Fatalf("alerts/writes = %d/%d, want 1/1", len(alerts), len(writes))
	}
	if writes[0].LastCouponAlertSignature != coupon.Signature {
		t.Fatalf("LastCouponAlertSignature = %q, want %q", writes[0].LastCouponAlertSignature, coupon.Signature)
	}

	alerts, writes = p.retroactiveCouponAlerts(apiItems, tracked, map[string]StoreCoupon{
		sellerCouponKey("EBAY_CA", "seller"): coupon,
	}, map[string]bool{}, now, logger)
	if len(alerts) != 0 || len(writes) != 0 {
		t.Fatalf("second retro alerts/writes = %d/%d, want 0/0", len(alerts), len(writes))
	}
}

func TestBestCachedCouponUsesLatestCouponCheckOnly(t *testing.T) {
	oldCheck := time.Now().Add(-2 * time.Hour)
	newCheck := time.Now()
	oldActive := StoreCoupon{
		Active:        true,
		Scope:         "store",
		Confidence:    0.9,
		FormulaType:   "flat",
		DiscountType:  "fixed",
		DiscountValue: 50,
		LastChecked:   oldCheck,
	}
	freshAmbiguous := StoreCoupon{
		Active:      false,
		Scope:       "store",
		Confidence:  0.5,
		FormulaType: "ambiguous",
		LastChecked: newCheck,
	}
	if got := bestCachedCoupon([]StoreCoupon{oldActive, freshAmbiguous}, 500); got.DiscountAmount != 0 {
		t.Fatalf("discount = %v, want 0 after fresher ambiguous check", got.DiscountAmount)
	}
}

func TestPriorDropCount(t *testing.T) {
	tests := []struct {
		name     string
		existing TrackedItem
		want     int
	}{
		{
			name: "uses stored count when present",
			existing: TrackedItem{
				DropCount:         3,
				LastNotifiedPrice: 250,
			},
			want: 3,
		},
		{
			name: "legacy alerted item counts as at least one prior drop",
			existing: TrackedItem{
				LastNotifiedPrice: 250,
			},
			want: 1,
		},
		{
			name:     "never-alerted item starts at zero",
			existing: TrackedItem{},
			want:     0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := priorDropCount(tt.existing); got != tt.want {
				t.Fatalf("priorDropCount() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestIsEbayEligible_MarketplaceFilters(t *testing.T) {
	caItem := EbayItem{Marketplace: "EBAY_CA"}
	usItem := EbayItem{Marketplace: "EBAY_US"}

	tests := []struct {
		name string
		item EbayItem
		sub  models.Subscription
		want bool
	}{
		{
			name: "canadian filter accepts canadian item",
			item: caItem,
			sub:  models.Subscription{DealType: "ebay_ca_price_drop"},
			want: true,
		},
		{
			name: "canadian filter rejects us item",
			item: usItem,
			sub:  models.Subscription{DealType: "ebay_ca_price_drop"},
			want: false,
		},
		{
			name: "us filter accepts us item",
			item: usItem,
			sub:  models.Subscription{DealType: "ebay_us_price_drop"},
			want: true,
		},
		{
			name: "marketplace falls back to item url",
			item: EbayItem{ItemURL: "https://www.ebay.com/itm/123"},
			sub:  models.Subscription{DealType: "ebay_us_price_drop"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEbayEligible(tt.item, tt.sub); got != tt.want {
				t.Fatalf("isEbayEligible() = %v, want %v", got, tt.want)
			}
		})
	}
}
