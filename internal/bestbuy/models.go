package bestbuy

import "time"

// Seller represents a Best Buy Marketplace seller to monitor.
type Seller struct {
	ID   string
	Name string
}

// DefaultSellers is the hardcoded list of marketplace sellers to track.
var DefaultSellers = []Seller{
	{ID: "591375", Name: "Tech Outlet Center"},
	{ID: "459214", Name: "PRO OB"},
	{ID: "35466598", Name: "Reversify"},
}

// Product represents a single product from the Best Buy Canada search API.
type Product struct {
	SKU            string  `firestore:"sku"`
	Name           string  `firestore:"name"`
	URL            string  `firestore:"url"`
	ImageURL       string  `firestore:"imageURL,omitempty"`
	RegularPrice   float64 `firestore:"regularPrice"`
	SalePrice      float64 `firestore:"salePrice"`
	SaleEndDate    string  `firestore:"saleEndDate,omitempty"`
	CategoryName   string  `firestore:"categoryName,omitempty"`
	SellerID       string  `firestore:"sellerID,omitempty"`
	SellerName     string  `firestore:"sellerName,omitempty"`
	CustomerRating float64 `firestore:"customerRating,omitempty"`
	IsMarketplace  bool    `firestore:"isMarketplace"`
	IsClearance    bool    `firestore:"isClearance"`
	IsOpenBox      bool    `firestore:"isOpenBox"`
	Source         string  `firestore:"source"` // "marketplace" or "openbox"
}

// AnalyzedProduct extends Product with AI analysis results and computed fields.
type AnalyzedProduct struct {
	Product
	CleanTitle  string    `firestore:"cleanTitle"`
	IsWarm      bool      `firestore:"isWarm"`
	IsLavaHot   bool      `firestore:"isLavaHot"`
	Summary     string    `firestore:"summary,omitempty"`
	DiscountPct float64   `firestore:"discountPct"`
	ProcessedAt time.Time `firestore:"processedAt"`
	LastSeen    time.Time `firestore:"lastSeen"`
}

// AnalyzeResult is the JSON structure returned by Gemini for a Best Buy product.
type AnalyzeResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
	Summary    string `json:"summary"`
}
