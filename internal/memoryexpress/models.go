package memoryexpress

import "time"

// Product represents a single clearance item scraped from the Memory Express Clearance Centre.
type Product struct {
	SKU            string  `firestore:"sku"`
	ILC            string  `firestore:"ilc,omitempty"`
	Title          string  `firestore:"title"`
	URL            string  `firestore:"url"`
	ImageURL       string  `firestore:"imageURL,omitempty"`
	RegularPrice   float64 `firestore:"regularPrice"`
	ClearancePrice float64 `firestore:"clearancePrice"`
	SalePrice      float64 `firestore:"salePrice"`
	DiscountPct    float64 `firestore:"discountPct"`
	Stock          int     `firestore:"stock"`
	Category       string  `firestore:"category"`
	StoreCode      string  `firestore:"storeCode"`
	StoreName      string  `firestore:"storeName"`
}

// AnalyzedProduct extends Product with AI analysis results.
type AnalyzedProduct struct {
	Product
	CleanTitle  string    `firestore:"cleanTitle"`
	IsWarm      bool      `firestore:"isWarm"`
	IsLavaHot   bool      `firestore:"isLavaHot"`
	Summary     string    `firestore:"summary,omitempty"`
	ProcessedAt time.Time `firestore:"processedAt"`
	LastSeen    time.Time `firestore:"lastSeen"`
}

// AnalyzeResult is the JSON structure returned by Gemini for a clearance product.
type AnalyzeResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
	Summary    string `json:"summary"`
}
