package bestbuy

import "time"

// Seller represents a Best Buy Marketplace seller to monitor.
type Seller struct {
	ID         string    `docstore:"id"`
	Name       string    `docstore:"name"`
	SearchPath string    `docstore:"searchPath,omitempty"`
	SearchURL  string    `docstore:"searchURL,omitempty"`
	IsActive   bool      `docstore:"isActive"`
	AddedAt    time.Time `docstore:"addedAt"`
}

// DefaultSellers is the hardcoded list of marketplace sellers to track.
var DefaultSellers = []Seller{
	{
		ID:         "591375",
		Name:       "Tech Outlet Center",
		SearchPath: "sellerName:Tech Outlet Center",
		SearchURL:  "https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center",
		IsActive:   true,
	},
	{
		ID:         "1247543",
		Name:       "Parts Search",
		SearchPath: "sellerName:Parts Search",
		SearchURL:  "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search",
		IsActive:   true,
	},
}

// Product represents a single product from the Best Buy Canada search API.
type Product struct {
	SKU            string  `docstore:"sku"`
	Name           string  `docstore:"name"`
	URL            string  `docstore:"url"`
	ImageURL       string  `docstore:"imageURL,omitempty"`
	RegularPrice   float64 `docstore:"regularPrice"`
	SalePrice      float64 `docstore:"salePrice"`
	SaleEndDate    string  `docstore:"saleEndDate,omitempty"`
	CategoryName   string  `docstore:"categoryName,omitempty"`
	SellerID       string  `docstore:"sellerID,omitempty"`
	SellerName     string  `docstore:"sellerName,omitempty"`
	CustomerRating float64 `docstore:"customerRating,omitempty"`
	IsMarketplace  bool    `docstore:"isMarketplace"`
	IsClearance    bool    `docstore:"isClearance"`
	IsOpenBox      bool    `docstore:"isOpenBox"`
	Source         string  `docstore:"source"` // "marketplace" or "openbox"
}

// AnalyzedProduct extends Product with AI analysis results and computed fields.
type AnalyzedProduct struct {
	Product
	CleanTitle  string    `docstore:"cleanTitle"`
	IsWarm      bool      `docstore:"isWarm"`
	IsLavaHot   bool      `docstore:"isLavaHot"`
	Summary     string    `docstore:"summary,omitempty"`
	DiscountPct float64   `docstore:"discountPct"`
	ProcessedAt time.Time `docstore:"processedAt"`
	LastSeen    time.Time `docstore:"lastSeen"`
}

// AnalyzeResult is the JSON structure returned by Gemini for a Best Buy product.
type AnalyzeResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
	Summary    string `json:"summary"`
}

// BatchAnalyzeResult is the JSON structure returned by Gemini for batch tier-2 verification.
type BatchAnalyzeResult struct {
	SKU        string `json:"sku"`
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
	Summary    string `json:"summary"`
}

// BatchScreenResult represents the tier-1 AI screening result for a single item.
type BatchScreenResult struct {
	SKU        string `json:"sku"`
	CleanTitle string `json:"clean_title"`
	IsTopDeal  bool   `json:"is_top_deal"`
	Reasoning  string `json:"reasoning"`
}
