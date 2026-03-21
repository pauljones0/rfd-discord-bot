package facebook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/genai"
)

// AIClient defines the interface for AI operations needed by the Facebook processor.
// The existing internal/ai Client can be adapted to satisfy this interface.
type AIClient interface {
	// GenerateContentRaw generates content using the AI model.
	// This is a lower-level method that the Facebook-specific functions wrap.
	GenerateContentRaw(ctx context.Context, prompt string, config *genai.GenerateContentConfig) (string, error)
}

// extractJSON finds and returns the first JSON object from a string that may
// contain markdown fences, preamble text, or trailing content.
func extractJSON(raw string) string {
	raw = strings.TrimSpace(raw)

	fenceRe := regexp.MustCompile("(?s)```(?:json)?\\s*\n?(.*?)\\s*```")
	if m := fenceRe.FindStringSubmatch(raw); len(m) > 1 {
		raw = strings.TrimSpace(m[1])
	}

	start := strings.Index(raw, "{")
	if start == -1 {
		return raw
	}

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inString {
			escaped = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}

	return raw[start:]
}

// NormalizeAd uses Gemini to extract structured vehicle data from an ad title and description.
func NormalizeAd(ctx context.Context, client AIClient, adTitle, adDescription string) (*models.CarData, error) {
	prompt := fmt.Sprintf(`Analyze the following Facebook Marketplace vehicle ad.
Title: %s
Description: %s

Extract the following information in JSON format:
{
  "year": int,
  "make": string,
  "model": string,
  "trim": string (if unknown, choose the most basic/cheapest variant),
  "engine": string (if unknown, choose the most basic/cheapest variant),
  "transmission": string (Automatic or Manual),
  "body_style": string,
  "drivetrain": string (FWD, AWD, RWD, 4WD),
  "odometer": int (mileage),
  "condition": string,
  "short_description": string (abbreviated for mobile, use symbols),
  "vehicle_type": string (one of: "car", "truck", "suv", "van", "motorcycle", "boat", "atv", "trailer", "other")
}
For unknown information, ALWAYS choose the cheapest possible variant from that year.
The vehicle_type field is critical: motorcycles, boats, ATVs, and trailers are NOT cars.
`, adTitle, adDescription)

	rawText, err := client.GenerateContentRaw(ctx, prompt, nil)
	if err != nil {
		return nil, err
	}

	if rawText == "" {
		return nil, fmt.Errorf("gemini returned no text content for ad: %s", adTitle)
	}

	jsonStr := extractJSON(rawText)
	var data models.CarData
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return nil, fmt.Errorf("failed to parse gemini json: %v content: %s", err, jsonStr)
	}

	return &data, nil
}

// PickCheapestTrim asks Gemini to select the cheapest/most basic trim from a list.
func PickCheapestTrim(ctx context.Context, client AIClient, year int, make, model string, options []string) string {
	if len(options) == 1 {
		return options[0]
	}

	prompt := fmt.Sprintf(`For a %d %s %s, which of these trims is the cheapest/most basic?
Options: %s

Reply with ONLY the exact option text, nothing else.`, year, make, model, strings.Join(options, ", "))

	answer, err := client.GenerateContentRaw(ctx, prompt, nil)
	if err != nil {
		slog.Warn("PickCheapestTrim failed, using first option", "error", err)
		return options[0]
	}

	answer = strings.TrimSpace(answer)
	answerLower := strings.ToLower(answer)
	for _, opt := range options {
		if strings.ToLower(opt) == answerLower {
			return opt
		}
	}
	for _, opt := range options {
		if strings.Contains(strings.ToLower(opt), answerLower) || strings.Contains(answerLower, strings.ToLower(opt)) {
			return opt
		}
	}

	return options[0]
}

// AnalyzeDeal uses Gemini with Google Search Grounding to assess if a deal is "FOMO".
func AnalyzeDeal(ctx context.Context, client AIClient, car *models.CarData, carfaxValue float64, askingPrice float64) (*models.FacebookDealAnalysis, error) {
	prompt := fmt.Sprintf(`A %d %s %s with %d km is listed for $%.2f.
Carfax private sale value is approximately $%.2f.
Description: %s

Using Google Search grounding, determine if this is a "FOMO" deal (significantly underpriced, highly desirable, or rare).
Return a JSON object with these fields:
{
  "fomo": boolean (true if this is a great deal),
  "title": string (a clean, concise title like "2019 Honda Civic LX - Great Deal"),
  "summary": string (a concise 2-3 sentence summary highlighting why this is or isn't a good deal, including key specs and value comparison)
}`, car.Year, car.Make, car.Model, car.Odometer, askingPrice, carfaxValue, car.Description)

	config := &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{GoogleSearchRetrieval: &genai.GoogleSearchRetrieval{}},
		},
	}

	rawText, err := client.GenerateContentRaw(ctx, prompt, config)
	if err != nil {
		return nil, err
	}

	if rawText == "" {
		return nil, fmt.Errorf("gemini returned no text content for deal analysis")
	}

	jsonStr := extractJSON(rawText)
	var analysis models.FacebookDealAnalysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse gemini analysis json: %v content: %s", err, jsonStr)
	}

	return &analysis, nil
}
