package models

import "time"

// GeminiQuotaStatus stores the current fallback state for Gemini models
type GeminiQuotaStatus struct {
	CurrentDay      string    `docstore:"currentDay"`      // YYYY-MM-DD in Pacific Time
	CurrentModel    string    `docstore:"currentModel"`    // model ID like "gemini-2.5-flash"
	AllExhausted    bool      `docstore:"allExhausted"`    // true when all model tiers are exhausted for the day
	ExhaustedAt     time.Time `docstore:"exhaustedAt"`     // when exhaustion was declared (for cooldown recovery)
	CurrentLocation string    `docstore:"currentLocation"` // active Vertex AI region
	LastUpdated     time.Time `docstore:"lastUpdated"`
}
