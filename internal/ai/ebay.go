package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

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

	activeModel := c.checkDayRollover(ctx)

	if c.AllTiersExhausted() {
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

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, genErr := c.activeClient().Models.GenerateContent(callCtx, activeModel, genai.Text(prompt), config)
		if genErr != nil {
			return c.handleGenerationError(ctx, genErr, &activeModel, attempt, "eBay batch screening")
		}
		c.resetConsecutiveErrors()

		parsed, parseErr := parseEbayBatchResponse(resp)
		if parseErr != nil {
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

	slog.Info("eBay batch screening complete",
		"batch_size", len(items),
		"target_top", topN,
		"actual_top", topCount,
		"model", activeModel,
		"location", c.currentLocation,
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

	activeModel := c.checkDayRollover(ctx)

	if c.AllTiersExhausted() {
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

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		resp, genErr := c.activeClient().Models.GenerateContent(callCtx, activeModel, genai.Text(prompt), config)
		if genErr != nil {
			return c.handleGenerationError(ctx, genErr, &activeModel, attempt, "eBay deal verification")
		}
		c.resetConsecutiveErrors()

		parsed, parseErr := parseEbayVerifyResponse(resp)
		if parseErr != nil {
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
	slog.Info("eBay deal verification complete",
		"item_id", itemID,
		"item_title", item.Title,
		"clean_title", result.CleanTitle,
		"is_warm", result.IsWarm,
		"is_lava_hot", result.IsLavaHot,
		"price", price,
		"model", activeModel,
		"location", c.currentLocation,
	)

	return result, nil
}

func parseEbayBatchResponse(resp *genai.GenerateContentResponse) ([]ebay.EbayBatchScreenResult, error) {
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var results []ebay.EbayBatchScreenResult
			if err := json.Unmarshal([]byte(jsonStr), &results); err != nil {
				return nil, fmt.Errorf("failed to unmarshal batch results: %w (raw: %s)", err, jsonStr)
			}
			return results, nil
		}
	}
	return nil, fmt.Errorf("no text response from gemini for batch screening")
}

func parseEbayVerifyResponse(resp *genai.GenerateContentResponse) (*ebay.EbayVerifyResult, error) {
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := stripCodeBlock(part.Text)

			var result ebay.EbayVerifyResult
			if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
				return nil, fmt.Errorf("failed to unmarshal verify result: %w (raw: %s)", err, jsonStr)
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("no text response from gemini for deal verification")
}
