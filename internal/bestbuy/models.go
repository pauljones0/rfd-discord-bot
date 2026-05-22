package bestbuy

import "time"

const (
	AlertKindPriceDrop      = "price_drop"
	AlertKindComputeOutlier = "compute_outlier"
)

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
	{
		ID:         "418240",
		Name:       "OpenBox",
		SearchPath: "sellerName:OpenBox",
		SearchURL:  "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AOpenBox",
		IsActive:   true,
	},
}

// Product represents a single product from the Best Buy Canada search API.
type Product struct {
	SKU             string            `docstore:"sku"`
	Name            string            `docstore:"name"`
	URL             string            `docstore:"url"`
	ImageURL        string            `docstore:"imageURL,omitempty"`
	RegularPrice    float64           `docstore:"regularPrice"`
	SalePrice       float64           `docstore:"salePrice"`
	SaleEndDate     string            `docstore:"saleEndDate,omitempty"`
	CategoryID      string            `docstore:"categoryID,omitempty"`
	CategoryName    string            `docstore:"categoryName,omitempty"`
	SellerID        string            `docstore:"sellerID,omitempty"`
	SellerName      string            `docstore:"sellerName,omitempty"`
	CustomerRating  float64           `docstore:"customerRating,omitempty"`
	IsMarketplace   bool              `docstore:"isMarketplace"`
	IsClearance     bool              `docstore:"isClearance"`
	IsOpenBox       bool              `docstore:"isOpenBox"`
	Source          string            `docstore:"source"` // "marketplace" or "openbox"
	LastIndex       string            `docstore:"lastIndex,omitempty"`
	IndexTimestamp  int64             `docstore:"indexTimestamp,omitempty"`
	SearchStartDate int64             `docstore:"searchStartDate,omitempty"`
	SearchEndDate   int64             `docstore:"searchEndDate,omitempty"`
	InStock         bool              `docstore:"inStock,omitempty"`
	InStockKnown    bool              `docstore:"inStockKnown,omitempty"`
	IsVisible       bool              `docstore:"isVisible,omitempty"`
	VisibilityKnown bool              `docstore:"visibilityKnown,omitempty"`
	OnlineOnly      bool              `docstore:"onlineOnly,omitempty"`
	InStoreOnly     bool              `docstore:"inStoreOnly,omitempty"`
	IsOnSale        bool              `docstore:"isOnSale,omitempty"`
	Advertised      bool              `docstore:"advertised,omitempty"`
	BrandName       string            `docstore:"brandName,omitempty"`
	ModelNumber     string            `docstore:"modelNumber,omitempty"`
	PrimaryUPC      string            `docstore:"primaryUPC,omitempty"`
	OfferEndDate    string            `docstore:"offerEndDate,omitempty"`
	Specs           map[string]string `docstore:"specs,omitempty"`

	ComparableCount       int       `docstore:"comparableCount,omitempty"`
	ComparableMedianPrice float64   `docstore:"comparableMedianPrice,omitempty"`
	ComparableP25Price    float64   `docstore:"comparableP25Price,omitempty"`
	ComparableLowestPrice float64   `docstore:"comparableLowestPrice,omitempty"`
	ComparableDiscountPct float64   `docstore:"comparableDiscountPct,omitempty"`
	ComparableSummary     string    `docstore:"comparableSummary,omitempty"`
	AvailabilityCheckedAt time.Time `docstore:"availabilityCheckedAt,omitempty"`

	SoldCompCount       int               `docstore:"ebaySoldCount,omitempty"`
	SoldCompMedianPrice float64           `docstore:"ebaySoldMedianPrice,omitempty"`
	SoldCompP25Price    float64           `docstore:"ebaySoldP25Price,omitempty"`
	SoldCompGapAmount   float64           `docstore:"ebaySoldGapAmount,omitempty"`
	SoldCompGapPct      float64           `docstore:"ebaySoldGapPct,omitempty"`
	SoldCompSummary     string            `docstore:"ebaySoldSummary,omitempty"`
	SoldCompCheckedAt   time.Time         `docstore:"ebaySoldCheckedAt,omitempty"`
	SoldCompExamples    []SoldCompListing `docstore:"ebaySoldExamples,omitempty"`
}

// AnalyzedProduct extends Product with AI analysis results and computed fields.
type AnalyzedProduct struct {
	Product
	CleanTitle               string    `docstore:"cleanTitle"`
	IsWarm                   bool      `docstore:"isWarm"`
	IsLavaHot                bool      `docstore:"isLavaHot"`
	Summary                  string    `docstore:"summary,omitempty"`
	DiscountPct              float64   `docstore:"discountPct"`
	ProcessedAt              time.Time `docstore:"processedAt"`
	LastSeen                 time.Time `docstore:"lastSeen"`
	InitialRegularPrice      float64   `docstore:"initialRegularPrice,omitempty"`
	InitialSalePrice         float64   `docstore:"initialSalePrice,omitempty"`
	InitialEffectivePrice    float64   `docstore:"initialEffectivePrice,omitempty"`
	PreviousRegularPrice     float64   `docstore:"previousRegularPrice,omitempty"`
	PreviousSalePrice        float64   `docstore:"previousSalePrice,omitempty"`
	PreviousEffectivePrice   float64   `docstore:"previousEffectivePrice,omitempty"`
	LowestSeenEffectivePrice float64   `docstore:"lowestSeenEffectivePrice,omitempty"`
	LastPriceDropDetectedAt  time.Time `docstore:"lastPriceDropDetectedAt,omitempty"`
	LastPriceDropAlertPrice  float64   `docstore:"lastPriceDropAlertPrice,omitempty"`
	LastPriceDropAlertAt     time.Time `docstore:"lastPriceDropAlertAt,omitempty"`
	LastPriceDropAlertKey    string    `docstore:"lastPriceDropAlertKey,omitempty"`
	AlertKind                string    `docstore:"-"`
	PriceDropAmount          float64   `docstore:"-"`
	PriceDropPct             float64   `docstore:"-"`
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
