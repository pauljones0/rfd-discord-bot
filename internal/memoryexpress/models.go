package memoryexpress

import "time"

// Product represents a single clearance item scraped from the Memory Express Clearance Centre.
type Product struct {
	SKU            string  `docstore:"sku"`
	ILC            string  `docstore:"ilc,omitempty"`
	Title          string  `docstore:"title"`
	URL            string  `docstore:"url"`
	ImageURL       string  `docstore:"imageURL,omitempty"`
	RegularPrice   float64 `docstore:"regularPrice"`
	ClearancePrice float64 `docstore:"clearancePrice"`
	SalePrice      float64 `docstore:"salePrice"`
	DiscountPct    float64 `docstore:"discountPct"`
	Stock          int     `docstore:"stock"`
	Category       string  `docstore:"category"`
	StoreCode      string  `docstore:"storeCode"`
	StoreName      string  `docstore:"storeName"`
}

// AnalyzedProduct extends Product with AI analysis results.
type AnalyzedProduct struct {
	Product
	CleanTitle  string    `docstore:"cleanTitle"`
	IsWarm      bool      `docstore:"isWarm"`
	IsLavaHot   bool      `docstore:"isLavaHot"`
	Summary     string    `docstore:"summary,omitempty"`
	ProcessedAt time.Time `docstore:"processedAt"`
	LastSeen    time.Time `docstore:"lastSeen"`
}

// AnalyzeResult is the JSON structure returned by Gemini for a clearance product.
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

// Tier1SelectionLimit returns the maximum number of products that should advance
// from tier-1 screening to tier-2 verification for a batch.
func Tier1SelectionLimit(count int) int {
	limit := count * 30 / 100
	if limit < 1 && count > 0 {
		limit = 1
	}
	return limit
}
