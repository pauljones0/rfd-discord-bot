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
	aiRequests         atomic.Int64
	aiReturned         atomic.Int64
	aiMissing          atomic.Int64
	aiParseFailures    atomic.Int64
	aiRetries          atomic.Int64
	aiPosted           atomic.Int64
	aiNotPosted        atomic.Int64

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

// TrackAIOutcome records structured model-output quality for a single AI stage.
func (t *Tracker) TrackAIOutcome(context string, requested, returned, missing, parseFailures, retries int) {
	t.aiRequests.Add(int64(requested))
	t.aiReturned.Add(int64(returned))
	t.aiMissing.Add(int64(missing))
	t.aiParseFailures.Add(int64(parseFailures))
	t.aiRetries.Add(int64(retries))
	slog.Info("ai_outcome",
		"processor", t.processor,
		"context", context,
		"requested", requested,
		"returned", returned,
		"missing", missing,
		"parse_failures", parseFailures,
		"retries", retries,
	)
}

// TrackAIDecision records whether an AI-reviewed candidate led to a notification.
func (t *Tracker) TrackAIDecision(context string, posted bool) {
	if posted {
		t.aiPosted.Add(1)
	} else {
		t.aiNotPosted.Add(1)
	}
	slog.Info("ai_decision",
		"processor", t.processor,
		"context", context,
		"posted", posted,
	)
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
		"ai_requested", t.aiRequests.Load(),
		"ai_returned", t.aiReturned.Load(),
		"ai_missing", t.aiMissing.Load(),
		"ai_parse_failures", t.aiParseFailures.Load(),
		"ai_retries", t.aiRetries.Load(),
		"ai_posted", t.aiPosted.Load(),
		"ai_not_posted", t.aiNotPosted.Load(),
		"carfax_valuations", t.carfaxValuations.Load(),
		"carfax_failures", t.carfaxFailures.Load(),
		"discord_messages_sent", t.discordMessagesSent.Load(),
		"ads_scraped", t.adsScraped.Load(),
		"ads_processed", t.adsProcessed.Load(),
		"deals_found", t.dealsFound.Load(),
	)
}
