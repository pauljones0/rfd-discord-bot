package facebook

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

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

// sanitizeJSONEscapes fixes invalid escape sequences in JSON strings that Gemini
// sometimes produces (e.g. \$ instead of $). It replaces \X with X when X is not
// a valid JSON escape character (one of: " \ / b f n r t u).
func sanitizeJSONEscapes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inString := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		// Handle escape sequences inside strings first, so that \\
		// is consumed as a pair and the following " toggles inString correctly.
		if inString && ch == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
				// Valid JSON escape: write both characters and skip next
				b.WriteByte(ch)
				b.WriteByte(next)
				i++
			default:
				// Invalid escape like \$: drop the backslash, keep the char
			}
			continue
		}
		if ch == '"' {
			inString = !inString
		}
		b.WriteByte(ch)
	}
	return b.String()
}

// bareKVRe matches a JSON key-value pair start like `"key":`.
var bareKVRe = regexp.MustCompile(`"[^"]+"\s*:`)

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
		// No opening brace: Gemini sometimes returns bare key-value pairs
		// without outer {}. Detect "key": patterns and wrap them.
		if loc := bareKVRe.FindStringIndex(raw); loc != nil {
			inner := strings.TrimSpace(raw[loc[0]:])
			inner = strings.TrimRight(inner, " \t\n,")
			return "{" + inner + "}"
		}
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

	jsonStr := sanitizeJSONEscapes(extractJSON(rawText))
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

	// Calculate vehicle age for odometer plausibility check
	vehicleAge := time.Now().Year() - car.Year
	if vehicleAge < 1 {
		vehicleAge = 1
	}
	kmPerYear := car.Odometer / vehicleAge

	var odometerNote string
	if kmPerYear < 5000 && car.Odometer > 0 && vehicleAge > 2 {
		odometerNote = fmt.Sprintf("⚠ Odometer averages only %dk km/year (Canadian avg is 15-20k). Possible rollback.", kmPerYear/1000)
	}

	prompt := fmt.Sprintf(`Analyze this Facebook Marketplace vehicle listing as a savvy Canadian car buyer.

Vehicle: %d %s %s %s
Engine: %s | Transmission: %s | Drivetrain: %s | Body: %s
Odometer: %d km | Condition: %s
Asking Price: $%.0f
%s
%sDescription: %s

STEP 1 — Market Value:
Find the typical Canadian private-sale price for this year/make/model/trim at similar km using Google Search. If Carfax value is provided, use it as anchor.

STEP 2 — Reliability Tier (adjusts thresholds):
- Tier 1 (Toyota, Lexus, Honda, Acura, Mazda): proven reliable, lower threshold needed. These hold value and routinely reach 300-400k km.
- Tier 2 (Hyundai post-2019, Kia post-2019, Ford, Subaru, GM trucks, Volkswagen): mainstream, standard thresholds.
- Tier 3 (BMW, Mercedes, Audi, Land Rover, Jaguar, Maserati, Porsche): expensive parts (2-4x), high labor costs. Needs a bigger discount to offset ownership costs.

STEP 3 — Repair Cost Assessment:
Classify any issues mentioned in the listing:
- MINOR ($0-800): brakes, battery, tires, AC recharge, sensors, window motors, door locks, cosmetic damage. These are OPPORTUNITIES — most buyers avoid these listings, creating better deals for handy buyers.
- MAJOR ($1000+): engine, transmission, head gasket, suspension overhaul, rust repair. These erode the discount.
Calculate: net_discount = raw_discount - major_repair_costs. Minor repairs do NOT reduce the discount — they explain WHY the price is low.

STEP 4 — Red Flag Check (any = disqualify):
- Rebuilt/salvage title mentioned
- Price 50%%+ below market with no explanation (likely scam)
- "Selling as-is" + "no test drives" together
- Odometer implausibility: < 5,000 km/year average over vehicle age suggests rollback

STEP 5 — Determine Deal Tiers:
is_warm — must meet ALL:
  - Net discount ≥ 15%% (Tier 1), ≥ 20%% (Tier 2), ≥ 30%% (Tier 3)
  - No major red flags from Step 4
  - OR: any tier where minor-issue discount creates net saving > $2,000 after repair
  - Tier 1 cars ARE eligible above 200k km if the platform commonly reaches 400k+ (Corolla, Civic, Camry, CR-V, RAV4, etc.)
  - Tier 3 vehicles must have a large discount because parts/labor eat the savings
is_lava_hot — exceptional only:
  - Net discount ≥ 30%% (Tier 1), ≥ 35%% (Tier 2), problem-free
  - Tier 3 vehicles are never lava hot

STEP 6 — Title: 5-12 words. Year make model trim - XXk km.

STEP 7 — Summary (2 sentences max, mathematical):
- State market value, asking, and raw %% discount.
- If minor fix opportunity: mention the fix cost and net saving. Example: "Market ~14k, asking 9k (36%% below). Needs brakes (~$500) — easy fix, net saving ~4.5k."
- If major issue: show net discount after repair. Example: "Market ~12k, asking 7k (42%% below). Needs trans (~$3k), net discount ~17%%."
- Clean deal example: "Market ~18k, asking 12k (33%% below). Clean, Tier 1 reliability."

STEP 8 — Known Issues: widely-documented failure patterns for this year/make/model where repair > $1,000.
- Format: "Component: failure risk LOWER-UPPERk km ($X-Yk)"
- Skip if vehicle's %d km is 20%%+ past the upper failure range — it survived.
- Max 2, most expensive first. Return "" if none.

Respond with exactly this JSON:
{"is_warm": true/false, "is_lava_hot": true/false, "title": "string", "summary": "string", "known_issues": "string"}
`, car.Year, car.Make, car.Model, car.Trim,
		car.Engine, car.Transmission, car.Drivetrain, car.BodyStyle,
		car.Odometer, car.Condition,
		askingPrice,
		carfaxContext,
		odometerNote,
		car.Description,
		car.Odometer)

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

	jsonStr := sanitizeJSONEscapes(extractJSON(rawText))
	var analysis models.FacebookDealAnalysis
	if err := json.Unmarshal([]byte(jsonStr), &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse gemini analysis json: %v content: %s", err, jsonStr)
	}

	return &analysis, nil
}
