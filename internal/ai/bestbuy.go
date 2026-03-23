package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

// AnalyzeBestBuyProduct uses Gemini to analyze a Best Buy product
// and determine if it's a warm or hot deal.
func (c *Client) AnalyzeBestBuyProduct(ctx context.Context, product bestbuy.Product) (*bestbuy.AnalyzeResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping Best Buy product analysis")
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
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping Best Buy analysis")
	}

	finalPrice := product.SalePrice
	if finalPrice == 0 {
		finalPrice = product.RegularPrice
	}

	discountPct := 0.0
	if product.RegularPrice > 0 && product.SalePrice > 0 && product.SalePrice < product.RegularPrice {
		discountPct = (product.RegularPrice - product.SalePrice) / product.RegularPrice * 100
	}

	sourceContext := "Best Buy Marketplace (third-party seller)"
	if product.Source == "openbox" {
		sourceContext = "Geek Squad Certified Open Box (Best Buy official)"
	}

	prompt := fmt.Sprintf(`You are a Canadian tech deal expert analyzing a Best Buy Canada product.

Product: "%s"
Category: %s
Regular Price: $%.2f CAD
Sale Price: $%.2f CAD
Discount: %.0f%% off
Seller: %s
Source: %s

Task:
1. Clean up the title to be concise (5-15 words, product name and key specs only, no marketing fluff, no "Refurbished (Excellent)" prefix).
2. Write a one-line summary of why this is or isn't a good deal (max 100 chars).
3. Determine if this is a "warm" deal:
   - The price is significantly below typical Canadian retail for this product.
   - The product has broad appeal or is a desirable tech item.
   - 30%%+ discount on a quality product with general demand qualifies as warm.
   - For marketplace/open-box items, compare against new retail pricing.
4. Determine if this is "Lava Hot" — be strict. Only if the price is absurdly good (50%%+ off on a popular, in-demand product, or a clear pricing error).

Return JSON only: {"clean_title": "...", "is_warm": bool, "is_lava_hot": bool, "summary": "..."}
`, product.Name, product.CategoryName, product.RegularPrice, finalPrice, discountPct, product.SellerName, sourceContext)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var result *bestbuy.AnalyzeResult
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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "Best Buy analysis")
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

		logTokenUsage(resp, "bestbuy_analysis", model, loc)

		parsed, parseErr := parseBestBuyResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Best Buy analysis",
					"model", activeModel, "product", product.Name, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Best Buy analysis, retrying",
					"model", activeModel, "product", product.Name, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse Best Buy analysis response: %w", parseErr)
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
	slog.Info("Best Buy product analysis complete",
		"processor", "bestbuy",
		"sku", product.SKU,
		"name", product.Name,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"discount_pct", discountPct,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func parseBestBuyResponse(resp *genai.GenerateContentResponse) (*bestbuy.AnalyzeResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked Best Buy analysis response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var result bestbuy.AnalyzeResult
			if err := json.Unmarshal([]byte(jsonStr), &result); err == nil {
				return &result, nil
			}
		}
	}

	return nil, fmt.Errorf("no text response from gemini for Best Buy analysis (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
}
