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

// ScreenMemExpressBatch performs tier-1 batch screening of Memory Express clearance items.
// It asks Gemini to select the top ~30% most deal-worthy items from a batch.
func (c *Client) ScreenMemExpressBatch(ctx context.Context, products []memoryexpress.Product) ([]memoryexpress.BatchScreenResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping Memory Express batch screening")
		return nil, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if len(products) == 0 {
		return nil, nil
	}

	c.mu.Lock()
	activeModel := c.checkDayRollover(ctx)
	exhausted := c.allExhausted && (c.exhaustedAt.IsZero() || time.Since(c.exhaustedAt) < exhaustionCooldown)
	c.mu.Unlock()

	if exhausted {
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping batch screening")
	}

	topN := len(products) * 30 / 100
	if topN < 1 {
		topN = 1
	}

	var itemList strings.Builder
	for i, p := range products {
		finalPrice := p.SalePrice
		if finalPrice == 0 {
			finalPrice = p.ClearancePrice
		}
		itemList.WriteString(fmt.Sprintf("%d. [SKU: %s] \"%s\" — Regular: $%.2f → Sale: $%.2f (%.0f%% off) — Category: %s — Store: %s\n",
			i+1, p.SKU, p.Title, p.RegularPrice, finalPrice, p.DiscountPct, p.Category, p.StoreName))
	}

	prompt := fmt.Sprintf(`You are a Canadian tech deal expert analyzing Memory Express clearance items.

Review these %d clearance items and select the top %d items that are most likely to be genuinely good deals.
Focus on items where the clearance price is significantly below typical retail/market value.
Ignore generic accessories, low-value items, and items where the discount is unremarkable.

Items:
%s
For each item, provide a clean title (5-15 words, product-focused, no marketing fluff).
Mark items that are NOT good deals with is_top_deal: false.

Return a JSON array with ALL items, marking the top deals:
[{"sku": "...", "clean_title": "...", "is_top_deal": true/false, "reasoning": "brief reason"}]
`, len(products), topN, itemList.String())

	slog.Debug("Memory Express batch screening prompt",
		"processor", "memoryexpress",
		"batch_size", len(products),
		"prompt_length", len(prompt),
	)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var results []memoryexpress.BatchScreenResult
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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "Memory Express batch screening")
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

		logTokenUsage(resp, "memexpress_batch_screening", model, loc)

		parsed, parseErr := parseMemExpressBatchResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Memory Express batch screening",
					"processor", "memoryexpress",
					"model", activeModel, "attempt", attempt, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Memory Express batch screening, retrying",
					"processor", "memoryexpress",
					"model", activeModel, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse Memory Express batch screening response: %w", parseErr)
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
	slog.Info("Memory Express batch screening complete",
		"processor", "memoryexpress",
		"batch_size", len(products),
		"target_top", topN,
		"actual_top", topCount,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return results, nil
}

// AnalyzeMemExpressProduct uses Gemini to analyze a Memory Express clearance product
// and determine if it's a warm or hot deal (tier-2 individual verification).
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

	slog.Debug("Memory Express tier-2 analysis prompt",
		"processor", "memoryexpress",
		"sku", product.SKU,
		"title", product.Title,
		"prompt_length", len(prompt),
	)

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

		// Log raw response for debugging
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				if part.Text != "" {
					slog.Debug("Memory Express AI raw response",
						"processor", "memoryexpress",
						"sku", product.SKU,
						"response", part.Text,
					)
				}
			}
		}

		parsed, parseErr := parseMemExpressResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Memory Express analysis",
					"processor", "memoryexpress",
					"model", activeModel, "product", product.Title, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Memory Express analysis, retrying",
					"processor", "memoryexpress",
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
	slog.Info("Memory Express tier-2 analysis complete",
		"processor", "memoryexpress",
		"sku", product.SKU,
		"title", product.Title,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"summary", result.Summary,
		"discount_pct", product.DiscountPct,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func parseMemExpressBatchResponse(resp *genai.GenerateContentResponse) ([]memoryexpress.BatchScreenResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked batch screening response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var results []memoryexpress.BatchScreenResult
			if err := json.Unmarshal([]byte(jsonStr), &results); err == nil {
				return results, nil
			}
		}
	}
	return nil, fmt.Errorf("no text response from gemini for batch screening (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
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
