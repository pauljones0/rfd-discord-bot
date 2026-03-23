package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

// AnalyzeMemExpressProduct uses Gemini to analyze a Memory Express clearance product
// and determine if it's a warm or hot deal.
func (c *Client) AnalyzeMemExpressProduct(ctx context.Context, product memoryexpress.Product) (*memoryexpress.AnalyzeResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping Memory Express product analysis")
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
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping Memory Express analysis")
	}

	finalPrice := product.SalePrice
	if finalPrice == 0 {
		finalPrice = product.ClearancePrice
	}

	prompt := fmt.Sprintf(`You are a Canadian tech deal expert analyzing a Memory Express clearance item.

Product: "%s"
Category: %s
Regular Price: $%.2f CAD
Clearance/Sale Price: $%.2f CAD
Discount: %.0f%% off
Store: %s (in-store pickup only)

Task:
1. Clean up the title to be concise (5-15 words, product name and key specs only, no marketing fluff).
2. Write a one-line summary of why this is or isn't a good deal (max 100 chars).
3. Determine if this is a "warm" deal:
   - The clearance price is significantly below typical retail for this product.
   - The product has broad appeal or is a desirable tech item.
   - 30%%+ discount on a quality product with general demand qualifies as warm.
4. Determine if this is "Lava Hot" — be strict. Only if the price is absurdly good (50%%+ off on a popular, in-demand product, or a clear pricing error).

Return JSON only: {"clean_title": "...", "is_warm": bool, "is_lava_hot": bool, "summary": "..."}
`, product.Title, product.Category, product.RegularPrice, finalPrice, product.DiscountPct, product.StoreName)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var result *memoryexpress.AnalyzeResult
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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "Memory Express analysis")
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

		logTokenUsage(resp, "memexpress_analysis", model, loc)

		parsed, parseErr := parseMemExpressResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Memory Express analysis",
					"model", activeModel, "product", product.Title, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Memory Express analysis, retrying",
					"model", activeModel, "product", product.Title, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse Memory Express analysis response: %w", parseErr)
		}
		result = parsed
		return nil
	})

	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	loc := c.currentLocation
	c.mu.Unlock()
	slog.Info("Memory Express product analysis complete",
		"processor", "memoryexpress",
		"sku", product.SKU,
		"title", product.Title,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"discount_pct", product.DiscountPct,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func parseMemExpressResponse(resp *genai.GenerateContentResponse) (*memoryexpress.AnalyzeResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked Memory Express analysis response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var result memoryexpress.AnalyzeResult
			if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
				return &result, nil
			}
		}
	}

	return nil, fmt.Errorf("no text response from gemini for Memory Express analysis (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
}
