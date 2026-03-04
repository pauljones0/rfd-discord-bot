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
}

type JSONLDComment struct {
	Type          string    `json:"@type"` // Should be "comment"
	Text          string    `json:"text"`
	DatePublished time.Time `json:"datePublished"`
}
