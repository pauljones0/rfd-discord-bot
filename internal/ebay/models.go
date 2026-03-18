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

// EbayItem represents an eBay listing stored in Firestore (only warm/hot items are persisted).
type EbayItem struct {
	ItemID    string `firestore:"itemID"`
	Title     string `firestore:"title"`
	CleanTitle string `firestore:"cleanTitle,omitempty"`
	Price      string `firestore:"price,omitempty"`
	Currency   string `firestore:"currency,omitempty"`
	ItemURL    string `firestore:"itemURL"`
	ImageURL   string `firestore:"imageURL,omitempty"`
	Seller     string `firestore:"seller"`
	Condition  string `firestore:"condition,omitempty"`
	CategoryID string `firestore:"categoryID,omitempty"`

	IsWarm    bool `firestore:"isWarm,omitempty"`
	IsLavaHot bool `firestore:"isLavaHot,omitempty"`

	ListingDate time.Time `firestore:"listingDate"`          // When eBay says it was listed
	FirstSeenAt time.Time `firestore:"firstSeenAt"`          // When our bot first discovered it
	LastCheckedAt time.Time `firestore:"lastCheckedAt"`      // Last API poll that returned this item

	DiscordMessageIDs map[string]string `firestore:"discordMessageIDs,omitempty"`
	LastUpdated       time.Time         `firestore:"lastUpdated"`
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
	ItemID       string `json:"itemId"`
	Title        string `json:"title"`
	Price        *Price `json:"price"`
	ItemWebURL   string `json:"itemWebUrl"`
	Image        *Image `json:"image"`
	Seller       *SellerInfo `json:"seller"`
	Condition    string `json:"condition"`
	CategoryID   string `json:"categoryId"`
	BuyingOptions []string `json:"buyingOptions"`
	ItemCreationDate string `json:"itemCreationDate"` // ISO8601
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
	Username       string `json:"username"`
	FeedbackScore  int    `json:"feedbackScore"`
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
		{username: "themobilebase"},
		{username: "helloworld2003"},
		{username: "uventure"},
		{username: "shanikuma6"},
		{username: "calgarycomputerwholesale"},
		{username: "originallaptoppartsandelectronics"},
		{username: "richyhub"},
		{username: "neweggcanada"},
		{username: "surplusbydesign"},
		{username: "ssdwholesale"},
		{username: "fsanchez89"},
		{username: "qnrvr17"},
		{username: "buythatapple"},

		// US sellers (ebay.com)
		{username: "vipoutlet", marketplace: "EBAY_US"},
		{username: "itworkstations", marketplace: "EBAY_US"},
		{username: "deltaserverstore", marketplace: "EBAY_US"},
		{username: "officialbestbuy", marketplace: "EBAY_US"},
		{username: "acer", marketplace: "EBAY_US"},
		{username: "jz.cpu1", marketplace: "EBAY_US"},
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
