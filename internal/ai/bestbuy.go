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

// ScreenBestBuyBatch performs tier-1 batch screening of Best Buy products.
// It asks Gemini to select the top ~30% most deal-worthy items from a batch.
func (c *Client) ScreenBestBuyBatch(ctx context.Context, products []bestbuy.Product) ([]bestbuy.BatchScreenResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping Best Buy batch screening")
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
		discountPct := 0.0
		if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
			discountPct = (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
		}
		finalPrice := p.SalePrice
		if finalPrice == 0 {
			finalPrice = p.RegularPrice
		}
		source := "Marketplace"
		if p.Source == "openbox" {
			source = "Open Box"
		}
		itemList.WriteString(fmt.Sprintf("%d. [SKU: %s] \"%s\" — Regular: $%.2f → Sale: $%.2f (%.0f%% off) — Seller: %s — Category: %s — %s",
			i+1, p.SKU, p.Name, p.RegularPrice, finalPrice, discountPct, p.SellerName, p.CategoryName, source))
		if p.BrandName != "" || p.ModelNumber != "" || p.PrimaryUPC != "" {
			itemList.WriteString(fmt.Sprintf(" — Brand/Model/UPC: %s / %s / %s",
				firstNonEmptyString(p.BrandName, "unknown"),
				firstNonEmptyString(p.ModelNumber, "unknown"),
				firstNonEmptyString(p.PrimaryUPC, "unknown")))
		}
		if p.ComparableSummary != "" {
			itemList.WriteString(" — " + p.ComparableSummary)
		}
		appendBestBuySoldCompEvidence(&itemList, p)
		itemList.WriteString("\n")
	}

	prompt := fmt.Sprintf(`You are a Canadian tech deal expert analyzing Best Buy Canada marketplace and open-box products.

Review these %d products and select the top %d items that are most likely to be genuinely good deals.
Focus on items where the price appears significantly below typical Canadian retail/market value.
For marketplace items, same-seller regular/list prices are weak evidence because marketplace sellers can inflate them.
When Best Buy comparable evidence is present, it already excludes the current seller. Treat that as stronger than the seller's own regular price.
When eBay sold evidence is present, treat it as historical resale evidence; use it to sanity-check value but do not hard-reject an otherwise compelling item solely because sold comps are thin.
Do not mark an item as a top deal from discount percentage alone. With comps, prefer items at least 20%% and $50 below the median comparable price. Lava-hot candidates should look 40%%+ and $100+ below comps.
For open-box items, consider the condition discount vs typical market value, but still prefer external/current-comparable evidence.
Ignore generic accessories, low-value items, and items where the discount is unremarkable.

Items:
%s
For each item, provide a clean title (5-15 words, product-focused, no marketing fluff, no "Refurbished" prefix).
Mark items that are NOT good deals with is_top_deal: false.

Return a JSON array with ALL items, marking the top deals:
[{"sku": "...", "clean_title": "...", "is_top_deal": true/false, "reasoning": "brief reason"}]
`, len(products), topN, itemList.String())

	slog.Debug("Best Buy batch screening prompt",
		"processor", "bestbuy",
		"batch_size", len(products),
		"prompt_length", len(prompt),
	)

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var results []bestbuy.BatchScreenResult
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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "Best Buy batch screening")
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

		c.logTokenUsage(resp, "bestbuy_batch_screening", model, loc)

		parsed, parseErr := parseBestBuyBatchResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Best Buy batch screening",
					"processor", "bestbuy",
					"model", activeModel, "attempt", attempt, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Best Buy batch screening, retrying",
					"processor", "bestbuy",
					"model", activeModel, "attempt", attempt, "error", parseErr)
				return parseErr
			}
			return fmt.Errorf("failed to parse Best Buy batch screening response: %w", parseErr)
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
	slog.Info("Best Buy batch screening complete",
		"processor", "bestbuy",
		"batch_size", len(products),
		"target_top", topN,
		"actual_top", topCount,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return results, nil
}

