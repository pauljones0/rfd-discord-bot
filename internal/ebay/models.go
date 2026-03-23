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

// EbayItem represents an eBay listing that passed AI analysis (used for Discord notifications).
type EbayItem struct {
	ItemID     string
	Title      string
	CleanTitle string
	Price      string
	Currency   string
	ItemURL    string
	ImageURL   string
	Seller     string
	Condition  string

	IsWarm    bool
	IsLavaHot bool
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

// EbayBatchScreenResult represents the tier-1 AI screening result for a single item.
type EbayBatchScreenResult struct {
	ItemID     string `json:"item_id"`
	CleanTitle string `json:"clean_title"`
	IsTopDeal  bool   `json:"is_top_deal"`
	Reasoning  string `json:"reasoning"`
}

// EbayVerifyResult represents the tier-2 AI verification result for an individual item.
type EbayVerifyResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
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
		{username: "uventure"},
		{username: "shanikuma6"},
		{username: "originallaptoppartsandelectronics"},
		{username: "neweggcanada"},
		{username: "surplusbydesign"},
		{username: "ssdwholesale"},
		{username: "fsanchez89"},
		{username: "qnrvr17"},
		{username: "buythatapple"},
		{username: "outlut"},                 // Outlut Computers & Electronics Inc.
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
