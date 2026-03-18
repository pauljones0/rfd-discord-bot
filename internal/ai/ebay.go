package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"google.golang.org/genai"
)

// ScreenEbayBatch performs tier-1 batch screening of eBay items.
// It asks Gemini to select the top ~30% most deal-worthy items from a batch.
// Returns the item IDs that passed screening along with their clean titles.
func (c *Client) ScreenEbayBatch(ctx context.Context, items []ebay.BrowseAPIItem) ([]ebay.EbayBatchScreenResult, error) {
	if c == nil || c.client == nil {
		return nil, nil
	}

	if len(items) == 0 {
		return nil, nil
	}

	activeModel := c.checkDayRollover(ctx)

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

	var resp *genai.GenerateContentResponse
	var err error

	err = util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		resp, err = c.client.Models.GenerateContent(ctx, activeModel, genai.Text(prompt), config)
		if err == nil {
			return nil
		}

		errStr := err.Error()
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "quota") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") {
			slog.Warn("AI quota exceeded during eBay batch screening", "model", activeModel, "error", err)
			upgradeErr := c.upgradeModelTier(ctx)
			if upgradeErr != nil {
				return fmt.Errorf("all model tiers exhausted during eBay batch screening: %w", err)
			}
			activeModel = c.currentModel
			return err
		}

		if strings.Contains(errStr, "connection reset") || strings.Contains(errStr, "INTERNAL") ||
			strings.Contains(errStr, "503") || strings.Contains(errStr, "504") || strings.Contains(errStr, "deadline exceeded") {
			slog.Warn("Transient Gemini error during eBay batch screening", "model", activeModel, "attempt", attempt, "error", err)
			return err
		}

		return fmt.Errorf("permanent gemini error during eBay batch screening: %w", err)
	})

	if err != nil {
		return nil, err
	}

	results, err := parseEbayBatchResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse eBay batch screening response: %w", err)
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
	)

	return results, nil
}

// VerifyEbayDeal performs tier-2 individual verification with Google Search grounding.
// This is the second pass to confirm that a screened item is actually a good deal.
func (c *Client) VerifyEbayDeal(ctx context.Context, item ebay.BrowseAPIItem, screenTitle string) (*ebay.EbayVerifyResult, error) {
	if c == nil || c.client == nil {
		return nil, nil
	}

	activeModel := c.checkDayRollover(ctx)

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
		Temperature:      genai.Ptr[float32](0.1),
		ResponseMIMEType: "application/json",
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
	}

	var resp *genai.GenerateContentResponse
	var err error

	err = util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		resp, err = c.client.Models.GenerateContent(ctx, activeModel, genai.Text(prompt), config)
		if err == nil {
			return nil
		}

		errStr := err.Error()
		if strings.Contains(errStr, "429") || strings.Contains(errStr, "quota") || strings.Contains(errStr, "RESOURCE_EXHAUSTED") {
			slog.Warn("AI quota exceeded during eBay deal verification",
				"model", activeModel,
				"item", item.Title,
				"error", err,
			)
			upgradeErr := c.upgradeModelTier(ctx)
			if upgradeErr != nil {
				return fmt.Errorf("all model tiers exhausted during eBay deal verification: %w", err)
			}
			activeModel = c.currentModel
			return err
		}

		if strings.Contains(errStr, "connection reset") || strings.Contains(errStr, "INTERNAL") ||
			strings.Contains(errStr, "503") || strings.Contains(errStr, "504") || strings.Contains(errStr, "deadline exceeded") {
			slog.Warn("Transient Gemini error during eBay deal verification", "model", activeModel, "attempt", attempt, "error", err)
			return err
		}

		return fmt.Errorf("permanent gemini error during eBay deal verification: %w", err)
	})

	if err != nil {
		return nil, err
	}

	result, err := parseEbayVerifyResponse(resp)
	if err != nil {
		return nil, fmt.Errorf("failed to parse eBay verification response: %w", err)
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
		"prompt", prompt,
	)

	return result, nil
}

func parseEbayBatchResponse(resp *genai.GenerateContentResponse) ([]ebay.EbayBatchScreenResult, error) {
	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil, fmt.Errorf("no response candidates from gemini")
	}

	for _, part := range resp.Candidates[0].Content.Parts {
		if part.Text != "" {
			jsonStr := strings.TrimSpace(part.Text)
			jsonStr = strings.TrimPrefix(jsonStr, "```json")
			jsonStr = strings.TrimPrefix(jsonStr, "```")
			jsonStr = strings.TrimSuffix(jsonStr, "```")
			jsonStr = strings.TrimSpace(jsonStr)

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
			jsonStr := strings.TrimSpace(part.Text)
			jsonStr = strings.TrimPrefix(jsonStr, "```json")
			jsonStr = strings.TrimPrefix(jsonStr, "```")
			jsonStr = strings.TrimSuffix(jsonStr, "```")
			jsonStr = strings.TrimSpace(jsonStr)

			var result ebay.EbayVerifyResult
			if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
				return nil, fmt.Errorf("failed to unmarshal verify result: %w (raw: %s)", err, jsonStr)
			}
			return &result, nil
		}
	}
	return nil, fmt.Errorf("no text response from gemini for deal verification")
}