// AnalyzeBestBuyBatch uses Gemini to verify a batch of tier-1 Best Buy candidates
// in one call, reducing per-item AI overhead for seller inventory runs.
func (c *Client) AnalyzeBestBuyBatch(ctx context.Context, products []bestbuy.Product) ([]bestbuy.BatchAnalyzeResult, error) {
	if c == nil || len(c.clients) == 0 {
		slog.Warn("AI client not initialized, skipping Best Buy batch analysis")
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
		return nil, fmt.Errorf("all model tiers exhausted for the day, skipping Best Buy batch analysis")
	}

	var itemList strings.Builder
	for i, p := range products {
		discountPct := 0.0
		if p.RegularPrice > 0 && p.SalePrice > 0 && p.SalePrice < p.RegularPrice {
			discountPct = (p.RegularPrice - p.SalePrice) / p.RegularPrice * 100
		}
		finalPrice := p.SalePrice
		if finalPrice == 0 {
			finalPrice = p.RegularPrice
		}
		itemList.WriteString(fmt.Sprintf("%d. [SKU: %s] \"%s\" — Category: %s — Regular: $%.2f — Current: $%.2f — %.0f%% off — Seller: %s",
			i+1, p.SKU, p.Name, p.CategoryName, p.RegularPrice, finalPrice, discountPct, p.SellerName))
		if p.BrandName != "" || p.ModelNumber != "" || p.PrimaryUPC != "" {
			itemList.WriteString(fmt.Sprintf(" — Brand/Model/UPC: %s / %s / %s",
				firstNonEmptyString(p.BrandName, "unknown"),
				firstNonEmptyString(p.ModelNumber, "unknown"),
				firstNonEmptyString(p.PrimaryUPC, "unknown")))
		}
		if p.ComparableSummary != "" {
			itemList.WriteString(" — " + p.ComparableSummary)
		}
		appendBestBuySoldCompEvidence(&itemList, p)
		itemList.WriteString("\n")
	}

	prompt := fmt.Sprintf(`You are a Canadian tech deal expert verifying Best Buy Canada marketplace products.

For each item, determine whether the price is a genuinely good Canadian tech deal.
Use "warm" only when it is meaningfully below market or unusually attractive.
Use "lava hot" only for absurdly good prices, likely pricing errors, or 50%%+ off on broadly desirable products.
Treat same-seller regular/list prices as weak evidence. Best Buy comparable evidence, when present, excludes the current seller and should be weighted heavily.
When eBay sold evidence is present, treat it as historical resale evidence; it can support value, resale demand, or skepticism when sold medians are weak.
If comps exist, do not call an item warm unless it is roughly 20%%+ and $50+ below the median comparable. Do not call it lava hot unless it is roughly 40%%+ and $100+ below comps.
Return all items, including non-deals, with concise product-focused clean titles and one-line summaries.

Items:
%s
Return JSON only:
[{"sku":"...","clean_title":"...","is_warm":true/false,"is_lava_hot":true/false,"summary":"max 100 chars"}]
`, itemList.String())

	config := &genai.GenerateContentConfig{
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
	}

	var results []bestbuy.BatchAnalyzeResult
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
			retErr, backoff := c.handleGenerationError(ctx, genErr, &activeModel, attempt, "Best Buy batch analysis")
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
		c.logTokenUsage(resp, "bestbuy_batch_analysis", model, loc)

		parsed, parseErr := parseBestBuyAnalyzeBatchResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				return util.PermanentError(parseErr)
			}
			return parseErr
		}
		results = parsed
		return nil
	})
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	loc := c.currentLocation
	c.mu.Unlock()
	slog.Info("Best Buy batch analysis complete",
		"processor", "bestbuy",
		"batch_size", len(products),
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds())
	return results, nil
}

// AnalyzeBestBuyProduct uses Gemini to analyze a Best Buy product
// and determine if it's a warm or hot deal (tier-2 individual verification).
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
Best Buy comparable evidence: %s
eBay sold evidence: %s

Task:
1. Clean up the title to be concise (5-15 words, product name and key specs only, no marketing fluff, no "Refurbished (Excellent)" prefix).
2. Write a one-line summary of why this is or isn't a good deal (max 100 chars).
3. Determine if this is a "warm" deal:
   - The price is significantly below typical Canadian retail for this product.
   - The product has broad appeal or is a desirable tech item.
   - Do not trust the seller's own regular/list price by itself.
   - Best Buy comparable evidence, when present, excludes this seller and is the strongest signal.
   - eBay sold evidence, when present, is historical resale evidence and should be considered separately from active Best Buy comps.
   - With comps, warm usually requires 20%%+ and $50+ below median comparable pricing.
   - For marketplace/open-box items, compare against new retail pricing and active comparable listings.
