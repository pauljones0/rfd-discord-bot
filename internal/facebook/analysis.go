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

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

func discountLabel(pct float64) string {
	if pct > 0 {
		return "below Carfax"
	}
	return "above Carfax"
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

// AnalyzeDeal uses Gemini with Google Search Grounding to assess if a deal is worth posting.
func AnalyzeDeal(ctx context.Context, client AIClient, car *models.CarData, carfaxValue float64, askingPrice float64) (*models.FacebookDealAnalysis, error) {
	var carfaxContext string
	if carfaxValue > 0 {
		discount := (1 - askingPrice/carfaxValue) * 100
		carfaxContext = fmt.Sprintf("Carfax Canada private-sale value: $%.0f (asking is %.0f%% %s).",
			carfaxValue, abs(discount), discountLabel(discount))
	} else {
		carfaxContext = "Carfax valuation unavailable — you MUST use Google Search to find the typical market price."
	}

	prompt := fmt.Sprintf(`Analyze this Facebook Marketplace vehicle listing.

Vehicle: %d %s %s %s
Engine: %s | Transmission: %s | Drivetrain: %s | Body: %s
Odometer: %d km | Condition: %s
Asking Price: $%.0f
%s
Description: %s

Task:
Using Google Search grounding, research the current Canadian private-sale market price for this exact vehicle (year, make, model, similar mileage).

1. Create a clean title (5-15 words, e.g. "2019 Honda Civic LX - 80k km").
2. Determine if this is "warm" (is_warm). A warm deal means:
   - The asking price is at least 20%% below typical private-sale market value for this year/make/model/mileage
   - The vehicle is desirable with broad appeal (not niche, not salvage, not extremely high mileage)
   - If Carfax value is available and > 0, the asking price must be meaningfully below it
   - Standard marketplace pricing is NOT warm. Be selective — most listings are NOT deals.
3. Determine if this is "Lava Hot" (is_lava_hot). Be extremely strict:
   - The asking price is at least 35%% below market value
   - You would genuinely lose sleep over missing this deal
   - Almost nothing should be lava hot

Return JSON:
{"fomo": true/false, "is_warm": true/false, "is_lava_hot": true/false, "title": "...", "summary": "2-3 sentences comparing asking price to market value with key specs"}
`, car.Year, car.Make, car.Model, car.Trim,
		car.Engine, car.Transmission, car.Drivetrain, car.BodyStyle,
		car.Odometer, car.Condition,
		askingPrice,
		carfaxContext,
		car.Description)

	config := &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{GoogleSearch: &genai.GoogleSearch{}},
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
