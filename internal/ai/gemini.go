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

func (c *Client) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, error) {
	if c == nil || c.client == nil {
		return "", false, nil // Graceful degradation
	}

	link := deal.ActualDealURL
	if link == "" {
		link = deal.PostURL // Fallback to thread URL if deal URL is not available
	}

	prompt := fmt.Sprintf(`
Analyze this deal:
Title: "%s"
Description: "%s"
User Comments Summary: "%s"
RFD Summary: "%s"
Deal Link: "%s"
Price: "%s"
Retailer: "%s"

Task:
1. Create a clean, concise title (5-15 words). Remove fluff ("Lava Hot", "Price Error"), store names if redundant, and focus on the product and price/discount.
2. Determine if this is "Lava Hot". Be extremely strict: only flag as True if you would genuinely FOMO or lose sleep over missing this deal. Regular sales should be False.

You MUST respond by calling the "submit_analysis" function with your final decision.
`, deal.Title, deal.Description, deal.Comments, deal.Summary, link, deal.Price, deal.Retailer)

	config := &genai.GenerateContentConfig{
		Temperature: genai.Ptr[float32](0.1),
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
			{
				FunctionDeclarations: []*genai.FunctionDeclaration{
					{
						Name:        "submit_analysis",
						Description: "Submit the final determination of the deal title and whether it is lava hot.",
						Parameters: &genai.Schema{
							Type: genai.TypeObject,
							Properties: map[string]*genai.Schema{
								"clean_title": {
									Type:        genai.TypeString,
									Description: "A concise 5-20 word summary of the product/deal. Remove \"Lava Hot\", price errors, store names (unless critical), and fluff.",
								},
								"is_lava_hot": {
									Type:        genai.TypeBoolean,
									Description: "A boolean indicating extreme urgency. True ONLY if the deal is an absolute must-buy that would cause FOMO or lost sleep if missed. False for regular good deals.",
								},
							},
							Required: []string{"clean_title", "is_lava_hot"},
						},
					},
				},
			},
		},
	}

	resp, err := c.client.Models.GenerateContent(ctx, c.modelID, genai.Text(prompt), config)
	if err != nil {
		return "", false, fmt.Errorf("gemini generation failed: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return "", false, fmt.Errorf("no response candidates from gemini")
	}

	candidate := resp.Candidates[0]
	if candidate.Content == nil || len(candidate.Content.Parts) == 0 {
		return "", false, fmt.Errorf("no response content from gemini")
	}

	var result string
	var hot bool
	var found bool

	for _, part := range candidate.Content.Parts {
		if part.FunctionCall != nil && part.FunctionCall.Name == "submit_analysis" {
			args := part.FunctionCall.Args
			if titleVal, ok := args["clean_title"]; ok {
				if title, ok := titleVal.(string); ok {
					result = title
				}
			}
			if hotVal, ok := args["is_lava_hot"]; ok {
				if hotBool, ok := hotVal.(bool); ok {
					hot = hotBool
				}
			}
			found = true
			break
		}
	}

	if !found {
		// Fallback if the model writes JSON block as text instead of function calling.
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				jsonStr := strings.TrimSpace(part.Text)
				jsonStr = strings.TrimPrefix(jsonStr, "```json")
				jsonStr = strings.TrimPrefix(jsonStr, "```")
				jsonStr = strings.TrimSuffix(jsonStr, "```")

				var extracted AnalysisResult
				if err := json.Unmarshal([]byte(jsonStr), &extracted); err == nil {
					result = extracted.CleanTitle
					hot = extracted.IsLavaHot
					found = true
					break
				}
			}
		}
	}

	if !found {
		return "", false, fmt.Errorf("no valid function call or text response from gemini")
	}

	// Log the input prompt and output response as a single message
	slog.Info("Completed Gemini AI Deal Analysis",
		"deal_id", deal.FirestoreID,
		"deal_title", deal.Title,
		"prompt", prompt,
		"response_title", result,
		"clean_title", result,
		"is_lava_hot", hot,
		"price", deal.Price,
		"retailer", deal.Retailer,
	)

	return result, hot, nil
}
