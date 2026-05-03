package models

import "time"

// CarData holds normalized vehicle data extracted from ad descriptions by Gemini.
type CarData struct {
	Year           int    `json:"year"`
	Make           string `json:"make"`
	Model          string `json:"model"`
	Trim           string `json:"trim"`
	Engine         string `json:"engine"`
	Transmission   string `json:"transmission"`
	BodyStyle      string `json:"body_style"`
	Drivetrain     string `json:"drivetrain"`
	Odometer       int    `json:"odometer"`
	Condition      string `json:"condition"`
	SellerRating   string `json:"seller_rating"`
	Description    string `json:"short_description"`
	LikelyGoodDeal bool   `json:"likely_good_deal"`
	VehicleType    string `json:"vehicle_type"`
}

// IsCarfaxEligible returns true if this vehicle type can be valued on Carfax Canada.
func (c *CarData) IsCarfaxEligible() bool {
	switch c.VehicleType {
	case "", "car", "truck", "suv", "van":
		return true
	default:
		return false
	}
}

// FacebookDealAnalysis holds the result of Gemini's deal analysis for Facebook car deals.
type FacebookDealAnalysis struct {
	IsWarm      bool   `json:"is_warm"`
	IsLavaHot   bool   `json:"is_lava_hot"`
	Title       string `json:"title"`
	Summary     string `json:"summary"`
	KnownIssues string `json:"known_issues"`
}

// ScrapedAd holds the data extracted from a single Facebook Marketplace listing.
type ScrapedAd struct {
	ListingID   string // Facebook listing ID extracted from the URL
	Title       string
	Price       float64
	URL         string
	Mileage     string
	Subtitles   []string
	Description string
	Category    string   // Feed category, e.g. "Cars & Trucks", "Motorcycles & Scooters"
	CarData     *CarData // Pre-filled from structured detail page data; nil = use Gemini
}

// FacebookAdRecord represents a processed Facebook ad stored in the document store.
type FacebookAdRecord struct {
	ID           string    `docstore:"id"`
	Title        string    `docstore:"title"`
	Price        string    `docstore:"price"`
	URL          string    `docstore:"url"`
	Year         int       `docstore:"year"`
	Make         string    `docstore:"make"`
	Model        string    `docstore:"model"`
	Mileage      int       `docstore:"mileage"`
	Transmission string    `docstore:"transmission"`
	Condition    string    `docstore:"condition"`
	CarfaxValue  float64   `docstore:"carfax_value"`
	VMRWholesale float64   `docstore:"vmr_wholesale"`
	VMRRetail    float64   `docstore:"vmr_retail"`
	IsGoodDeal   bool      `docstore:"is_good_deal"`
	ProcessedAt  time.Time `docstore:"processed_at"`
	LastSeen     time.Time `docstore:"last_seen"`
}

// PriceHistory stores a daily price snapshot for a vehicle model.
type PriceHistory struct {
	Model string  `docstore:"model"`
	Date  string  `docstore:"date"`
	Value float64 `docstore:"value"`
}
