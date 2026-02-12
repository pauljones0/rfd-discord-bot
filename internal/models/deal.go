package models

import (
	"errors"
	"time"
)

// ErrDealExists is returned when attempting to create a deal that already exists.
var ErrDealExists = errors.New("deal already exists")

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	Title                  string    `firestore:"title"`
	PostURL                string    `firestore:"postURL"`
	AuthorName             string    `firestore:"authorName"`
	AuthorURL              string    `firestore:"authorURL"`
	ThreadImageURL         string    `firestore:"threadImageURL,omitempty"`
	LikeCount              int       `firestore:"likeCount"`
	CommentCount           int       `firestore:"commentCount"`
	ViewCount              int       `firestore:"viewCount"`
	ActualDealURL          string    `firestore:"actualDealURL,omitempty"`
	FirestoreID            string    `firestore:"-"` // To store the Firestore document ID, not stored in Firestore itself
	DiscordMessageID       string    `firestore:"discordMessageID,omitempty"`
	LastUpdated            time.Time `firestore:"lastUpdated"`
	PublishedTimestamp     time.Time `firestore:"publishedTimestamp"` // Parsed from PostedTime
	DiscordLastUpdatedTime time.Time `firestore:"discordLastUpdatedTime,omitempty"`
}
