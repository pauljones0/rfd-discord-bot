package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

// cleanTitleRe matches patterns like "**Clean Title:** ..." or "Clean Title: ..."
var cleanTitleRe = regexp.MustCompile(`(?i)\*{0,2}clean[_ ]title\*{0,2}:\s*(.+)`)

// ScreenEbayBatch performs tier-1 batch screening of eBay items.
// It asks Gemini to select the top ~30% most deal-worthy items from a batch.
// Returns the item IDs that passed screening along with their clean titles.
func (c *Client) ScreenEbayBatch(ctx context.Context, items []ebay.BrowseAPIItem) ([]ebay.EbayBatchScreenResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping eBay batch screening")
		return nil, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(items) == 0 {
		return nil, nil
	}

	c.mu.Lock()
	activeModel := c.checkDayRollover(ctx)
	exhausted := c.allExhausted && (c.exhaustedAt.IsZero() || time.Since(c.exhaustedAt) < exhaustionCooldown)
	c.mu.Unlock()

	if exhausted {
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping batch screening")
	}

	// Determine how many to select based on batch size
	topN := len(items) * 30 / 100
	if topN < 1 {
		topN = 1
	}

	// Build the item list for the prompt
	var itemList strings.Builder
	for i, item := range items {
		price := "Unknown"
		if item.Price != nil {
			price = fmt.Sprintf("%s %s", item.Price.Currency, item.Price.Value)
		}
		seller := "Unknown"
		if item.Seller != nil {
			seller = item.Seller.Username
		}

		itemID := ebay.ExtractItemID(item.ItemID)
		itemList.WriteString(fmt.Sprintf("%d. [ID: %s] \"%s\" — %s — Seller: %s — Condition: %s — %s\n",
			i+1, itemID, item.Title, price, seller, item.Condition, item.ItemWebURL))
	}

	prompt := fmt.Sprintf(`You are a Canadian deal-hunting expert analyzing eBay listings.

Review these %d eBay listings and select the top %d items that are most likely to be genuinely good deals.
Focus on items where the price appears significantly below typical retail/market value.
Ignore generic listings, overpriced items, and low-value accessories.

Items:
%s
For each item you select as a top deal, provide a clean title (5-15 words, product-focused, no fluff).
Mark items that are NOT good deals with is_top_deal: false.

Return a JSON array with ALL items, marking the top deals:
[{"item_id": "...", "clean_title": "...", "is_top_deal": true/false, "reasoning": "brief reason"}]
`, len(items), topN, itemList.String())

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var results []ebay.EbayBatchScreenResult
	start := time.Now()

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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "eBay batch screening")
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

		logTokenUsage(resp, "ebay_batch_screening", model, loc)

		parsed, parseErr := parseEbayBatchResponse(resp)
		if parseErr != nil {
			// Safety/content blocks are deterministic — don't retry
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked eBay batch screening",
					"model", activeModel, "attempt", attempt, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			// Empty/missing response from Gemini is transient — retry
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during eBay batch screening, retrying", "model", activeModel, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse eBay batch screening response: %w", parseErr)
		}
		results = parsed
		return nil
	})

	if err != nil {
		return nil, err
	}

	topCount := 0
	for _, r := range results {
		if r.IsTopDeal {
			topCount++
		}
	}

	c.mu.Lock()
	loc := c.currentLocation
	c.mu.Unlock()
	slog.Info("eBay batch screening complete",
		"batch_size", len(items),
		"target_top", topN,
		"actual_top", topCount,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return results, nil
}

