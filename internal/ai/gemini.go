package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
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
}

type AnalysisResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
}

func NewClient(ctx context.Context, projectID string, locations []string, apiKey string, fallbackModels []string, store QuotaStore) (*Client, error) {
	if apiKey == "" && projectID == "" {
		return nil, nil // Return nil client if no credentials provided
	}

	if len(fallbackModels) == 0 {
		return nil, fmt.Errorf("fallback models list is empty")
	}

	clients := make(map[string]*genai.Client)

	if apiKey != "" {
		// Gemini API backend: single client, no region concept
		cfg := &genai.ClientConfig{
			APIKey:  apiKey,
			Backend: genai.BackendGeminiAPI,
		}
		client, err := genai.NewClient(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to create gemini client: %w", err)
		}
		clients[""] = client
		locations = []string{""} // no region failover for API key mode
		slog.Info("Using Gemini API backend (API key)")
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
		c.updateFirestoreQuota(ctx)
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
	// A stale Firestore record from a previous deployment may reference
	// a model that no longer exists in the configured tiers.
	if !c.isKnownModel(c.currentModel) {
		slog.Warn("Loaded model not in fallback list, resetting to default", "stale_model", c.currentModel, "default", c.fallbackModels[0])
		c.currentModel = c.fallbackModels[0]
		c.updateFirestoreQuota(ctx)
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
	c.updateFirestoreQuota(ctx)
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

func (c *Client) updateFirestoreQuota(ctx context.Context) {
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
		slog.Error("Failed to update gemini quota status in firestore", "error", err)
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
				c.updateFirestoreQuota(ctx)
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
		c.updateFirestoreQuota(ctx)
		return nil
	}

	for i, m := range c.fallbackModels {
		if m == c.currentModel {
			if i+1 < len(c.fallbackModels) {
				c.currentModel = c.fallbackModels[i+1]
				slog.Info("Quota exhausted, upgrading model tier", "new_model", c.currentModel)
				c.updateFirestoreQuota(ctx)
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
	c.updateFirestoreQuota(ctx)
	slog.Warn("All Gemini model tiers and regions exhausted, will retry after cooldown",
		"cooldown", exhaustionCooldown,
	)
	return fmt.Errorf("all model tiers exhausted across all regions")
}

// handleRateLimitError handles 429/RESOURCE_EXHAUSTED errors by retrying on the
// same model for transient per-minute rate limits, only escalating the tier after
// multiple consecutive failures suggesting genuine daily quota exhaustion.
// Returns true if the caller should retry the request.
func (c *Client) handleRateLimitError(ctx context.Context) (shouldRetry bool, err error) {
	c.consecutive429s++

	if c.consecutive429s < 3 {
		slog.Info("Rate limited, waiting before retry on same model",
			"model", c.currentModel,
			"location", c.currentLocation,
			"consecutive_429s", c.consecutive429s,
		)
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(5 * time.Second):
		}
		return true, nil
	}

	// 3+ consecutive 429s — treat as genuine quota exhaustion, escalate tier
	c.consecutive429s = 0
	upgradeErr := c.upgradeModelTier(ctx)
	if upgradeErr != nil {
		return false, upgradeErr
	}
	return true, nil
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
// Returns the error for the retry framework (nil means success, retryable errors are returned as-is).
func (c *Client) handleGenerationError(ctx context.Context, genErr error, activeModel *string, attempt int, logContext string) error {
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
		shouldRetry, handleErr := c.handleRateLimitError(ctx)
		if !shouldRetry {
			return fmt.Errorf("all model tiers exhausted: %w", genErr)
		}
		if handleErr != nil {
			return handleErr
		}
		*activeModel = c.currentModel
		return genErr
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
		return genErr
	}

	// Permanent errors
	return fmt.Errorf("permanent gemini error: %w", genErr)
}

// stripCodeBlock removes markdown code fences (```json ... ``` or ``` ... ```)
// that LLMs sometimes wrap around JSON responses.
func stripCodeBlock(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

// AllTiersExhausted returns true if all Gemini model tiers have been exhausted.
// Returns false if the cooldown period has elapsed (auto-recovery).
func (c *Client) AllTiersExhausted() bool {
	if c == nil {
		return false
	}
	if !c.allExhausted {
		return false
	}
	// Check cooldown: if enough time has passed, allow retry
	if !c.exhaustedAt.IsZero() && time.Since(c.exhaustedAt) >= exhaustionCooldown {
		return false
	}
	return true
}

func (c *Client) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, bool, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping deal analysis")
		return "", false, false, nil
	}

	if ctx.Err() != nil {
		return "", false, false, ctx.Err()
	}

	startTime := time.Now()

	// Always ensure we are using the correct model for the current day
	activeModel := c.checkDayRollover(ctx)

	link := deal.ActualDealURL
	if link == "" {
		link = deal.PostURL // Fallback to thread URL if deal URL is not available
	}

	var optionalFields string
	if deal.OriginalPrice != "" {
		optionalFields += fmt.Sprintf("Original Price: \"%s\"\n", deal.OriginalPrice)
	}
	if deal.Savings != "" {
		optionalFields += fmt.Sprintf("Savings: \"%s\"\n", deal.Savings)
	}
	if deal.Category != "" {
		optionalFields += fmt.Sprintf("Category: \"%s\"\n", deal.Category)
	}

	likes, comments, views := deal.Stats()

	prompt := fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"
Deal Link: "%s"
Price: "%s"
%sRetailer: "%s"
Community Engagement: %d upvotes, %d comments, %d views

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is a "warm" deal (is_warm). A warm deal is a high-quality find that should appeal to a value-conscious shopper, not just a standard weekly sale. Be selective.
   Signals of a Warm deal:
   - The price is a significant discount (e.g., 25%%+ off for standard items, or a clear "All-Time Low" (ATL) for high-demand tech).
   - User comments are strongly positive (e.g., "Incredible price", "Best deal I've seen in months", "Glad I waited for this").
   - Community engagement is strong (high upvotes relative to views, many comments).
   - It's a highly desirable product with broad appeal.
   Standard sales, generic clearance items, and deals with lukewarm/indifferent comments should be False.
3. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

Respond with exactly this JSON format:
{"clean_title": "your clean title here", "is_warm": true/false, "is_lava_hot": true/false}

`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, optionalFields, deal.Retailer, likes, comments, views)

	slog.Info("Starting AI deal analysis",
		"deal_id", deal.FirestoreID,
		"deal_title", deal.Title,
		"has_description", deal.Description != "",
		"has_comments", deal.Comments != "",
		"has_summary", deal.Summary != "",
		"price", deal.Price,
		"retailer", deal.Retailer,
		"category", deal.Category,
		"likes", likes,
		"comments", comments,
		"views", views,
		"model", activeModel,
		"location", c.currentLocation,
		"prompt", prompt,
	)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var resp *genai.GenerateContentResponse
	var err error

	err = util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, err = c.activeClient().Models.GenerateContent(callCtx, activeModel, genai.Text(prompt), config)
		if err == nil {
			c.resetConsecutiveErrors()
			return nil
		}

		return c.handleGenerationError(ctx, err, &activeModel, attempt, "deal analysis")
	})

	if err != nil {
		return "", false, false, err
	}

	if len(resp.Candidates) == 0 {
		return "", false, false, fmt.Errorf("no response candidates from gemini")
	}

	candidate := resp.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return "", false, false, fmt.Errorf("no response content from gemini")
	}

	var result string
	var hot bool
	var warm bool
	var found bool

	// With ResponseMIMEType: "application/json", the model outputs a JSON string in the text part.
	for _, part := range candidate.Content.Parts {
		if part.Text != "" {
			rawResponse := part.Text
			jsonStr := stripCodeBlock(rawResponse)

			var extracted AnalysisResult
			if err := json.Unmarshal([]byte(jsonStr), &extracted); err == nil {
				result = extracted.CleanTitle
				warm = extracted.IsWarm
				hot = extracted.IsLavaHot
				found = true

				slog.Info("AI raw response",
					"deal_id", deal.FirestoreID,
					"raw_response", rawResponse,
				)
				break
			}
		}
	}

	if !found {
		return "", false, false, fmt.Errorf("no valid function call or text response from gemini")
	}

	duration := time.Since(startTime)

	slog.Info("AI deal analysis complete",
		"deal_id", deal.FirestoreID,
		"original_title", deal.Title,
		"clean_title", result,
		"is_warm", warm,
		"is_lava_hot", hot,
		"model", activeModel,
		"location", c.currentLocation,
		"duration_ms", duration.Milliseconds(),
	)

	return result, warm, hot, nil
}
