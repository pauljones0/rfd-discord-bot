package models

import "time"

// GeminiQuotaStatus stores the current fallback state for Gemini models
type GeminiQuotaStatus struct {
	CurrentDay      string    // YYYY-MM-DD in Pacific Time
	CurrentModel    string    // model ID like "gemini-2.5-flash"
	AllExhausted    bool      // true until cooldown or Pacific day rollover
	ExhaustedAt     time.Time // when exhaustion was declared (for cooldown recovery)
	CurrentLocation string    // Legacy field: active API key index encoded as keyN
	LastUpdated     time.Time
}
