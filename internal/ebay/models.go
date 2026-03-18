package ebay

import "time"

// EbaySeller represents a tracked eBay seller stored in Firestore.
type EbaySeller struct {
	Username    string    `firestore:"username"`
	DisplayName string    `firestore:"displayName,omitempty"`
	IsActive    bool      `firestore:"isActive"`
	AddedAt     time.Time `firestore:"addedAt"`
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
	usernames := []string{
		"vipoutletcanada",
		"themobilebase",
		"helloworld2003",
		"uventure",
		"shanikuma6",
		"calgarycomputerwholesale",
		"originallaptoppartsandelectronics",
		"acer",
		"itworkstations",
		"richyhub",
		"neweggcanada",
		"surplusbydesign",
		"deltaserverstore",
		"ssdwholesale",
		"fsanchez89",
		"jz.cpu1",
		"qnrvr17",
		"buythatapple",
		"vipoutlet",
		"officialbestbuy",
	}

	sellers := make([]EbaySeller, len(usernames))
	for i, u := range usernames {
		sellers[i] = EbaySeller{
			Username: u,
			IsActive: true,
			AddedAt:  now,
		}
	}
	return sellers
}
