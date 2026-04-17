package models

import (
	"errors"
	"time"
)

// ErrDealExists is returned when attempting to create a deal that already exists.
var ErrDealExists = errors.New("deal already exists")

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	Title                  string            `firestore:"title" validate:"required"`
	PostURL                string            `firestore:"postURL" validate:"required,url"`
	Category               string            `firestore:"category,omitempty"`
	ThreadImageURL         string            `firestore:"threadImageURL,omitempty" validate:"omitempty,url"`
	ActualDealURL          string            `firestore:"actualDealURL,omitempty" validate:"omitempty,url"`
	FirestoreID            string            `firestore:"-"`                           // To store the Firestore document ID, not stored in Firestore itself
	DiscordMessageIDs      map[string]string `firestore:"discordMessageIDs,omitempty"` // Mapping of ChannelID -> MessageID
	LastUpdated            time.Time         `firestore:"lastUpdated"`
	PublishedTimestamp     time.Time         `firestore:"publishedTimestamp" validate:"required"` // Parsed from PostedTime
	DiscordLastUpdatedTime time.Time         `firestore:"discordLastUpdatedTime,omitempty"`

	Threads      []ThreadContext `firestore:"threads"`
	SearchTokens []string        `firestore:"searchTokens,omitempty"`

	Price         string `firestore:"price,omitempty"`
	OriginalPrice string `firestore:"originalPrice,omitempty"`
	Savings       string `firestore:"savings,omitempty"`
	Retailer      string `firestore:"retailer,omitempty"`

	// AI Enriched Fields
	CleanTitle  string `firestore:"cleanTitle,omitempty"`
	AIProcessed bool   `firestore:"aiProcessed"`

	// Rank Tracking — sticky flags set by engagement heat score
	HasBeenWarm bool `firestore:"hasBeenWarm,omitempty"`
	HasBeenHot  bool `firestore:"hasBeenHot,omitempty"`

	// Detailed Content
	Description string `firestore:"description,omitempty"`
	Comments    string `firestore:"comments,omitempty"` // Flattened comments for AI context
	Summary     string `firestore:"summary,omitempty"`  // RFD editor summary if available
}

// ThreadContext represents an individual RedFlagDeals thread that is part of a DealIdea.
type ThreadContext struct {
	FirestoreID        string `firestore:"firestoreID"`
	PostURL            string `firestore:"postURL" validate:"required,url"`
	LikeCount          int    `firestore:"likeCount"`
	CommentCount       int    `firestore:"commentCount" validate:"gte=0"`
	ViewCount          int    `firestore:"viewCount" validate:"gte=0"`
	ViewCountAvailable bool   `firestore:"viewCountAvailable,omitempty"`
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
