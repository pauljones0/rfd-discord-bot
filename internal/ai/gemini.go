package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
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

	// Define the schema for Structured Outputs
	model.ResponseSchema = &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"clean_title": {
				Type:        genai.TypeString,
				Description: "A concise 5-20 word summary of the product/deal. Remove \"Lava Hot\", price errors, store names (unless critical), and fluff.",
			},
			"is_lava_hot": {
				Type:        genai.TypeBoolean,
				Description: "True if the deal seems exceptionally good (high percentage off, price error, \"lava hot\" in title). False otherwise.",
			},
		},
		Required: []string{"clean_title", "is_lava_hot"},
	}

	return &Client{model: model}, nil
}

func (c *Client) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, error) {
	if c == nil || c.model == nil {
		return "", false, nil // Graceful degradation
	}

	prompt := fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is "Lava Hot" (exceptionally good deal, price error, or highly praised by users).

Output JSON adhering to the schema.
`, deal.Title, deal.Description, deal.Comments, deal.Summary)

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
			// Clean up potential markdown formatting just in case
			jsonStr := strings.TrimSpace(string(txt))
			jsonStr = strings.TrimPrefix(jsonStr, "```json")
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
