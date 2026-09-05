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
	Title             string
	PostURL           string
	Category          string
	ThreadImageURL    string
	ActualDealURL     string
	DocumentID        string            // Stable identity; retained across upgrades.
	DiscordMessageIDs map[string]string // Mapping of ChannelID -> MessageID
	// DiscordMessageApplicationIDs records the application that authored each
	// receipt. Imported receipts still prevent reposting, but another application
	// must never attempt to edit them.
	DiscordMessageApplicationIDs map[string]string
	LastUpdated                  time.Time
	PublishedTimestamp           time.Time // Parsed from PostedTime
	DiscordLastUpdatedTime       time.Time
	ExpiresAt                    time.Time

	Threads      []ThreadContext
	SearchTokens []string

	Price         string
	OriginalPrice string
	Savings       string
	Retailer      string

	// Optional title cleanup; never used for deal qualification.
	CleanTitle  string
	AIProcessed bool

	// Rank Tracking — sticky flags set by engagement heat score
	HasBeenWarm bool
	HasBeenHot  bool

	// Detailed Content
	Description string
	Comments    string // Legacy imported detail, retained for data compatibility
	Summary     string // RFD editor summary if available
}

// DealDetailFetchStats summarizes RFD detail-page fetch health for a run.
type DealDetailFetchStats struct {
	Requested int
	Attempted int
	Succeeded int
	Failed    int
	NotFound  int
}

// ThreadContext represents an individual RedFlagDeals thread that is part of one grouped deal.
type ThreadContext struct {
	DocumentID         string
	PostURL            string
	LikeCount          int
	CommentCount       int
	ViewCount          int
	ViewCountAvailable bool
	NotFound           bool
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
