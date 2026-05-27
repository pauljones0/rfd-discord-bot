package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

const (
	// exhaustionCooldown is how long to wait before retrying after all regions/tiers
	// are exhausted. DSQ quotas can recover in minutes, so midnight is too aggressive.
	exhaustionCooldown = 30 * time.Minute

	// consecutive504sThreshold triggers a region switch when sustained 504s indicate
	// regional backend congestion (the same root cause as DSQ-driven 429s).
	consecutive504sThreshold = 5
)

type QuotaStore interface {
	GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error)
	UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error
}

type Client struct {
	mu              sync.Mutex               // protects mutable state below
	clients         map[string]*genai.Client // region -> genai client (Vertex AI) or "" -> single client (Gemini API)
	locations       []string                 // ordered region list for failover
	currentLocation string                   // active region
	store           QuotaStore
	fallbackModels  []string
	currentModel    string
	currentDay      string
	allExhausted    bool
	exhaustedAt     time.Time
	consecutive429s int
	consecutive504s int

	// Atomic token counters accumulated by logTokenUsage, drained by DrainTokens.
	pendingInputTokens  atomic.Int64
	pendingOutputTokens atomic.Int64
}

// CleanTitleResult is the response format for batch title cleaning.
type CleanTitleResult struct {
	Index      int    `json:"index"`
	CleanTitle string `json:"clean_title"`
}

func NewClient(ctx context.Context, projectID string, locations []string, apiKeys []string, fallbackModels []string, store QuotaStore) (*Client, error) {
	if len(apiKeys) == 0 && projectID == "" {
		return nil, nil // Return nil client if no credentials provided
	}

	if len(fallbackModels) == 0 {
		return nil, fmt.Errorf("fallback models list is empty")
	}

	clients := make(map[string]*genai.Client)

	if len(apiKeys) > 0 {
		// Gemini API backend: one client per API key for quota rotation.
		// Each key is treated as a "location" so the existing region failover
		// mechanism rotates through keys on quota exhaustion.
		for i, key := range apiKeys {
			cfg := &genai.ClientConfig{
				APIKey:  key,
				Backend: genai.BackendGeminiAPI,
			}
			client, err := genai.NewClient(ctx, cfg)
			if err != nil {
				slog.Warn("Failed to create Gemini API client for key, skipping",
					"key_index", i, "error", err)
				continue
			}
			loc := fmt.Sprintf("key%d", i)
			clients[loc] = client
		}
		if len(clients) == 0 {
			return nil, fmt.Errorf("failed to create any Gemini API clients")
		}
		// Build locations list from successfully created clients
		locations = make([]string, 0, len(clients))
		for i := range apiKeys {
			loc := fmt.Sprintf("key%d", i)
			if _, ok := clients[loc]; ok {
				locations = append(locations, loc)
			}
		}
		slog.Info("Using Gemini API backend with key rotation",
			"num_keys", len(locations))
	} else {
		// Vertex AI backend: one client per region
		if len(locations) == 0 {
			return nil, fmt.Errorf("locations list is empty for Vertex AI backend")
		}
		for _, loc := range locations {
			cfg := &genai.ClientConfig{
				Project:  projectID,
				Location: loc,
				Backend:  genai.BackendVertexAI,
			}
			client, err := genai.NewClient(ctx, cfg)
			if err != nil {
				slog.Warn("Failed to create Vertex AI client for region, skipping", "location", loc, "error", err)
				continue
			}
			clients[loc] = client
		}
		if len(clients) == 0 {
			return nil, fmt.Errorf("failed to create any Vertex AI clients")
		}
		// Filter locations to only those with successful clients
		var validLocations []string
		for _, loc := range locations {
			if _, ok := clients[loc]; ok {
				validLocations = append(validLocations, loc)
			}
		}
		locations = validLocations
		slog.Info("Using Vertex AI backend", "project", projectID, "locations", locations)
	}

	c := &Client{
		clients:         clients,
		locations:       locations,
		currentLocation: locations[0],
		store:           store,
		fallbackModels:  fallbackModels,
	}

	// Load initial state
	c.initQuotaState(ctx)

	return c, nil
}

