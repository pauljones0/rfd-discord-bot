package ebay

import "time"

// EbaySeller represents a tracked eBay seller stored in Firestore.
type EbaySeller struct {
	Username    string    `firestore:"username"`
	DisplayName string    `firestore:"displayName,omitempty"`
	Marketplace string    `firestore:"marketplace,omitempty"` // "EBAY_CA" or "EBAY_US"; defaults to EBAY_CA
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

// TrackedItem represents an eBay listing being monitored for price drops in Firestore.
type TrackedItem struct {
	ItemID      string    `firestore:"itemID"`
	Title       string    `firestore:"title"`
	Price       float64   `firestore:"price"`
	Currency    string    `firestore:"currency"`
	Seller      string    `firestore:"seller"`
	Condition   string    `firestore:"condition"`
	ItemURL     string    `firestore:"itemURL"`
	ImageURL    string    `firestore:"imageURL"`
	FirstSeenAt time.Time `firestore:"firstSeenAt"`
	LastSeenAt  time.Time `firestore:"lastSeenAt"`
}

// EbayItem represents an eBay listing for Discord notification (price drop).
type EbayItem struct {
	ItemID    string
	Title     string
	Price     string
	Currency  string
	ItemURL   string
	ImageURL  string
	Seller    string
	Condition string
}

// EbayPollState tracks the state of the last eBay polling run (singleton in bot_config).
type EbayPollState struct {
	LastPollTime  time.Time `firestore:"lastPollTime"`
	LastPollItems int       `firestore:"lastPollItems"`
	LastError     string    `firestore:"lastError,omitempty"`
	LastUpdated   time.Time `firestore:"lastUpdated"`
}

// BrowseAPIItem represents a single item from the eBay Browse API response.
type BrowseAPIItem struct {
	ItemID           string      `json:"itemId"`
	Title            string      `json:"title"`
	Price            *Price      `json:"price"`
	ItemWebURL       string      `json:"itemWebUrl"`
	Image            *Image      `json:"image"`
	Seller           *SellerInfo `json:"seller"`
	Condition        string      `json:"condition"`
	CategoryID       string      `json:"categoryId"`
	BuyingOptions    []string    `json:"buyingOptions"`
	ItemCreationDate string      `json:"itemCreationDate"` // ISO8601
}

// Price represents the eBay API price object.
type Price struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
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
	}

	entries := []entry{
		// Canadian sellers (ebay.ca)
		{username: "vipoutletcanada"},
		{username: "helloworld2003"},
		{username: "shanikuma6"},
		{username: "originallaptoppartsandelectronics"},
		{username: "neweggcanada"},
		{username: "surplusbydesign"},
		{username: "ssdwholesale"},
		{username: "qnrvr17"},
		{username: "buythatapple"},
		{username: "montrealcomputers"},      // Montreal Computers CANADA
	}

	sellers := make([]EbaySeller, len(entries))
	for i, e := range entries {
		sellers[i] = EbaySeller{
			Username:    e.username,
			Marketplace: e.marketplace,
			IsActive:    true,
			AddedAt:     now,
		}
	}
	return sellers
}
