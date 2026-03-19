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

type QuotaStore interface {
	GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error)
	UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error
}

type Client struct {
	client         *genai.Client
	store          QuotaStore
	fallbackModels []string
	currentModel   string
	currentDay     string
}

type AnalysisResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
}

func NewClient(ctx context.Context, projectID, location string, fallbackModels []string, store QuotaStore) (*Client, error) {
	if projectID == "" {
		return nil, nil // Return nil client if no project provided
	}

	if len(fallbackModels) == 0 {
		return nil, fmt.Errorf("fallback models list is empty")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  projectID,
		Location: location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}

	c := &Client{
		client:         client,
		store:          store,
		fallbackModels: fallbackModels,
	}

	// Load initial state
	c.initQuotaState(ctx)

	return c, nil
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
	if c.store == nil || len(c.fallbackModels) == 0 {
		return c.fallbackModels[0]
	}

	today := getPacificDate()
	
	if c.currentDay == today && c.currentModel != "" {
		return c.currentModel
	}

	quota, err := c.store.GetGeminiQuotaStatus(ctx)
	if err != nil {
		slog.Warn("Failed to get Gemini quota status, using default model", "error", err)
		c.currentModel = c.fallbackModels[0]
		c.currentDay = today
		return c.currentModel
	}

	if quota == nil {
		c.currentModel = c.fallbackModels[0]
		c.currentDay = today
		c.updateFirestoreQuota(ctx)
		return c.currentModel
	}

	if quota.CurrentDay != today {
		c.currentDay = today
		c.currentModel = c.fallbackModels[0]
		slog.Info("Day rolled over or restarted, resetting to lowest tier model", "model", c.currentModel)
		c.updateFirestoreQuota(ctx)
	} else {
		c.currentDay = quota.CurrentDay
		c.currentModel = quota.CurrentModel
	}

	return c.currentModel
}

func (c *Client) updateFirestoreQuota(ctx context.Context) {
    if c.store == nil {
        return
    }
	err := c.store.UpdateGeminiQuotaStatus(ctx, models.GeminiQuotaStatus{
		CurrentDay:   c.currentDay,
		CurrentModel: c.currentModel,
	})
	if err != nil {
		slog.Error("Failed to update gemini quota status in firestore", "error", err)
	}
}

func (c *Client) upgradeModelTier(ctx context.Context) error {
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
	return fmt.Errorf("all model tiers exhausted for the day")
}

func (c *Client) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, bool, error) {
	if c == nil || c.client == nil {
		return "", false, false, nil // Graceful degradation
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

	prompt := fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"
Deal Link: "%s"
Price: "%s"
%sRetailer: "%s"

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is a "warm" deal (is_warm). A warm deal is a high-quality find that should appeal to a value-conscious shopper, not just a standard weekly sale. Be selective.
   Signals of a Warm deal:
   - The price is a significant discount (e.g., 25%%+ off for standard items, or a clear "All-Time Low" (ATL) for high-demand tech).
   - User comments are strongly positive (e.g., "Incredible price", "Best deal I've seen in months", "Glad I waited for this").
   - It's a highly desirable product with broad appeal.
   Standard sales, generic clearance items, and deals with lukewarm/indifferent comments should be False.
3. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, optionalFields, deal.Retailer)

	slog.Debug("Starting AI deal analysis",
		"deal_id", deal.FirestoreID,
		"deal_title", deal.Title,
		"model", activeModel,
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
		resp, err = c.client.Models.GenerateContent(callCtx, activeModel, genai.Text(prompt), config)
		if err == nil {
			return nil
		}

		errStr := err.Error()
		// Quota / Rate limit errors -> Upgrade tier and retry immediately (the loop in AnalyzeDeal handles the logic)
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "quota") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") ||
			strings.Contains(errStr, "404") || strings.Contains(errStr, "NOT_FOUND") {
			slog.Warn("AI model unavailable or quota exceeded, upgrading tier", "model", activeModel, "error", err)
			upgradeErr := c.upgradeModelTier(ctx)
			if upgradeErr != nil {
				return fmt.Errorf("gemini generation failed, all fallback quotas exhausted: %w", err)
			}
			activeModel = c.currentModel // Update activeModel for the next attempt within RetryWithBackoff
			return err                   // Return original error to trigger retry
		}

		// Transient network/service errors -> Retry with backoff
		if strings.Contains(errStr, "connection reset by peer") ||
			strings.Contains(errStr, "INTERNAL") ||
			strings.Contains(errStr, "Service Unavailable") ||
			strings.Contains(errStr, "503") ||
			strings.Contains(errStr, "504") ||
			strings.Contains(errStr, "deadline exceeded") {
			slog.Warn("Transient Gemini error, retrying", "model", activeModel, "attempt", attempt, "error", err)
			return err
		}

		// Permanent errors
		return fmt.Errorf("permanent gemini error: %w", err)
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
			jsonStr := strings.TrimSpace(rawResponse)
			// Sometimes the model might still wrap in markdown code blocks despite application/json
			jsonStr = strings.TrimPrefix(jsonStr, "```json")
			jsonStr = strings.TrimPrefix(jsonStr, "```")
			jsonStr = strings.TrimSuffix(jsonStr, "```")
			jsonStr = strings.TrimSpace(jsonStr)

			var extracted AnalysisResult
			if err := json.Unmarshal([]byte(jsonStr), &extracted); err == nil {
				result = extracted.CleanTitle
				warm = extracted.IsWarm
				hot = extracted.IsLavaHot
				found = true

				slog.Debug("AI raw response",
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
		"duration_ms", duration.Milliseconds(),
	)

	return result, warm, hot, nil
}