4. Determine if this is "Lava Hot" — be strict. Only if the price is absurdly good (50%%+ off on a popular, in-demand product, or a clear pricing error).
   - With comps, lava hot usually requires 40%%+ and $100+ below comparable pricing.

Return JSON only: {"clean_title": "...", "is_warm": bool, "is_lava_hot": bool, "summary": "..."}
`, product.Name, product.CategoryName, product.RegularPrice, finalPrice, discountPct, product.SellerName, sourceContext, firstNonEmptyString(product.ComparableSummary, "No active comparable summary available."), bestBuySoldCompPrompt(product))

	slog.Debug("Best Buy tier-2 analysis prompt",
		"processor", "bestbuy",
		"sku", product.SKU,
		"name", product.Name,
		"prompt_length", len(prompt),
	)

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

		c.logTokenUsage(resp, "bestbuy_analysis", model, loc)

		// Log raw response for debugging
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				if part.Text != "" {
					slog.Debug("Best Buy AI raw response",
						"processor", "bestbuy",
						"sku", product.SKU,
						"response", part.Text,
					)
				}
			}
		}

		parsed, parseErr := parseBestBuyResponse(resp)
		if parseErr != nil {
			if strings.Contains(parseErr.Error(), "gemini blocked") {
				slog.Warn("Gemini blocked Best Buy analysis",
					"processor", "bestbuy",
					"model", activeModel, "product", product.Name, "error", parseErr)
				return util.PermanentError(parseErr)
			}
			if strings.Contains(parseErr.Error(), "no text response") || strings.Contains(parseErr.Error(), "no response candidates") {
				slog.Warn("Gemini returned empty response during Best Buy analysis, retrying",
					"processor", "bestbuy",
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
	slog.Info("Best Buy tier-2 analysis complete",
		"processor", "bestbuy",
		"sku", product.SKU,
		"name", product.Name,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"summary", result.Summary,
		"discount_pct", discountPct,
		"model", activeModel,
		"location", loc,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	return result, nil
}

func parseBestBuyBatchResponse(resp *genai.GenerateContentResponse) ([]bestbuy.BatchScreenResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked batch screening response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var results []bestbuy.BatchScreenResult
			if err := json.Unmarshal([]byte(jsonStr), &results); err == nil {
				return results, nil
			}
		}
	}
	return nil, fmt.Errorf("no text response from gemini for batch screening (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
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

func parseBestBuyAnalyzeBatchResponse(resp *genai.GenerateContentResponse) ([]bestbuy.BatchAnalyzeResult, error) {
	if reason := checkResponseBlocked(resp); reason != "" {
		return nil, fmt.Errorf("gemini blocked Best Buy batch analysis response: %s", reason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}
	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text == "" {
			continue
		}
		jsonStr := stripCodeBlock(part.Text)
		var results []bestbuy.BatchAnalyzeResult
		if err := json.Unmarshal([]byte(jsonStr), &results); err == nil {
			return results, nil
		}
	}
	return nil, fmt.Errorf("no text response from gemini for Best Buy batch analysis (finish_reason=%s, parts=%d)",
		resp.Candidates[0].FinishReason, len(resp.Candidates[0].Content.Parts))
}

func appendBestBuySoldCompEvidence(builder *strings.Builder, product bestbuy.Product) {
	if product.SoldCompSummary == "" {
		return
	}
	builder.WriteString(" — eBay sold evidence: " + product.SoldCompSummary)
	if examples := bestBuySoldCompExamples(product); examples != "" {
		builder.WriteString(" Examples: " + examples)
	}
}

func bestBuySoldCompPrompt(product bestbuy.Product) string {
	if product.SoldCompSummary == "" {
		return "No eBay sold summary available."
	}
	if examples := bestBuySoldCompExamples(product); examples != "" {
		return product.SoldCompSummary + " Examples: " + examples
	}
	return product.SoldCompSummary
}

func bestBuySoldCompExamples(product bestbuy.Product) string {
	if len(product.SoldCompExamples) == 0 {
		return ""
	}
	parts := make([]string, 0, len(product.SoldCompExamples))
	for i, example := range product.SoldCompExamples {
		if i >= 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%q $%.2f", example.Title, example.Price))
	}
	return strings.Join(parts, "; ")
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
