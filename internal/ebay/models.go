package ebay

import "time"

// EbaySeller represents a tracked eBay seller stored in the document store.
type EbaySeller struct {
	Username    string    `docstore:"username"`
	DisplayName string    `docstore:"displayName,omitempty"`
	Marketplace string    `docstore:"marketplace,omitempty"` // "EBAY_CA" or "EBAY_US"; defaults to EBAY_CA
	CategoryIDs []string  `docstore:"categoryIDs,omitempty"`
	IsActive    bool      `docstore:"isActive"`
	AddedAt     time.Time `docstore:"addedAt"`
}

// MarketplaceID returns the eBay marketplace ID for the seller.
// Defaults to "EBAY_CA" when not set, preserving older stored seller records.
func (s EbaySeller) MarketplaceID() string {
	if s.Marketplace == "" {
		return "EBAY_CA"
	}
	return s.Marketplace
}

// EffectiveCategoryIDs returns the seller-specific Browse category scope.
// Falls back to the default tech categories when no explicit scope is configured.
func (s EbaySeller) EffectiveCategoryIDs() []string {
	if len(s.CategoryIDs) == 0 {
		return append([]string(nil), browseTechCategoryIDs...)
	}

	seen := make(map[string]struct{}, len(s.CategoryIDs))
	ids := make([]string, 0, len(s.CategoryIDs))
	for _, id := range s.CategoryIDs {
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return append([]string(nil), browseTechCategoryIDs...)
	}
	return ids
}

// TrackedItem represents an eBay listing being monitored for price drops.
type TrackedItem struct {
	ItemID                   string    `docstore:"itemID"`
	Title                    string    `docstore:"title"`
	Price                    float64   `docstore:"price"` // Effective price after item-level coupons when available.
	BasePrice                float64   `docstore:"basePrice,omitempty"`
	CouponDiscount           float64   `docstore:"couponDiscount,omitempty"`
	CouponCode               string    `docstore:"couponCode,omitempty"`
	CouponMessage            string    `docstore:"couponMessage,omitempty"`
	CouponSource             string    `docstore:"couponSource,omitempty"`
	CouponSignature          string    `docstore:"couponSignature,omitempty"`
	OriginalPrice            float64   `docstore:"originalPrice,omitempty"`
	LastNotifiedPrice        float64   `docstore:"lastNotifiedPrice,omitempty"`
	LastCouponAlertSignature string    `docstore:"lastCouponAlertSignature,omitempty"`
	LastCouponAlertAt        time.Time `docstore:"lastCouponAlertAt,omitempty"`
	DropCount                int       `docstore:"dropCount,omitempty"`
	Currency                 string    `docstore:"currency"`
	Seller                   string    `docstore:"seller"`
	Condition                string    `docstore:"condition"`
	ItemURL                  string    `docstore:"itemURL"`
	ImageURL                 string    `docstore:"imageURL"`
	FirstSeenAt              time.Time `docstore:"firstSeenAt"`
	LastSeenAt               time.Time `docstore:"lastSeenAt"`
}

// EbayItem represents an eBay listing for Discord notification (price drop).
type EbayItem struct {
	ItemID                   string
	Title                    string
	CurrentPrice             float64
	PreviousPrice            float64
	BasePrice                float64
	OriginalPrice            float64
	CouponDiscount           float64
	CouponCode               string
	CouponMessage            string
	CouponSource             string
	PriceDrop                float64
	PercentDrop              float64
	DropCount                int
	Currency                 string
	ItemURL                  string
	ImageURL                 string
	Seller                   string
	SellerFeedbackScore      int
	SellerFeedbackPercentage string
	Condition                string
	Marketplace              string
	ListedAt                 time.Time
	SoldMedian               float64
	IsGoodDeal               bool
}

// EbayPollState tracks the state of the last eBay polling run (singleton in bot_config).
type EbayPollState struct {
	LastPollTime  time.Time `docstore:"lastPollTime"`
	LastPollItems int       `docstore:"lastPollItems"`
	LastError     string    `docstore:"lastError,omitempty"`
	LastUpdated   time.Time `docstore:"lastUpdated"`
}

// StoreCoupon caches seller/store-level coupons discovered from rendered item pages.
// The Browse API remains the listing source of truth; these coupons are only used
// during post-drop effective price calculations.
type StoreCoupon struct {
	Marketplace               string    `docstore:"marketplace"`
	Seller                    string    `docstore:"seller"`
	Signature                 string    `docstore:"signature"`
	DiscountType              string    `docstore:"discountType,omitempty"` // "fixed", "percent", "none", or "unknown"
	DiscountValue             float64   `docstore:"discountValue,omitempty"`
	MaxDiscount               float64   `docstore:"maxDiscount,omitempty"`
	FormulaType               string    `docstore:"formulaType,omitempty"` // "flat", "percent", "percent_cap", threshold variants, "ambiguous", or "unknown"
	ThresholdAmount           float64   `docstore:"thresholdAmount,omitempty"`
	Code                      string    `docstore:"code,omitempty"`
	RawText                   string    `docstore:"rawText,omitempty"`
	Confidence                float64   `docstore:"confidence,omitempty"`
	InferenceMaxErrorCents    int       `docstore:"inferenceMaxErrorCents,omitempty"`
	InferenceCompetingRules   int       `docstore:"inferenceCompetingRules,omitempty"`
	InferenceNeedsMoreSamples bool      `docstore:"inferenceNeedsMoreSamples,omitempty"`
	InferenceNextSampleHint   string    `docstore:"inferenceNextSampleHint,omitempty"`
	Scope                     string    `docstore:"scope,omitempty"` // "store", "item", "none", or "unknown"
	SampledItemIDs            []string  `docstore:"sampledItemIDs,omitempty"`
	SampledItemURLs           []string  `docstore:"sampledItemURLs,omitempty"`
	FirstSeen                 time.Time `docstore:"firstSeen"`
	LastSeen                  time.Time `docstore:"lastSeen"`
	LastChecked               time.Time `docstore:"lastChecked"`
	ExpiresAt                 time.Time `docstore:"expiresAt,omitempty"`
	NextCheckAt               time.Time `docstore:"nextCheckAt"`
	Active                    bool      `docstore:"active"`
	ConsecutiveNoCoupon       int       `docstore:"consecutiveNoCoupon,omitempty"`
}

