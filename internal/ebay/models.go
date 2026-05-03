package ebay

import "time"

// EbaySeller represents a tracked eBay seller stored in Firestore.
type EbaySeller struct {
	Username    string    `firestore:"username"`
	DisplayName string    `firestore:"displayName,omitempty"`
	Marketplace string    `firestore:"marketplace,omitempty"` // "EBAY_CA" or "EBAY_US"; defaults to EBAY_CA
	CategoryIDs []string  `firestore:"categoryIDs,omitempty"`
	IsActive    bool      `firestore:"isActive"`
	AddedAt     time.Time `firestore:"addedAt"`
}

// MarketplaceID returns the eBay marketplace ID for the seller.
// Defaults to "EBAY_CA" when not set (backward-compatible with existing Firestore documents).
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

// TrackedItem represents an eBay listing being monitored for price drops in Firestore.
type TrackedItem struct {
	ItemID            string    `firestore:"itemID"`
	Title             string    `firestore:"title"`
	Price             float64   `firestore:"price"` // Effective price after item-level coupons when available.
	BasePrice         float64   `firestore:"basePrice,omitempty"`
	CouponDiscount    float64   `firestore:"couponDiscount,omitempty"`
	CouponCode        string    `firestore:"couponCode,omitempty"`
	CouponMessage     string    `firestore:"couponMessage,omitempty"`
	CouponSource      string    `firestore:"couponSource,omitempty"`
	OriginalPrice     float64   `firestore:"originalPrice,omitempty"`
	LastNotifiedPrice float64   `firestore:"lastNotifiedPrice,omitempty"`
	DropCount         int       `firestore:"dropCount,omitempty"`
	Currency          string    `firestore:"currency"`
	Seller            string    `firestore:"seller"`
	Condition         string    `firestore:"condition"`
	ItemURL           string    `firestore:"itemURL"`
	ImageURL          string    `firestore:"imageURL"`
	FirstSeenAt       time.Time `firestore:"firstSeenAt"`
	LastSeenAt        time.Time `firestore:"lastSeenAt"`
}

// EbayItem represents an eBay listing for Discord notification (price drop).
type EbayItem struct {
	ItemID                   string
	Title                    string
	CurrentPrice             float64
	PreviousPrice            float64
	BasePrice                float64
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
}

// EbayPollState tracks the state of the last eBay polling run (singleton in bot_config).
type EbayPollState struct {
	LastPollTime  time.Time `firestore:"lastPollTime"`
	LastPollItems int       `firestore:"lastPollItems"`
	LastError     string    `firestore:"lastError,omitempty"`
	LastUpdated   time.Time `firestore:"lastUpdated"`
}

// StoreCoupon caches seller/store-level coupons discovered from rendered item pages.
// The Browse API remains the listing source of truth; these coupons are only used
// during post-drop effective price calculations.
type StoreCoupon struct {
	Marketplace         string    `firestore:"marketplace"`
	Seller              string    `firestore:"seller"`
	Signature           string    `firestore:"signature"`
	DiscountType        string    `firestore:"discountType,omitempty"` // "fixed", "percent", "none", or "unknown"
	DiscountValue       float64   `firestore:"discountValue,omitempty"`
	MaxDiscount         float64   `firestore:"maxDiscount,omitempty"`
	Code                string    `firestore:"code,omitempty"`
	RawText             string    `firestore:"rawText,omitempty"`
	Confidence          float64   `firestore:"confidence,omitempty"`
	Scope               string    `firestore:"scope,omitempty"` // "store", "item", "none", or "unknown"
	SampledItemIDs      []string  `firestore:"sampledItemIDs,omitempty"`
	SampledItemURLs     []string  `firestore:"sampledItemURLs,omitempty"`
	FirstSeen           time.Time `firestore:"firstSeen"`
	LastSeen            time.Time `firestore:"lastSeen"`
	LastChecked         time.Time `firestore:"lastChecked"`
	ExpiresAt           time.Time `firestore:"expiresAt,omitempty"`
	NextCheckAt         time.Time `firestore:"nextCheckAt"`
	Active              bool      `firestore:"active"`
	ConsecutiveNoCoupon int       `firestore:"consecutiveNoCoupon,omitempty"`
}

// BrowseAPIItem represents a single item from the eBay Browse API response.
type BrowseAPIItem struct {
	ItemID           string      `json:"itemId"`
	Title            string      `json:"title"`
	Price            *Price      `json:"price"`
	ItemHref         string      `json:"itemHref"`
	ItemWebURL       string      `json:"itemWebUrl"`
	Image            *Image      `json:"image"`
	Seller           *SellerInfo `json:"seller"`
	Condition        string      `json:"condition"`
	CategoryID       string      `json:"categoryId"`
	BuyingOptions    []string    `json:"buyingOptions"`
	AvailableCoupons bool        `json:"availableCoupons"`
	CouponDiscount   float64     `json:"-"`
	CouponCode       string      `json:"-"`
	CouponMessage    string      `json:"-"`
	CouponSource     string      `json:"-"`
	ItemCreationDate string      `json:"itemCreationDate"` // ISO8601
	Marketplace      string      `json:"-"`
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

// DefaultSellers returns the hardcoded default seller list for initial Firestore seeding.
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

		// American sellers (ebay.com)
		{username: "vipoutlet", marketplace: "EBAY_US", categoryIDs: []string{"58058", "293", "15032", "1249"}},
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
