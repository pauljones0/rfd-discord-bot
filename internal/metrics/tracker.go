package metrics

import (
	"log/slog"
	"sync/atomic"
)

// Tracker tracks API usage metrics across a processor run.
// Thread-safe via atomic operations for counters and mutex for string fields.
type Tracker struct {
	processor string

	// Gemini metrics
	geminiCalls        atomic.Int64
	geminiInputTokens  atomic.Int64
	geminiOutputTokens atomic.Int64

	// Carfax metrics
	carfaxValuations atomic.Int64
	carfaxFailures   atomic.Int64

	// Discord metrics
	discordMessagesSent atomic.Int64

	// General processing metrics
	adsScraped   atomic.Int64
	adsProcessed atomic.Int64
	dealsFound   atomic.Int64
}

// NewTracker creates a new API usage tracker for a specific processor.
func NewTracker(processor string) *Tracker {
	return &Tracker{processor: processor}
}

// TrackGeminiCall records a Gemini API call with token counts.
func (t *Tracker) TrackGeminiCall(inputTokens, outputTokens int) {
	t.geminiCalls.Add(1)
	t.geminiInputTokens.Add(int64(inputTokens))
	t.geminiOutputTokens.Add(int64(outputTokens))
}

// TrackCarfaxValuation records a Carfax valuation attempt.
func (t *Tracker) TrackCarfaxValuation(success bool) {
	if success {
		t.carfaxValuations.Add(1)
	} else {
		t.carfaxFailures.Add(1)
	}
}

// TrackDiscordMessage records a Discord message sent.
func (t *Tracker) TrackDiscordMessage() {
	t.discordMessagesSent.Add(1)
}

// TrackAdsScraped records the number of ads scraped.
func (t *Tracker) TrackAdsScraped(count int) {
	t.adsScraped.Add(int64(count))
}

// TrackAdProcessed records a processed ad.
func (t *Tracker) TrackAdProcessed() {
	t.adsProcessed.Add(1)
}

// TrackDealFound records a deal that was found and posted.
func (t *Tracker) TrackDealFound() {
	t.dealsFound.Add(1)
}

// LogSummary emits an INFO-level log with all accumulated metrics.
// Call this at the end of each processor run.
func (t *Tracker) LogSummary() {
	slog.Info("api_usage_summary",
		"processor", t.processor,
		"gemini_calls", t.geminiCalls.Load(),
		"gemini_input_tokens", t.geminiInputTokens.Load(),
		"gemini_output_tokens", t.geminiOutputTokens.Load(),
		"carfax_valuations", t.carfaxValuations.Load(),
		"carfax_failures", t.carfaxFailures.Load(),
		"discord_messages_sent", t.discordMessagesSent.Load(),
		"ads_scraped", t.adsScraped.Load(),
		"ads_processed", t.adsProcessed.Load(),
		"deals_found", t.dealsFound.Load(),
	)
}
