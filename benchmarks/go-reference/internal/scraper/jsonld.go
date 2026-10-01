package scraper

import "time"

// JSONLDDiscussionForumPosting represents the structure of the JSON-LD data
// embedded in RedFlagDeals topic pages.
type JSONLDDiscussionForumPosting struct {
	Context       string          `json:"@context"`
	Type          string          `json:"@type"` // Should be "DiscussionForumPosting"
	Headline      string          `json:"headline"`
	Text          string          `json:"text"` // The main post content
	DatePublished time.Time       `json:"datePublished"`
	Comment       []JSONLDComment `json:"comment"`
	About         *JSONLDProduct  `json:"about,omitempty"`
}

type JSONLDComment struct {
	Type          string    `json:"@type"` // Should be "comment"
	Text          string    `json:"text"`
	DatePublished time.Time `json:"datePublished"`
}

type JSONLDProduct struct {
	Type            string                 `json:"@type"` // Should be "Product"
	Name            string                 `json:"name"`
	Description     string                 `json:"description"`
	Offers          *JSONLDOffer           `json:"offers,omitempty"`
	Brand           *JSONLDBrand           `json:"brand,omitempty"`
	AggregateRating *JSONLDAggregateRating `json:"aggregateRating,omitempty"`
}

type JSONLDOffer struct {
	Type          string `json:"@type"` // Should be "Offer"
	Price         string `json:"price"`
	PriceCurrency string `json:"priceCurrency"`
	Availability  string `json:"availability"`
	URL           string `json:"url"`
}

type JSONLDBrand struct {
	Name string `json:"name"`
}

type JSONLDAggregateRating struct {
	RatingValue interface{} `json:"ratingValue"` // Can be string or float
	RatingCount interface{} `json:"ratingCount"` // Can be string or int
}
