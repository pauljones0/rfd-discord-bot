package models

import (
	"errors"
	"time"
)

// ErrDealExists is returned when attempting to create a deal that already exists.
var ErrDealExists = errors.New("deal already exists")

const dealRetention = 30 * 24 * time.Hour

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	Title                  string            `docstore:"title" validate:"required"`
	PostURL                string            `docstore:"postURL" validate:"required,url"`
	Category               string            `docstore:"category,omitempty"`
	ThreadImageURL         string            `docstore:"threadImageURL,omitempty" validate:"omitempty,url"`
	ActualDealURL          string            `docstore:"actualDealURL,omitempty" validate:"omitempty,url"`
	DocumentID             string            `docstore:"-"`                           // Document ID; not stored in the document itself.
	DiscordMessageIDs      map[string]string `docstore:"discordMessageIDs,omitempty"` // Mapping of ChannelID -> MessageID
	LastUpdated            time.Time         `docstore:"lastUpdated"`
	PublishedTimestamp     time.Time         `docstore:"publishedTimestamp" validate:"required"` // Parsed from PostedTime
	DiscordLastUpdatedTime time.Time         `docstore:"discordLastUpdatedTime,omitempty"`
	ExpiresAt              time.Time         `docstore:"expiresAt,omitempty"`

	Threads      []ThreadContext `docstore:"threads"`
	SearchTokens []string        `docstore:"searchTokens,omitempty"`

	Price         string `docstore:"price,omitempty"`
	OriginalPrice string `docstore:"originalPrice,omitempty"`
	Savings       string `docstore:"savings,omitempty"`
	Retailer      string `docstore:"retailer,omitempty"`

	// AI Enriched Fields
	CleanTitle  string `docstore:"cleanTitle,omitempty"`
	AIProcessed bool   `docstore:"aiProcessed"`

	// Rank Tracking — sticky flags set by engagement heat score
	HasBeenWarm bool `docstore:"hasBeenWarm,omitempty"`
	HasBeenHot  bool `docstore:"hasBeenHot,omitempty"`

	// Detailed Content
	Description string `docstore:"description,omitempty"`
	Comments    string `docstore:"comments,omitempty"` // Flattened comments for AI context
	Summary     string `docstore:"summary,omitempty"`  // RFD editor summary if available
}

// ThreadContext represents an individual RedFlagDeals thread that is part of a DealIdea.
type ThreadContext struct {
	DocumentID         string `docstore:"documentID"`
	PostURL            string `docstore:"postURL" validate:"required,url"`
	LikeCount          int    `docstore:"likeCount"`
	CommentCount       int    `docstore:"commentCount" validate:"gte=0"`
	ViewCount          int    `docstore:"viewCount" validate:"gte=0"`
	ViewCountAvailable bool   `docstore:"viewCountAvailable,omitempty"`
	NotFound           bool   `docstore:"notFound,omitempty"`
}

// Stats returns the engagement metrics from the primary (most popular) thread.
// Threads are sorted by LikeCount desc by processor.sortThreads(), so Threads[0]
// is the most engaged thread. Using the primary thread avoids integer-division
// averaging that can round likes down to 0 when duplicate threads are merged
// (e.g., 2 total likes across 3 threads → 2/3 = 0).
func (d *DealInfo) Stats() (likes, comments, views int) {
	if len(d.Threads) == 0 {
		return 0, 0, 0
	}
	primary := d.Threads[0]
	return primary.LikeCount, primary.CommentCount, primary.ViewCount
}

// EngagementStats returns the primary thread's engagement metrics and whether
// the current scrape actually exposed a view count on the RFD card.
func (d *DealInfo) EngagementStats() (likes, comments, views int, hasViews bool) {
	if len(d.Threads) == 0 {
		return 0, 0, 0, false
	}
	primary := d.Threads[0]
	return primary.LikeCount, primary.CommentCount, primary.ViewCount, primary.ViewCountAvailable
}

// PrimaryPostURL returns the primary (most popular) thread URL.
func (d *DealInfo) PrimaryPostURL() string {
	if len(d.Threads) == 0 {
		return d.PostURL // Fallback to legacy field just in case
	}
	return d.Threads[0].PostURL
}

// ExpiryTime returns the retention cutoff for the deal.
func (d DealInfo) ExpiryTime() time.Time {
	if !d.ExpiresAt.IsZero() {
		return d.ExpiresAt
	}
	if d.PublishedTimestamp.IsZero() {
		return time.Time{}
	}
	return d.PublishedTimestamp.Add(dealRetention)
}
