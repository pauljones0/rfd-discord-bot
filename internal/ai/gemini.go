package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

type Client struct {
	model *genai.GenerativeModel
}

type AnalysisResult struct {
	CleanTitle string `json:"clean_title"`
	IsLavaHot  bool   `json:"is_lava_hot"`
}

func NewClient(ctx context.Context, apiKey, modelID string) (*Client, error) {
	if apiKey == "" {
		return nil, nil // Return nil client if no key provided
	}

	client, err := genai.NewClient(ctx, option.WithAPIKey(apiKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}

	model := client.GenerativeModel(modelID)
	model.SetTemperature(0.1) // Low temperature for deterministic output
	model.ResponseMIMEType = "application/json"

	return &Client{model: model}, nil
}

func (c *Client) AnalyzeDeal(ctx context.Context, title string) (string, bool, error) {
	if c == nil || c.model == nil {
		return "", false, nil // Graceful degradation
	}

	prompt := fmt.Sprintf(`
Analyze this deal title: "%s"

Output JSON with two fields:
1. "clean_title": A concise 5-20 word summary of the product/deal. Remove "Lava Hot", price errors, store names (unless critical), and fluff. 
2. "is_lava_hot": Boolean. True if the deal seems exceptionally good (high percentage off, price error, "lava hot" in title). False otherwise.

Example Input: "[Amazon.ca] LAVA HOT! Sony WH-1000XM5 Wireless Noise Cancelling Headphones - $298 (Reg. $498) - ATL!"
Example Output: {"clean_title": "Sony WH-1000XM5 Wireless Noise Cancelling Headphones", "is_lava_hot": true}
`, title)

	resp, err := c.model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return "", false, fmt.Errorf("gemini generation failed: %w", err)
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return "", false, fmt.Errorf("no response candidates from gemini")
	}

	var result AnalysisResult
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			// Clean up potential markdown formatting ```json ... ```
			jsonStr := strings.TrimPrefix(string(txt), "```json")
			jsonStr = strings.TrimPrefix(jsonStr, "```")
			jsonStr = strings.TrimSuffix(jsonStr, "```")

			if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
				return "", false, fmt.Errorf("failed to parse gemini response: %w", err)
			}
			return result.CleanTitle, result.IsLavaHot, nil
		}
	}

	return "", false, fmt.Errorf("no text part in response")
}
