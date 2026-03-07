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
	DiscordMessageID       string            `firestore:"discordMessageID,omitempty"`  // Legacy webhook-based message ID
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
	IsLavaHot   bool   `firestore:"isLavaHot,omitempty"`
	AIProcessed bool   `firestore:"aiProcessed"`

	// Rank Tracking
	HasBeenWarm bool `firestore:"hasBeenWarm,omitempty"`
	// Deprecated: AI is now the sole source of truth for hotness (IsLavaHot).
	HasBeenHot bool `firestore:"hasBeenHot,omitempty"`

	// Detailed Content
	Description string `firestore:"description,omitempty"`
	Comments    string `firestore:"comments,omitempty"` // Flattened comments for AI context
	Summary     string `firestore:"summary,omitempty"`  // RFD editor summary if available
}

// ThreadContext represents an individual RedFlagDeals thread that is part of a DealIdea.
type ThreadContext struct {
	FirestoreID  string `firestore:"firestoreID"`
	PostURL      string `firestore:"postURL" validate:"required,url"`
	LikeCount    int    `firestore:"likeCount"`
	CommentCount int    `firestore:"commentCount" validate:"gte=0"`
	ViewCount    int    `firestore:"viewCount" validate:"gte=0"`
}

// Stats returns the aggregated metrics for the deal across all threads.
func (d *DealInfo) Stats() (likes, comments, views int) {
	if len(d.Threads) == 0 {
		return 0, 0, 0
	}
	var totalLikes, totalComments, totalViews int
	for _, t := range d.Threads {
		totalLikes += t.LikeCount
		totalComments += t.CommentCount
		totalViews += t.ViewCount
	}
	// We want integer division matching the mathematical average.
	// For negative numbers (likes), standard Go division rounds towards zero.
	// E.g., -5 / 2 = -2
	count := len(d.Threads)
	likes = totalLikes / count
	comments = totalComments / count
	views = totalViews / count

	return likes, comments, views
}

// PrimaryPostURL returns the primary (most popular) thread URL.
func (d *DealInfo) PrimaryPostURL() string {
	if len(d.Threads) == 0 {
		return d.PostURL // Fallback to legacy field just in case
	}
	return d.Threads[0].PostURL
}