// activeClient returns the genai.Client for the current region.
func (c *Client) activeClient() *genai.Client {
	return c.clients[c.currentLocation]
}

func (c *Client) initQuotaState(ctx context.Context) {
	if c.store == nil {
		c.currentModel = c.fallbackModels[0]
		return
	}

	c.checkDayRollover(ctx)
}

func getPacificDate() string {
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		// Fallback if system missing tzdata
		loc = time.FixedZone("PST", -8*60*60)
	}
	return time.Now().In(loc).Format("2006-01-02")
}

func (c *Client) checkDayRollover(ctx context.Context) string {
	if len(c.fallbackModels) == 0 {
		return ""
	}
	if c.store == nil {
		return c.fallbackModels[0]
	}

	today := getPacificDate()

	// Check cooldown recovery: if exhausted but cooldown has elapsed, reset everything
	if c.allExhausted && !c.exhaustedAt.IsZero() && time.Since(c.exhaustedAt) >= exhaustionCooldown {
		slog.Info("Retrying after cooldown period",
			"exhausted_at", c.exhaustedAt,
			"cooldown", exhaustionCooldown,
		)
		c.resetToDefaults(ctx, today)
		return c.currentModel
	}

	if c.currentDay == today && c.currentModel != "" {
		return c.currentModel
	}

	quota, err := c.store.GetGeminiQuotaStatus(ctx)
	if err != nil {
		slog.Warn("Failed to get Gemini quota status, using default model", "error", err)
		c.currentModel = c.fallbackModels[0]
		c.currentDay = today
		c.currentLocation = c.locations[0]
		return c.currentModel
	}

	if quota == nil {
		c.resetToDefaults(ctx, today)
		return c.currentModel
	}

	if quota.CurrentDay != today {
		c.currentDay = today
		c.currentModel = c.fallbackModels[0]
		c.currentLocation = c.locations[0]
		c.allExhausted = false
		c.exhaustedAt = time.Time{}
		slog.Info("Day rolled over or restarted, resetting to lowest tier model", "model", c.currentModel, "location", c.currentLocation)
		c.updateStoredQuota(ctx)
	} else {
		c.currentDay = quota.CurrentDay
		c.currentModel = quota.CurrentModel
		c.allExhausted = quota.AllExhausted
		c.exhaustedAt = quota.ExhaustedAt
		if quota.CurrentLocation != "" && c.isKnownLocation(quota.CurrentLocation) {
			c.currentLocation = quota.CurrentLocation
		}
	}

	// Validate the loaded model exists in the current fallback list.
	// A stale storage record from a previous deployment may reference
	// a model that no longer exists in the configured tiers.
	if !c.isKnownModel(c.currentModel) {
		slog.Warn("Loaded model not in fallback list, resetting to default", "stale_model", c.currentModel, "default", c.fallbackModels[0])
		c.currentModel = c.fallbackModels[0]
		c.updateStoredQuota(ctx)
	}

	return c.currentModel
}

// resetToDefaults resets the client to the first region, cheapest model, and clears exhaustion.
func (c *Client) resetToDefaults(ctx context.Context, today string) {
	c.currentModel = c.fallbackModels[0]
	c.currentDay = today
	c.currentLocation = c.locations[0]
	c.allExhausted = false
	c.exhaustedAt = time.Time{}
	c.consecutive429s = 0
	c.consecutive504s = 0
	c.updateStoredQuota(ctx)
}

func (c *Client) isKnownModel(model string) bool {
	for _, m := range c.fallbackModels {
		if m == model {
			return true
		}
	}
	return false
}

func (c *Client) isKnownLocation(location string) bool {
	for _, loc := range c.locations {
		if loc == location {
			return true
		}
	}
	return false
}

