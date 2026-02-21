package models

import (
	"errors"
	"time"
)

// ErrDealExists is returned when attempting to create a deal that already exists.
var ErrDealExists = errors.New("deal already exists")

// DealInfo represents the structured information for a deal.
type DealInfo struct {
	Title                  string    `firestore:"title" validate:"required"`
	PostURL                string    `firestore:"postURL" validate:"required,url"`
	AuthorName             string    `firestore:"authorName"`
	AuthorURL              string    `firestore:"authorURL"`
	ThreadImageURL         string    `firestore:"threadImageURL,omitempty" validate:"omitempty,url"`
	LikeCount              int       `firestore:"likeCount" validate:"gte=0"`
	CommentCount           int       `firestore:"commentCount" validate:"gte=0"`
	ViewCount              int       `firestore:"viewCount" validate:"gte=0"`
	ActualDealURL          string    `firestore:"actualDealURL,omitempty" validate:"omitempty,url"`
	FirestoreID            string    `firestore:"-"` // To store the Firestore document ID, not stored in Firestore itself
	DiscordMessageID       string    `firestore:"discordMessageID,omitempty"`
	LastUpdated            time.Time `firestore:"lastUpdated"`
	PublishedTimestamp     time.Time `firestore:"publishedTimestamp" validate:"required"` // Parsed from PostedTime
	DiscordLastUpdatedTime time.Time `firestore:"discordLastUpdatedTime,omitempty"`

	Price    string `firestore:"price,omitempty"`
	Retailer string `firestore:"retailer,omitempty"`

	// AI Enriched Fields
	CleanTitle  string `firestore:"cleanTitle,omitempty"`
	IsLavaHot   bool   `firestore:"isLavaHot,omitempty"`
	AIProcessed bool   `firestore:"aiProcessed"`

	// Detailed Content
	Description string `firestore:"description,omitempty"`
	Comments    string `firestore:"comments,omitempty"` // Flattened comments for AI context
	Summary     string `firestore:"summary,omitempty"`  // RFD editor summary if available
}
