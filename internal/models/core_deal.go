package models

import "time"

// CoreDeal represents the parsed and normalized deal from incoming Discord notifications.
type CoreDeal struct {
	EventID       string    `docstore:"eventId"`
	SourcePackage string    `docstore:"sourcePackage"`
	ProductName   string    `docstore:"productName"`
	StoreName     string    `docstore:"storeName"`
	Category      string    `docstore:"category"`
	PriceCAD      float64   `docstore:"priceCad"`
	OriginalPrice float64   `docstore:"originalPrice"`
	OriginalCurr  string    `docstore:"originalCurrency"`
	Link          string    `docstore:"link"`
	ImageBase64   string    `docstore:"imageBase64,omitempty"`
	ReceivedAt    time.Time `docstore:"receivedAt"`

	// Price history stats at trigger time
	MinPriceSeen float64 `docstore:"minPriceSeen"`
	P25PriceSeen float64 `docstore:"p25PriceSeen"`
	P50PriceSeen float64 `docstore:"p50PriceSeen"`
	P75PriceSeen float64 `docstore:"p75PriceSeen"`
	HistoryCount int     `docstore:"historyCount"`
	AnomalyType  string  `docstore:"anomalyType,omitempty"`
	BoxPlot      string  `docstore:"boxPlot,omitempty"`
}

// CoreAlert tracks an active Discord alert to allow appending new stores/links to the same embed.
type CoreAlert struct {
	PriceCAD   float64           `docstore:"priceCad"`
	StoreNames []string          `docstore:"storeNames"`
	Links      []string          `docstore:"links"`
	MessageIDs map[string]string `docstore:"messageIds"` // channelID -> messageID
	FiredAt    time.Time         `docstore:"firedAt"`
	Deal       CoreDeal          `docstore:"deal"` // Original deal data to regenerate the embed
}

// CorePriceHistory stores the historical price points for a specific product.
type CorePriceHistory struct {
	ProductName  string       `docstore:"productName"`
	Category     string       `docstore:"category"`
	Prices       []float64    `docstore:"prices"` // CAD prices
	EventIDs     []string     `docstore:"eventIds,omitempty"`
	RecentAlerts []CoreAlert  `docstore:"recentAlerts,omitempty"`
	LastUpdated  time.Time    `docstore:"lastUpdated"`
}

// CoreCategoryStats tracks how many observations have been seen in a category.
type CoreCategoryStats struct {
	Category    string    `docstore:"category"`
	TotalCount  int       `docstore:"totalCount"`
	LastUpdated time.Time `docstore:"lastUpdated"`
}

// CoreRule represents a single regex search and replace rule.
type CoreRule struct {
	Pattern string `docstore:"pattern"`
	Replace string `docstore:"replace"`
}

// CoreRulesConfig represents the configuration document containing active or pending rules.
type CoreRulesConfig struct {
	ID        string     `docstore:"id"`
	Rules     []CoreRule `docstore:"rules"`
	UpdatedAt time.Time  `docstore:"updated_at"`
}