func (c *Client) updateStoredQuota(ctx context.Context) {
	if c.store == nil {
		return
	}
	err := c.store.UpdateGeminiQuotaStatus(ctx, models.GeminiQuotaStatus{
		CurrentDay:      c.currentDay,
		CurrentModel:    c.currentModel,
		AllExhausted:    c.allExhausted,
		ExhaustedAt:     c.exhaustedAt,
		CurrentLocation: c.currentLocation,
	})
	if err != nil {
		slog.Error("Failed to update gemini quota status in storage", "error", err)
	}
}

// switchRegion cycles to the next region in the locations list.
// Resets model tier to cheapest and clears consecutive error counters.
// Returns false if no more regions are available.
func (c *Client) switchRegion(ctx context.Context) bool {
	if len(c.locations) <= 1 {
		return false
	}

	for i, loc := range c.locations {
		if loc == c.currentLocation {
			if i+1 < len(c.locations) {
				c.currentLocation = c.locations[i+1]
				c.currentModel = c.fallbackModels[0]
				c.consecutive429s = 0
				c.consecutive504s = 0
				slog.Info("Switching Vertex AI region",
					"from", loc,
					"to", c.currentLocation,
					"model", c.currentModel,
				)
				c.updateStoredQuota(ctx)
				return true
			}
			break
		}
	}
	return false
}

func (c *Client) upgradeModelTier(ctx context.Context) error {
	// If current model isn't in the fallback list (stale state), reset to first tier
	if !c.isKnownModel(c.currentModel) {
		slog.Warn("Current model not in fallback list during upgrade, resetting", "stale_model", c.currentModel, "default", c.fallbackModels[0])
		c.currentModel = c.fallbackModels[0]
		c.updateStoredQuota(ctx)
		return nil
	}

	for i, m := range c.fallbackModels {
		if m == c.currentModel {
			if i+1 < len(c.fallbackModels) {
				c.currentModel = c.fallbackModels[i+1]
				slog.Info("Quota exhausted, upgrading model tier", "new_model", c.currentModel)
				c.updateStoredQuota(ctx)
				return nil
			}
			break
		}
	}

	// All model tiers exhausted for current region — try switching region
	if c.switchRegion(ctx) {
		slog.Info("All model tiers exhausted in region, switched to next region",
			"new_location", c.currentLocation,
			"new_model", c.currentModel,
		)
		return nil
	}

	// No more regions available
	c.allExhausted = true
	c.exhaustedAt = time.Now()
	c.updateStoredQuota(ctx)
	slog.Warn("All Gemini model tiers and regions exhausted, will retry after cooldown",
		"cooldown", exhaustionCooldown,
	)
	return fmt.Errorf("all model tiers exhausted across all regions")
}

// handleRateLimitError handles 429/RESOURCE_EXHAUSTED errors by retrying on the
// same model for transient per-minute rate limits, only escalating the tier after
// multiple consecutive failures suggesting genuine daily quota exhaustion.
// Returns (shouldRetry, backoff duration to sleep before retrying, error).
// Caller must hold c.mu. The returned backoff should be waited on AFTER releasing the lock.
func (c *Client) handleRateLimitError(ctx context.Context) (shouldRetry bool, backoff time.Duration, err error) {
	c.consecutive429s++

	if c.consecutive429s < 3 {
		slog.Info("Rate limited, waiting before retry on same model",
			"model", c.currentModel,
			"location", c.currentLocation,
			"consecutive_429s", c.consecutive429s,
		)
		return true, 5 * time.Second, nil
	}

	// 3+ consecutive 429s — treat as genuine quota exhaustion, escalate tier
	c.consecutive429s = 0
	upgradeErr := c.upgradeModelTier(ctx)
	if upgradeErr != nil {
		return false, 0, upgradeErr
	}
	return true, 0, nil
}