// VerifyEbayDeal performs tier-2 individual verification with Google Search grounding.
// This is the second pass to confirm that a screened item is actually a good deal.
func (c *Client) VerifyEbayDeal(ctx context.Context, item ebay.BrowseAPIItem, screenTitle string) (*ebay.EbayVerifyResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping eBay deal verification")
		return nil, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	c.mu.Lock()
	activeModel := c.checkDayRollover(ctx)
	exhausted := c.allExhausted && (c.exhaustedAt.IsZero() || time.Since(c.exhaustedAt) < exhaustionCooldown)
	c.mu.Unlock()

	if exhausted {
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping deal verification")
	}

	price := "Unknown"
	if item.Price != nil {
		price = fmt.Sprintf("%s %s", item.Price.Currency, item.Price.Value)
	}
	seller := "Unknown"
	if item.Seller != nil {
		seller = item.Seller.Username
	}

	prompt := fmt.Sprintf(`Verify if this eBay listing is genuinely a good deal for a Canadian buyer.
Use Google Search to check the current retail/market price of this product.

Title: "%s"
Screened Title: "%s"
Price: %s
Seller: %s
Condition: %s
eBay URL: %s

Task:
1. Search for the current retail price of this exact product (or very similar) in Canada.
2. Clean up the title to be concise (5-15 words, product name and key specs only).
3. Determine if this is a "warm" deal:
   - The eBay price is significantly below current retail (25%%+ for standard items, any clear discount for high-demand tech).
   - The product is desirable with broad appeal.
   - Standard eBay pricing for used/refurbished items is NOT warm unless it's exceptionally low.
4. Determine if this is "Lava Hot" — be extremely strict. Only if you would genuinely lose sleep over missing this deal.

Return JSON: {"clean_title": "...", "is_warm": bool, "is_lava_hot": bool}
`, item.Title, screenTitle, price, seller, item.Condition, item.ItemWebURL)

	config := &genai.GenerateContentConfig{
		Temperature: genai.Ptr[float32](0.1),
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
	}

	var result *ebay.EbayVerifyResult
	start := time.Now()

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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "eBay deal verification")
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

		logTokenUsage(resp, "ebay_deal_verification", model, loc)

		parsed, parseErr := parseEbayVerifyResponse(resp)
		if parseErr != nil {
			// Safety/content blocks are deterministic — don't retry
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked eBay deal verification",
					"model", activeModel, "item", item.Title, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			// Empty/missing response from Gemini is transient — retry
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during eBay deal verification, retrying",
					"model", activeModel, "item", item.Title, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse eBay verification response: %w", parseErr)
		}
		result = parsed
		return nil
	})

	if err != nil {
		return nil, err
	}

	itemID := ebay.ExtractItemID(item.ItemID)
	c.mu.Lock()
	loc := c.currentLocation
	c.mu.Unlock()
	slog.Info("eBay deal verification complete",
		"item_id", itemID,
		"item_title", item.Title,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"price", price,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func parseEbayBatchResponse(resp *genai.GenerateContentResponse) ([]ebay.EbayBatchScreenResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked batch screening response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	// Try each text part individually — JSON may not be in the first one.
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var results []ebay.EbayBatchScreenResult
			if err := json.Unmarshal([]byte(jsonStr), &results); err == nil {
				return results, nil
			}
		}
	}
	return nil, fmt.Errorf("no text response from gemini for batch screening (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
}

func parseEbayVerifyResponse(resp *genai.GenerateContentResponse) (*ebay.EbayVerifyResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked verification response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	// Try each text part — with Google Search grounding, the JSON may not be
	// in the first text part (some parts may contain grounded explanations).
	var allText strings.Builder
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var result ebay.EbayVerifyResult
			if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
				return &result, nil
			}
			allText.WriteString(part.Text)
			allText.WriteString("\n")
		}
	}

	// Fallback: try extracting JSON from the concatenated text of all parts.
	if allText.Len() > 0 {
		jsonStr := stripCodeBlock(allText.String())
		var result ebay.EbayVerifyResult
		if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
			return &result, nil
		}

		// Last resort: try to parse structured text responses where Gemini
		// returns the fields in prose instead of JSON.
		if r, ok := parseVerifyFromText(allText.String()); ok {
			return r, nil
		}
	}

	return nil, fmt.Errorf("no text response from gemini for deal verification (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
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

// parseVerifyFromText attempts to extract verification fields from a
// free-text Gemini response (e.g. markdown bullet points) when JSON
// parsing fails. Returns nil, false if the text doesn't contain the
// expected fields.
func parseVerifyFromText(text string) (*ebay.EbayVerifyResult, bool) {
	lower := strings.ToLower(text)

	// Look for clean title in patterns like:
	//   **Clean Title:** Some Product Name
	//   Clean Title: Some Product Name
	titleMatch := cleanTitleRe.FindStringSubmatch(text)
	if titleMatch == nil {
		return nil, false
	}
	cleanTitle := strings.TrimSpace(titleMatch[1])
	// Remove leading/trailing markdown bold markers
	cleanTitle = strings.Trim(cleanTitle, "* ")
	cleanTitle = strings.TrimSpace(cleanTitle)
	if cleanTitle == "" {
		return nil, false
	}

	// Parse is_warm — look for "Is Warm: True/False" or "is_warm": true/false
	isWarm := strings.Contains(lower, "is warm:** true") ||
		strings.Contains(lower, "is warm: true") ||
		strings.Contains(lower, "is_warm: true") ||
		strings.Contains(lower, `"is_warm": true`)

	// Parse is_lava_hot
	isLavaHot := strings.Contains(lower, "is lava hot:** true") ||
		strings.Contains(lower, "is lava hot: true") ||
		strings.Contains(lower, "is_lava_hot: true") ||
		strings.Contains(lower, `"is_lava_hot": true`)

	slog.Info("Parsed eBay verification from free-text response",
		"clean_title", cleanTitle,
		"is_warm", isWarm,
		"is_lava_hot", isLavaHot,
	)

	return &ebay.EbayVerifyResult{
		CleanTitle: cleanTitle,
		IsWarm:     isWarm,
		IsLavaHot:  isLavaHot,
	}, true
}
