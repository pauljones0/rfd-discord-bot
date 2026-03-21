package models

import "time"

// GeminiQuotaStatus stores the current fallback state for Gemini models
type GeminiQuotaStatus struct {
	CurrentDay      string    `firestore:"currentDay"`      // YYYY-MM-DD in Pacific Time
	CurrentModel    string    `firestore:"currentModel"`    // model ID like "gemini-2.5-flash"
	AllExhausted    bool      `firestore:"allExhausted"`    // true when all model tiers are exhausted for the day
	ExhaustedAt     time.Time `firestore:"exhaustedAt"`     // when exhaustion was declared (for cooldown recovery)
	CurrentLocation string    `firestore:"currentLocation"` // active Vertex AI region
	LastUpdated     time.Time `firestore:"lastUpdated"`
}