// handle504Error tracks sustained 504/deadline-exceeded errors and triggers
// a region switch when they indicate regional backend congestion.
// Returns true if a region switch occurred and the caller should retry.
func (c *Client) handle504Error(ctx context.Context) bool {
	c.consecutive504s++
	if c.consecutive504s >= consecutive504sThreshold {
		c.consecutive504s = 0
		if c.switchRegion(ctx) {
			slog.Info("Sustained 504 errors, switched region",
				"new_location", c.currentLocation,
				"new_model", c.currentModel,
			)
			return true
		}
	}
	return false
}

func (c *Client) resetConsecutiveErrors() {
	c.consecutive429s = 0
	c.consecutive504s = 0
}

// handleGenerationError classifies and handles errors from genai.GenerateContent calls.
// It updates activeModel when tiers or regions change.
// Caller must hold c.mu. Returns (error, backoff). If backoff > 0, caller should sleep
// that duration AFTER releasing the lock before retrying.
func (c *Client) handleGenerationError(ctx context.Context, genErr error, activeModel *string, attempt int, logContext string) (error, time.Duration) {
	errStr := genErr.Error()

	// 429/quota/model-not-found errors
	if strings.Contains(errStr, "429") || strings.Contains(errStr, "quota") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") ||
		strings.Contains(errStr, "404") || strings.Contains(errStr, "NOT_FOUND") {
		slog.Warn("AI model unavailable or quota exceeded",
			"context", logContext,
			"model", *activeModel,
			"location", c.currentLocation,
			"error", genErr,
		)
		shouldRetry, backoff, handleErr := c.handleRateLimitError(ctx)
		if !shouldRetry {
			return fmt.Errorf("all model tiers exhausted: %w", genErr), 0
		}
		if handleErr != nil {
			return handleErr, 0
		}
		*activeModel = c.currentModel
		return genErr, backoff
	}

	// Transient network/service errors
	if strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "INTERNAL") ||
		strings.Contains(errStr, "Service Unavailable") ||
		strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "deadline exceeded") {
		slog.Warn("Transient Gemini error, retrying",
			"context", logContext,
			"model", *activeModel,
			"location", c.currentLocation,
			"attempt", attempt,
			"error", genErr,
		)
		// Track 504s for region failover
		if strings.Contains(errStr, "504") || strings.Contains(errStr, "deadline exceeded") {
			if c.handle504Error(ctx) {
				*activeModel = c.currentModel
			}
		}
		return genErr, 0
	}

	// Unsupported tool/feature errors (e.g. google_search_retrieval not supported
	// on the current model tier). Upgrade immediately — retrying the same model
	// won't help since it's a capability gap, not a transient issue.
	if strings.Contains(errStr, "400") && (strings.Contains(errStr, "is not supported") || strings.Contains(errStr, "not available")) {
		slog.Warn("AI model does not support requested feature, upgrading tier",
			"context", logContext,
			"model", *activeModel,
			"location", c.currentLocation,
			"error", genErr,
		)
		if err := c.upgradeModelTier(ctx); err != nil {
			// All tiers exhausted in this region, try switching region
			if c.switchRegion(ctx) {
				*activeModel = c.currentModel
				return genErr, 0
			}
			return fmt.Errorf("feature unsupported on all model tiers: %w", genErr), 0
		}
		*activeModel = c.currentModel
		return genErr, 0
	}

	// Permanent errors
	return fmt.Errorf("permanent gemini error: %w", genErr), 0
}

// stripCodeBlock removes markdown code fences (```json ... ``` or ``` ... ```)
// that LLMs sometimes wrap around JSON responses, and trims any trailing text
// after the JSON object/array that Gemini occasionally appends.
func stripCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	s = extractJSONValue(s)
	return s
}