// CouponObservation is one browser-derived coupon datapoint for a seller item.
// Observations are used to infer whether a coupon formula is safe to apply across
// a seller store without repeatedly opening listing pages.
type CouponObservation struct {
	Marketplace    string    `docstore:"marketplace"`
	Seller         string    `docstore:"seller"`
	Signature      string    `docstore:"signature"`
	ItemID         string    `docstore:"itemID"`
	CategoryID     string    `docstore:"categoryID,omitempty"`
	ItemURL        string    `docstore:"itemURL,omitempty"`
	BasePrice      float64   `docstore:"basePrice"`
	DiscountAmount float64   `docstore:"discountAmount"`
	Code           string    `docstore:"code,omitempty"`
	Message        string    `docstore:"message,omitempty"`
	EvidenceText   string    `docstore:"evidenceText,omitempty"`
	Scope          string    `docstore:"scope,omitempty"`
	Backend        string    `docstore:"backend,omitempty"`
	Confidence     float64   `docstore:"confidence,omitempty"`
	ObservedAt     time.Time `docstore:"observedAt"`
	ExpiresAt      time.Time `docstore:"expiresAt,omitempty"`
}

// BrowseAPIItem represents a single item from the eBay Browse API response.
type BrowseAPIItem struct {
	ItemID                     string      `json:"itemId"`
	Title                      string      `json:"title"`
	Price                      *Price      `json:"price"`
	ItemHref                   string      `json:"itemHref"`
	ItemWebURL                 string      `json:"itemWebUrl"`
	Image                      *Image      `json:"image"`
	Seller                     *SellerInfo `json:"seller"`
	Condition                  string      `json:"condition"`
	CategoryID                 string      `json:"categoryId"`
	BuyingOptions              []string    `json:"buyingOptions"`
	AvailableCoupons           bool        `json:"availableCoupons"`
	CouponDiscount             float64     `json:"-"`
	CouponCode                 string      `json:"-"`
	CouponMessage              string      `json:"-"`
	CouponSource               string      `json:"-"`
	CouponSignature            string      `json:"-"`
	ItemCreationDate           string      `json:"itemCreationDate"` // ISO8601
	Marketplace                string      `json:"-"`
	EstimatedAvailableQuantity *int        `json:"estimatedAvailableQuantity"`
}

// Price represents the eBay API price object.
type Price struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

// BrowseAPIItemDetail represents fields fetched from the Browse getItem endpoint.
type BrowseAPIItemDetail struct {
	ItemID           string            `json:"itemId"`
	AvailableCoupons []AvailableCoupon `json:"availableCoupons"`
}

// AvailableCoupon represents an item-level coupon returned by the Browse API.
type AvailableCoupon struct {
	DiscountAmount *Price `json:"discountAmount"`
	Message        string `json:"message"`
	RedemptionCode string `json:"redemptionCode"`
	TermsWebURL    string `json:"termsWebUrl"`
}

// Image represents the eBay API image object.
type Image struct {
	ImageURL string `json:"imageUrl"`
}

// SellerInfo represents the eBay API seller info.
type SellerInfo struct {
	Username           string `json:"username"`
	FeedbackScore      int    `json:"feedbackScore"`
	FeedbackPercentage string `json:"feedbackPercentage"`
}

// BrowseSearchResponse represents the eBay Browse API search response.
type BrowseSearchResponse struct {
	ItemSummaries []BrowseAPIItem `json:"itemSummaries"`
	Total         int             `json:"total"`
	Next          string          `json:"next"` // Pagination URL
	Limit         int             `json:"limit"`
	Offset        int             `json:"offset"`
}

// DefaultSellers returns the hardcoded default seller list for initial seeding.
func DefaultSellers() []EbaySeller {
	now := time.Now()

	type entry struct {
		username    string
		marketplace string // empty = EBAY_CA (default)
		categoryIDs []string
	}

	entries := []entry{
		// Canadian sellers (ebay.ca)
		{username: "vipoutletcanada", categoryIDs: []string{"58058", "293", "15032", "1249"}},
		{username: "neweggcanada", categoryIDs: []string{"58058", "293", "15032", "1249"}},
		{username: "surplusbydesign", categoryIDs: []string{"58058", "293", "15032", "1249"}},
		{username: "ssdwholesale", categoryIDs: []string{"58058"}},
		{username: "montrealcomputers", categoryIDs: []string{"58058", "293", "15032"}},
	}

	sellers := make([]EbaySeller, len(entries))
	for i, e := range entries {
		sellers[i] = EbaySeller{
			Username:    e.username,
			Marketplace: e.marketplace,
			CategoryIDs: append([]string(nil), e.categoryIDs...),
			IsActive:    true,
			AddedAt:     now,
		}
	}
	return sellers
}
