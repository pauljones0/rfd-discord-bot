package models

import "time"

// GeminiQuotaStatus stores the current fallback state for Gemini models
type GeminiQuotaStatus struct {
	CurrentDay   string    `firestore:"currentDay"`   // YYYY-MM-DD in Pacific Time
	CurrentModel string    `firestore:"currentModel"` // model ID like "gemini-2.5-flash"
	LastUpdated  time.Time `firestore:"lastUpdated"`
}