// extractJSONValue finds the outermost JSON object or array in s by tracking
// brace/bracket depth, returning only that portion. If no valid boundary is
// found the original string is returned unchanged so callers still get a
// parse error with the full raw text.
func extractJSONValue(s string) string {
	start := strings.IndexAny(s, "{[")
	if start == -1 {
		return s
	}
	open := rune(s[start])
	var close rune
	if open == '{' {
		close = '}'
	} else {
		close = ']'
	}

	depth := 0
	inString := false
	escaped := false
	for i, ch := range s[start:] {
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == open {
			depth++
		} else if ch == close {
			depth--
			if depth == 0 {
				return s[start : start+i+1]
			}
		}
	}
	return s
}

// logTokenUsage logs token counts from a Gemini response if available
// and accumulates them on the client for later retrieval via DrainTokens.
func (c *Client) logTokenUsage(resp *genai.GenerateContentResponse, context, model, location string) {
	if resp == nil || resp.UsageMetadata == nil {
		return
	}
	um := resp.UsageMetadata
	slog.Info("gemini_token_usage",
		"context", context,
		"model", model,
		"location", location,
		"prompt_tokens", um.PromptTokenCount,
		"output_tokens", um.CandidatesTokenCount,
		"total_tokens", um.TotalTokenCount,
	)
	c.pendingInputTokens.Add(int64(um.PromptTokenCount))
	c.pendingOutputTokens.Add(int64(um.CandidatesTokenCount))
}

// DrainTokens returns the accumulated input and output token counts since the
// last drain, then resets both counters to zero. This is used by callers to feed
// real token counts into the metrics tracker.
func (c *Client) DrainTokens() (int, int) {
	if c == nil {
		return 0, 0
	}
	in := c.pendingInputTokens.Swap(0)
	out := c.pendingOutputTokens.Swap(0)
	return int(in), int(out)
}

// GenerateContentRaw sends a prompt to the active Gemini model and returns the
// raw text response along with the input and output token counts for that call.
// An optional config can specify tools (e.g. Google Search Grounding) or response
// format constraints. This method is used by the Facebook processor for ad
// normalization and deal analysis.
//
// Token counts are returned directly (not accumulated on the client) so that
// concurrent callers sharing the same Client get accurate per-call counts.
func (c *Client) GenerateContentRaw(ctx context.Context, prompt string, config *genai.GenerateContentConfig) (string, int, int, error) {
	if c == nil || len(c.clients) == 0 {
		return "", 0, 0, fmt.Errorf("AI client not initialized")
	}

	start := time.Now()
	c.mu.Lock()
	activeModel := c.checkDayRollover(ctx)
	c.mu.Unlock()

	var result string
	var inTokens, outTokens int

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		c.mu.Lock()
		client := c.activeClient()
		model := activeModel
		c.mu.Unlock()

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		resp, genErr := client.Models.GenerateContent(callCtx, model, genai.Text(prompt), config)
		if genErr != nil {
			c.mu.Lock()
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "generate_content_raw")
			c.mu.Unlock()
			if backoff > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
			return retErr
		}

		c.mu.Lock()
		c.resetConsecutiveErrors()
		loc := c.currentLocation
		c.mu.Unlock()

		if attempt > 0 {
			slog.Info("Gemini retry succeeded",
				"model", model,
				"location", loc,
				"context", "generate_content_raw",
				"attempt", attempt,
			)
		}

		c.logTokenUsage(resp, "generate_content_raw", model, loc)

		// Capture token counts for the caller (not via shared atomics).
		if resp != nil && resp.UsageMetadata != nil {
			inTokens = int(resp.UsageMetadata.PromptTokenCount)
			outTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			return fmt.Errorf("no response content from gemini")
		}

		var textParts strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				textParts.WriteString(part.Text)
			}
		}

		result = textParts.String()
		if result == "" {
			return fmt.Errorf("gemini returned empty text response")
		}

		return nil
	})

	if err == nil {
		c.mu.Lock()
		loc := c.currentLocation
		c.mu.Unlock()
		slog.Info("GenerateContentRaw completed", "model", activeModel, "location", loc, "duration_ms", time.Since(start).Milliseconds())
	}

	return result, inTokens, outTokens, err
}

