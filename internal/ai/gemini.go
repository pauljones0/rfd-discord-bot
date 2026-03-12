package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/genai"
)

type Client struct {
	client  *genai.Client
	modelID string
}

type AnalysisResult struct {
	CleanTitle string `json:"clean_title"`
	IsWarm     bool   `json:"is_warm"`
	IsLavaHot  bool   `json:"is_lava_hot"`
}

func NewClient(ctx context.Context, apiKey, modelID string) (*Client, error) {
	if apiKey == "" {
		return nil, nil // Return nil client if no key provided
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}

	return &Client{client: client, modelID: modelID}, nil
}

func (c *Client) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, bool, error) {
	if c == nil || c.client == nil {
		return "", false, false, nil // Graceful degradation
	}

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
2. Determine if this is a "warm" deal. A warm deal should feel like a good deal, but something that you wouldn't FOMO or lose sleep over.
3. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

You MUST respond ONLY with a raw JSON object containing exactly three keys: "clean_title" (string), "is_warm" (boolean), and "is_lava_hot" (boolean). Do not include any other text, markdown formatting, or backticks.
`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, optionalFields, deal.Retailer)

	config := &genai.GenerateContentConfig{
		Temperature: genai.Ptr[float32](0.1),
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
		},
	}

	resp, err := c.client.Models.GenerateContent(ctx, c.modelID, genai.Text(prompt), config)
	if err != nil {
		return "", false, false, fmt.Errorf("gemini generation failed: %w", err)
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
			jsonStr := strings.TrimSpace(part.Text)
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
				break
			}
		}
	}

	if !found {
		return "", false, false, fmt.Errorf("no valid function call or text response from gemini")
	}

	// Log the input prompt and output response as a single message
	slog.Info("Completed Gemini AI Deal Analysis",
		"deal_id", deal.FirestoreID,
		"deal_title", deal.Title,
		"prompt", prompt,
		"response_title", result,
		"clean_title", result,
		"is_warm", warm,
		"is_lava_hot", hot,
		"price", deal.Price,
		"original_price", deal.OriginalPrice,
		"savings", deal.Savings,
		"retailer", deal.Retailer,
	)

	return result, warm, hot, nil
}