// LogCurrentState emits an INFO log with the current model, region, and exhaustion state.
// Useful at the start of each processor run for visibility.
func (c *Client) LogCurrentState() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	slog.Info("gemini_state",
		"model", c.currentModel,
		"location", c.currentLocation,
		"all_exhausted", c.allExhausted,
		"locations", c.locations,
		"fallback_models", c.fallbackModels,
	)
}

// AllTiersExhausted returns true if all Gemini model tiers have been exhausted.
// Returns false if the cooldown period has elapsed (auto-recovery).
func (c *Client) AllTiersExhausted() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.allExhausted {
		return false
	}
	// Check cooldown: if enough time has passed, allow retry
	if !c.exhaustedAt.IsZero() && time.Since(c.exhaustedAt) >= exhaustionCooldown {
		return false
	}
	return true
}

// CleanTitles sends a batch of deal titles to Gemini for cleaning.
// Returns a map of request index -> clean title.
func (c *Client) CleanTitles(ctx context.Context, requests []models.TitleRequest) (map[int]string, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping title cleaning")
		return nil, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(requests) == 0 {
		return nil, nil
	}

	startTime := time.Now()

	c.mu.Lock()
	activeModel := c.checkDayRollover(ctx)
	c.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("Clean these deal titles. For each, create a concise title (5-15 words). ")
	sb.WriteString("Remove fluff (\"Lava Hot\", \"Price Error\", \"YMMV\", emojis), store names if redundant, ")
	sb.WriteString("and focus on the product and price/discount.\n\n")

	for _, r := range requests {
		sb.WriteString(fmt.Sprintf("%d. Title: \"%s\"", r.Index, r.Title))
		if r.Retailer != "" {
			sb.WriteString(fmt.Sprintf(" | Retailer: \"%s\"", r.Retailer))
		}
		if r.Price != "" {
			sb.WriteString(fmt.Sprintf(" | Price: \"%s\"", r.Price))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nRespond with a JSON array: [{\"index\": 0, \"clean_title\": \"...\"}, ...]")

	prompt := sb.String()

	slog.Info("Starting batch title cleaning",
		"count", len(requests),
		"model", activeModel,
		"location", c.currentLocation,
		"prompt_len", len(prompt),
	)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	results := make(map[int]string)

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		c.mu.Lock()
		client := c.activeClient()
		model := activeModel
		c.mu.Unlock()

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, genErr := client.Models.GenerateContent(callCtx, model, genai.Text(prompt), config)
		if genErr != nil {
			c.mu.Lock()
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "batch_title_cleaning")
			c.mu.Unlock()
			if backoff > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(backoff):
				}
			}
			return retErr
		}
		c.mu.Lock()
		c.resetConsecutiveErrors()
		loc := c.currentLocation
		c.mu.Unlock()

		if attempt > 0 {
			slog.Info("Gemini retry succeeded",
				"model", model,
				"location", loc,
				"context", "batch_title_cleaning",
				"attempt", attempt,
			)
		}

		c.logTokenUsage(resp, "batch_title_cleaning", model, loc)

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			slog.Warn("Gemini returned empty response during batch title cleaning, retrying",
				"model", activeModel, "attempt", attempt)
			return fmt.Errorf("no response content from gemini for batch title cleaning")
		}

		candidate := resp.Candidates[0]
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				rawResponse := part.Text
				jsonStr := stripCodeBlock(rawResponse)

				var extracted []CleanTitleResult
				if err := json.Unmarshal([]byte(jsonStr), &extracted); err == nil {
					for _, r := range extracted {
						if r.CleanTitle != "" {
							results[r.Index] = r.CleanTitle
						}
					}
					slog.Info("Batch title cleaning raw response",
						"count", len(extracted),
						"raw_response", rawResponse,
					)
					return nil
				}
			}
		}

		var rawParts strings.Builder
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				rawParts.WriteString(part.Text)
			}
		}
		raw := rawParts.String()
		if len(raw) > 500 {
			raw = raw[:500]
		}
		slog.Warn("Gemini returned unparseable response during batch title cleaning, retrying",
			"model", activeModel, "attempt", attempt,
			"finish_reason", candidate.FinishReason, "parts", len(candidate.Content.Parts),
			"raw_truncated", raw)
		return fmt.Errorf("no valid JSON response from gemini (finish_reason=%s, parts=%d)",
			candidate.FinishReason, len(candidate.Content.Parts))
	})

	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	loc := c.currentLocation
	c.mu.Unlock()
	slog.Info("Batch title cleaning complete",
		"titles_cleaned", len(results),
		"titles_requested", len(requests),
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(startTime).Milliseconds(),
	)

	return results, nil
}

// checkResponseBlocked inspects a Gemini response for safety blocks or content filters.
// Returns a human-readable reason if the response was blocked, or empty string if not blocked.
func checkResponseBlocked(resp *genai.GenerateContentResponse) string {
	if resp.PromptFeedback != nil && resp.PromptFeedback.BlockReason != "" {
		msg := string(resp.PromptFeedback.BlockReason)
		if resp.PromptFeedback.BlockReasonMessage != "" {
			msg += ": " + resp.PromptFeedback.BlockReasonMessage
		}
		return "prompt blocked — " + msg
	}
	if len(resp.Candidates) > 0 {
		c := resp.Candidates[0]
		switch c.FinishReason {
		case genai.FinishReasonSafety, genai.FinishReasonBlocklist,
			genai.FinishReasonProhibitedContent, genai.FinishReasonSPII:
			reason := string(c.FinishReason)
			for _, sr := range c.SafetyRatings {
				if sr.Blocked {
					reason += fmt.Sprintf(" [%s=%s]", sr.Category, sr.Probability)
				}
			}
			return "finish_reason=" + reason
		}
	}
	return ""
}

// GenerateContentWithModel runs a Gemini API text generation using a specific model override.
func (c *Client) GenerateContentWithModel(ctx context.Context, modelOverride, prompt string, config *genai.GenerateContentConfig) (string, int, int, error) {
	if c == nil || len(c.clients) == 0 {
		return "", 0, 0, fmt.Errorf("AI client not initialized")
	}

	start := time.Now()
	var result string
	var inTokens, outTokens int

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		c.mu.Lock()
		client := c.activeClient()
		c.mu.Unlock()

		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		resp, genErr := client.Models.GenerateContent(callCtx, modelOverride, genai.Text(prompt), config)
		if genErr != nil {
			slog.Warn("AI call with model override failed, retrying", "model", modelOverride, "error", genErr)
			return genErr
		}

		c.mu.Lock()
		c.resetConsecutiveErrors()
		loc := c.currentLocation
		c.mu.Unlock()

		c.logTokenUsage(resp, "generate_content_override", modelOverride, loc)

		if resp != nil && resp.UsageMetadata != nil {
			inTokens = int(resp.UsageMetadata.PromptTokenCount)
			outTokens = int(resp.UsageMetadata.CandidatesTokenCount)
		}

		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil || len(resp.Candidates[0].Content.Parts) == 0 {
			return fmt.Errorf("no response content from gemini")
		}

		var textParts strings.Builder
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.Text != "" {
				textParts.WriteString(part.Text)
			}
		}

		result = textParts.String()
		if result == "" {
			return fmt.Errorf("gemini returned empty text response")
		}

		return nil
	})

	if err == nil {
		c.mu.Lock()
		loc := c.currentLocation
		c.mu.Unlock()
		slog.Info("GenerateContentWithModel completed", "model", modelOverride, "location", loc, "duration_ms", time.Since(start).Milliseconds())
	}

	return result, inTokens, outTokens, err
}
