package facebook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// VMRResult holds the valuation data from VMR Canada.
type VMRResult struct {
	Wholesale       float64 // adjusted wholesale value
	Retail          float64 // adjusted retail value
	TrimName        string  // which trim was matched
	ProvinceAdj     float64 // province percentage applied (e.g. 5.0)
	ReliabilityRank float64 // make's reliability score (0-4)
	ReliabilityTier string  // "Tier 1", "Tier 2", "Tier 3"
}

// vmrTrim holds a single trim option parsed from the VMR page.
type vmrTrim struct {
	Name      string
	Wholesale float64
	Retail    float64
}

// provinceAdjustments maps Canadian province codes to VMR price adjustments (%).
// Baseline is Ontario South (0%). Source: vmrcanada.com/help/province_considerations.html
var provinceAdjustments = map[string]float64{
	"AB": 3, "BC": 6, "MB": 3, "NB": 4, "NL": 8,
	"NT": 10, "NS": 5, "NU": 10, "ON": 0, "PE": 8,
	"QC": 2, "SK": 5, "YT": 10,
}

// reliabilityScores maps vehicle makes to VMR Canada reliability survey scores (0-4).
// Source: vmrcanada.com/research/reliability-vmrcanada-results.html
var reliabilityScores = map[string]float64{
	"Lexus": 3.67, "Honda": 3.48, "Toyota": 3.47, "Lincoln": 3.42,
	"Mazda": 3.40, "Buick": 3.38, "Acura": 3.33, "Subaru": 3.30,
	"Infiniti": 3.29, "Kia": 3.17, "Nissan": 3.06, "Chevrolet": 3.04,
	"GMC": 3.02, "Ram": 2.99, "Dodge": 2.95, "Ford": 2.93,
	"Mitsubishi": 2.86, "Cadillac": 2.83, "Hyundai": 2.75,
	"Mercedes-Benz": 2.74, "Jeep": 2.74, "Audi": 2.73,
	"Chrysler": 2.68, "Volkswagen": 2.67,
}

// postalToProvince maps the first letter of a Canadian postal code to its province code.
var postalToProvince = map[byte]string{
	'A': "NL", 'B': "NS", 'C': "PE", 'E': "NB",
	'G': "QC", 'H': "QC", 'J': "QC",
	'K': "ON", 'L': "ON", 'M': "ON", 'N': "ON", 'P': "ON",
	'R': "MB", 'S': "SK", 'T': "AB", 'V': "BC",
	'X': "NT", // NT and NU both use X
	'Y': "YT",
}

// ProvinceFromPostal derives the province code from a Canadian postal code.
func ProvinceFromPostal(postal string) string {
	postal = strings.TrimSpace(strings.ToUpper(postal))
	if len(postal) == 0 {
		return "ON" // default to Ontario
	}
	if prov, ok := postalToProvince[postal[0]]; ok {
		return prov
	}
	return "ON"
}

// ReliabilityTier returns the tier classification for a reliability score.
func ReliabilityTier(score float64) string {
	if score >= 3.3 {
		return "Tier 1"
	}
	if score >= 2.9 {
		return "Tier 2"
	}
	return "Tier 3"
}

// commercialMakes lists vehicle makes that VMR Canada does not cover.
// These are commercial truck/equipment manufacturers — skip them to avoid
// wasting API calls and generating noisy "no trims found" errors.
var commercialMakes = map[string]bool{
	"hino": true, "international": true, "freightliner": true,
	"peterbilt": true, "kenworth": true, "mack": true,
	"western star": true, "isuzu": true, "fuso": true,
	"panterra": true, "autocar": true, "capacity": true,
}

// GetVMRValue fetches a vehicle valuation from VMR Canada via HTTP.
// No browser/Playwright needed — the data is embedded in static HTML.
func GetVMRValue(ctx context.Context, ai AIClient, year int, makeName, model, trim, postalCode string, odometer int) (*VMRResult, error) {
	// Guard: skip VMR for invalid years (Gemini sometimes extracts year=0 from "wanted" posts)
	if year <= 1950 || year > time.Now().Year()+2 {
		return nil, fmt.Errorf("VMR skipped: year %d is outside valid range (1950-%d)", year, time.Now().Year()+2)
	}

	// Guard: skip commercial truck makes that VMR doesn't cover
	if commercialMakes[strings.ToLower(strings.TrimSpace(makeName))] {
		return nil, fmt.Errorf("VMR skipped: commercial make %q not covered by VMR", makeName)
	}

	// Guard: VMR data starts around 1992 for most makes; older vehicles waste API calls
	if year < 1992 {
		return nil, fmt.Errorf("VMR skipped: year %d too old (VMR coverage starts ~1992)", year)
	}

	start := time.Now()
	province := ProvinceFromPostal(postalCode)

	slog.Info("VMR valuation starting",
		"processor", "facebook",
		"year", year, "make", makeName, "model", model,
		"trim", trim, "province", province, "odometer", odometer)

	// Remap make/model to VMR's naming conventions (e.g. Ram → Dodge)
	vmrMake, vmrModel := vmrNormalize(makeName, model, year)

	slug := vmrModelSlug(vmrMake, vmrModel)
	pageURL := fmt.Sprintf("https://www.vmrcanada.com/used-car/values/%d-%s-%s.html", year, vmrMakeSlug(vmrMake), slug)

	slog.Info("VMR fetching page",
		"processor", "facebook", "component", "vmr",
		"url", pageURL, "vmr_make", vmrMake, "vmr_model", vmrModel)

	body, err := fetchVMRPage(ctx, pageURL)
	if err != nil {
		// If 404, try Gemini to suggest the correct model slug
		if strings.Contains(err.Error(), "404") && ai != nil {
			corrected, aiErr := suggestVMRSlug(ctx, ai, year, vmrMake, vmrModel)
			if aiErr != nil {
				slog.Warn("VMR AI slug suggestion failed",
					"processor", "facebook", "component", "vmr",
					"year", year, "make", vmrMake, "model", vmrModel,
					"error", aiErr)
			} else if corrected == "" || corrected == slug {
				slog.Info("VMR AI slug suggestion unchanged",
					"processor", "facebook", "component", "vmr",
					"year", year, "make", vmrMake, "model", vmrModel,
					"original_slug", slug, "suggested_slug", corrected)
			} else {
				slog.Info("VMR URL corrected by AI",
					"processor", "facebook",
					"original_slug", slug, "corrected_slug", corrected)
				pageURL = fmt.Sprintf("https://www.vmrcanada.com/used-car/values/%d-%s-%s.html", year, vmrMakeSlug(vmrMake), corrected)
				body, err = fetchVMRPage(ctx, pageURL)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("failed to fetch VMR page: %w", err)
		}
	}

	trims, err := parseVMRTrims(body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse VMR page: %w", err)
	}

	// Fallback: VMR uses a static table (Trim/Fair/Clean/Exc) for older vehicles
	// instead of the <input name="submodel_prices"> radio buttons used for newer ones.
	if len(trims) == 0 {
		trims = parseVMRTable(body)
		if len(trims) > 0 {
			slog.Info("VMR parsed trims from table fallback",
				"processor", "facebook", "component", "vmr",
				"url", pageURL, "trim_count", len(trims))
		}
	}

	if len(trims) == 0 {
		// Diagnostic: detect what VMR returned so we can identify template changes
		bodySnippet := body
		if len(bodySnippet) > 500 {
			bodySnippet = bodySnippet[:500]
		}
		hasForm := strings.Contains(body, "submodel_prices")
		hasTable := strings.Contains(body, "<table")
		hasScript := strings.Contains(body, "document.pricing")
		slog.Warn("VMR page returned 0 trims from both parsers",
			"processor", "facebook", "component", "vmr",
			"url", pageURL,
			"body_length", len(body),
			"has_submodel_form", hasForm,
			"has_table", hasTable,
			"has_pricing_script", hasScript,
			"body_snippet", bodySnippet)
		return nil, fmt.Errorf("no trims found on VMR page for %d %s %s", year, makeName, model)
	}

	// Fuzzy-match the best trim
	matched := matchTrim(trims, trim)

	// Apply mileage adjustment
	vehicleAge := time.Now().Year() - year
	if vehicleAge < 1 {
		vehicleAge = 1
	}
	wsAdj, rtAdj := mileageAdjustment(matched.Wholesale, odometer, vehicleAge)
	adjusted := vmrTrim{
		Name:      matched.Name,
		Wholesale: matched.Wholesale + wsAdj,
		Retail:    matched.Retail + rtAdj,
	}

	// Apply province adjustment
	provAdj := provinceAdjustments[province]
	if provAdj > 0 {
		adjusted.Wholesale *= (1 + provAdj/100)
		adjusted.Retail *= (1 + provAdj/100)
	}

	// Round to nearest $25
	adjusted.Wholesale = math.Round(adjusted.Wholesale/25) * 25
	adjusted.Retail = math.Round(adjusted.Retail/25) * 25

	// Reliability data
	relScore := reliabilityScores[makeName]
	relTier := ReliabilityTier(relScore)

	result := &VMRResult{
		Wholesale:       adjusted.Wholesale,
		Retail:          adjusted.Retail,
		TrimName:        adjusted.Name,
		ProvinceAdj:     provAdj,
		ReliabilityRank: relScore,
		ReliabilityTier: relTier,
	}

	slog.Info("VMR valuation complete",
		"processor", "facebook",
		"year", year, "make", makeName, "model", model,
		"trim_matched", result.TrimName,
		"wholesale", result.Wholesale, "retail", result.Retail,
		"province_adj", provAdj, "reliability", relScore,
		"duration_ms", time.Since(start).Milliseconds())

	return result, nil
}

// vmrMakeSlug converts a make name to VMR's URL slug format.
// VMR uses lowercase with hyphens for multi-word makes (e.g. "land-rover").
// Ram trucks are listed under "dodge" on VMR — use vmrNormalize to remap first.
func vmrMakeSlug(makeName string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(makeName), " ", "-"))
}

// vmrModelSlug converts a model name to VMR's URL slug format.
// VMR uses URL-encoded spaces (%20) in multi-word model names, not hyphens.
// Examples: "Grand Vitara" → "grand%20vitara", "Crown Victoria" → "crown%20victoria"
// Hyphens in model names are preserved: "CR-V" → "cr-v", "F-150" → "f-150"
func vmrModelSlug(makeName, model string) string {
	slug := strings.ToLower(strings.TrimSpace(model))
	// Remove any characters that aren't alphanumeric, hyphens, or spaces
	var b strings.Builder
	for _, r := range slug {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == ' ' {
			b.WriteRune(r)
		}
	}
	// URL-encode spaces as %20 (VMR uses spaces, not hyphens, in model slugs)
	return strings.ReplaceAll(b.String(), " ", "%20")
}

// vmrNormalize remaps make/model to match VMR's naming conventions.
// VMR lists all Ram trucks under "Dodge" with the model format "1500 Ram".
// Year is needed for models that changed naming over time (e.g. WRX was Impreza WRX before 2015).
func vmrNormalize(makeName, model string, year int) (string, string) {
	lowerMake := strings.ToLower(strings.TrimSpace(makeName))
	lowerModel := strings.ToLower(strings.TrimSpace(model))

	// Ram as a standalone make: "Ram" + "1500" → "Dodge" + "1500 Ram"
	if lowerMake == "ram" {
		return "Dodge", model + " Ram"
	}

	// Dodge Ram models: "Dodge" + "Ram 1500" → "Dodge" + "1500 Ram"
	if lowerMake == "dodge" && strings.HasPrefix(lowerModel, "ram ") {
		suffix := strings.TrimSpace(model[4:]) // everything after "Ram "
		return "Dodge", suffix + " Ram"
	}

	// Dodge bare-number models: "Dodge" + "1500" → "Dodge" + "1500 Ram"
	// Gemini sometimes extracts just the number without "Ram" prefix.
	if lowerMake == "dodge" && (lowerModel == "1500" || lowerModel == "2500" || lowerModel == "3500") {
		return "Dodge", model + " Ram"
	}

	// Subaru WRX was sold as "Impreza WRX" until 2014; became standalone "WRX" in 2015.
	// VMR lists pre-2015 as "Impreza WRX" and 2015+ as "WRX".
	if lowerMake == "subaru" && lowerModel == "wrx" && year < 2015 {
		return "Subaru", "Impreza WRX"
	}
	// Same for STI variant
	if lowerMake == "subaru" && (lowerModel == "wrx sti" || lowerModel == "sti") && year < 2015 {
		return "Subaru", "Impreza WRX STI"
	}

	return makeName, model
}

// fetchVMRPage fetches the VMR valuation page HTML.
func fetchVMRPage(ctx context.Context, pageURL string) (string, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", err
	}
	// Basic headers — VMR has no bot protection
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-CA,en-US;q=0.9,en;q=0.8")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("VMR page fetch failed",
			"processor", "facebook", "component", "vmr",
			"url", pageURL, "error", err,
			"duration_ms", time.Since(start).Milliseconds())
		return "", err
	}
	defer resp.Body.Close()

	durationMs := time.Since(start).Milliseconds()

	if resp.StatusCode == 404 {
		slog.Info("VMR page returned 404",
			"processor", "facebook", "component", "vmr",
			"url", pageURL, "duration_ms", durationMs)
		return "", fmt.Errorf("VMR page not found (404): %s", pageURL)
	}
	if resp.StatusCode != 200 {
		slog.Warn("VMR page unexpected status",
			"processor", "facebook", "component", "vmr",
			"url", pageURL, "status", resp.StatusCode,
			"duration_ms", durationMs)
		return "", fmt.Errorf("VMR returned status %d for %s", resp.StatusCode, pageURL)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read VMR response: %w", err)
	}

	// VMR returns HTTP 200 for missing vehicles — a "Missing Page Report"
	// error page instead of a proper 404. Detect this soft-404 so the AI
	// slug correction path can fire (it checks for "404" in the error).
	body := string(bodyBytes)
	if strings.Contains(body, "Missing Page Report") {
		slog.Info("VMR page soft-404 detected",
			"processor", "facebook", "component", "vmr",
			"url", pageURL, "body_length", len(body),
			"duration_ms", durationMs)
		return "", fmt.Errorf("VMR page not found (404): %s (soft-404: Missing Page Report)", pageURL)
	}

	slog.Info("VMR page fetched successfully",
		"processor", "facebook", "component", "vmr",
		"url", pageURL, "body_length", len(body),
		"duration_ms", durationMs)

	return body, nil
}

// parseVMRTrims extracts trim options from VMR HTML.
// Each trim is a radio button: <input name="submodel_prices" value="|wholesale|retail">
// The label text contains the trim description.
func parseVMRTrims(body string) ([]vmrTrim, error) {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var trims []vmrTrim
	var findTrims func(*html.Node)
	findTrims = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "input" {
			var name, value string
			for _, a := range n.Attr {
				if a.Key == "name" {
					name = a.Val
				}
				if a.Key == "value" {
					value = a.Val
				}
			}
			if name == "submodel_prices" && strings.HasPrefix(value, "|") {
				parts := strings.Split(value, "|")
				if len(parts) >= 3 {
					ws, wsErr := strconv.ParseFloat(parts[1], 64)
					rt, rtErr := strconv.ParseFloat(parts[2], 64)
					if wsErr == nil && rtErr == nil {
						// Find the label text — it's usually the sibling text or parent label
						label := findTrimLabel(n)
						trims = append(trims, vmrTrim{
							Name:      label,
							Wholesale: ws,
							Retail:    rt,
						})
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findTrims(c)
		}
	}
	findTrims(doc)

	return trims, nil
}

// parseVMRTable extracts trim data from VMR's static table format.
// Older vehicles use a table with columns: Trim | Fair | Clean | Exc (or similar).
// We map Fair → Wholesale, Clean → Retail as the closest equivalents.
func parseVMRTable(body string) []vmrTrim {
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil
	}

	var trims []vmrTrim

	// Find all <tr> rows and check if they contain pricing data
	var walkRows func(*html.Node)
	walkRows = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			cells := collectCells(n)
			// We need at least 4 cells: Trim, Fair, Clean, Exc
			if len(cells) >= 4 {
				name := strings.TrimSpace(cells[0])
				// Skip header rows
				lowerName := strings.ToLower(name)
				if lowerName == "trim" || lowerName == "" || lowerName == "model" || lowerName == "submodel" {
					goto next
				}

				// Try to parse the numeric columns.
				// VMR table formats vary — try columns 1,2 (Fair, Clean) first
				fair := parseVMRPrice(cells[1])
				clean := parseVMRPrice(cells[2])

				if fair > 0 && clean > 0 {
					trims = append(trims, vmrTrim{
						Name:      name,
						Wholesale: fair,
						Retail:    clean,
					})
				}
			}
		}
	next:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walkRows(c)
		}
	}
	walkRows(doc)

	return trims
}

// collectCells extracts text content from each <td> or <th> child of a <tr>.
func collectCells(tr *html.Node) []string {
	var cells []string
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && (c.Data == "td" || c.Data == "th") {
			cells = append(cells, strings.TrimSpace(collectText(c)))
		}
	}
	return cells
}

// parseVMRPrice parses a price string that may contain commas, dollar signs, or spaces.
func parseVMRPrice(s string) float64 {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "$", "")
	s = strings.ReplaceAll(s, ",", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" || s == "-" || s == "N/A" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

// findTrimLabel extracts the text label for a radio button input.
// Walks up to the parent <label> or <td> and collects text content.
func findTrimLabel(n *html.Node) string {
	// Check if the input is inside a <label>
	parent := n.Parent
	for parent != nil {
		if parent.Type == html.ElementNode && (parent.Data == "label" || parent.Data == "td" || parent.Data == "tr") {
			text := collectText(parent)
			// Clean up — remove the radio button value artifacts
			text = strings.TrimSpace(text)
			if text != "" {
				return text
			}
		}
		parent = parent.Parent
	}
	// Fallback: check next sibling text
	if n.NextSibling != nil && n.NextSibling.Type == html.TextNode {
		return strings.TrimSpace(n.NextSibling.Data)
	}
	return "Unknown"
}

// collectText recursively collects all text from a node, skipping input elements.
func collectText(n *html.Node) string {
	if n.Type == html.ElementNode && n.Data == "input" {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(collectText(c))
	}
	return sb.String()
}

// matchTrim fuzzy-matches the AI-provided trim against available VMR trims.
func matchTrim(trims []vmrTrim, targetTrim string) vmrTrim {
	if len(trims) == 0 {
		return vmrTrim{}
	}
	target := strings.ToLower(strings.TrimSpace(targetTrim))

	// Exact match
	for _, t := range trims {
		if strings.ToLower(t.Name) == target {
			slog.Info("VMR trim matched",
				"processor", "facebook", "component", "vmr",
				"strategy", "exact", "target_trim", targetTrim, "matched_trim", t.Name)
			return t
		}
	}

	// Substring match
	for _, t := range trims {
		tLower := strings.ToLower(t.Name)
		if strings.Contains(tLower, target) || strings.Contains(target, tLower) {
			slog.Info("VMR trim matched",
				"processor", "facebook", "component", "vmr",
				"strategy", "substring", "target_trim", targetTrim, "matched_trim", t.Name)
			return t
		}
	}

	// Cleaned match (alphanumeric only)
	cleanTarget := cleanAlphaNum(target)
	for _, t := range trims {
		cleanName := cleanAlphaNum(strings.ToLower(t.Name))
		if strings.Contains(cleanName, cleanTarget) || strings.Contains(cleanTarget, cleanName) {
			slog.Info("VMR trim matched",
				"processor", "facebook", "component", "vmr",
				"strategy", "cleaned", "target_trim", targetTrim, "matched_trim", t.Name)
			return t
		}
	}

	// Default to cheapest trim (most conservative estimate)
	cheapest := trims[0]
	for _, t := range trims[1:] {
		if t.Wholesale < cheapest.Wholesale {
			cheapest = t
		}
	}
	slog.Info("VMR trim fallback to cheapest",
		"processor", "facebook", "component", "vmr",
		"target_trim", targetTrim, "cheapest_trim", cheapest.Name,
		"available_count", len(trims))
	return cheapest
}

func cleanAlphaNum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mileageAdjustment calculates the VMR mileage adjustment for wholesale and retail.
// Replicates VMR's client-side calcvalue() formula.
func mileageAdjustment(wholesale float64, actualKm int, vehicleAge int) (wsAdj, rtAdj float64) {
	normalKm := float64(20000 * vehicleAge)
	diff := normalKm - float64(actualKm)

	factor := 0.12 * ((wholesale / 28000) + 0.27)
	wsAdj = diff * factor
	rtAdj = wsAdj // same adjustment for both

	// Cap at 70% of wholesale (positive or negative)
	maxAdj := wholesale * 0.70
	if wsAdj > maxAdj {
		wsAdj = maxAdj
		rtAdj = maxAdj
	}
	if wsAdj < -maxAdj {
		wsAdj = -maxAdj
		rtAdj = -maxAdj
	}

	return wsAdj, rtAdj
}

// suggestVMRSlug uses Gemini to suggest the correct VMR model slug when the direct URL 404s.
func suggestVMRSlug(ctx context.Context, ai AIClient, year int, makeName, model string) (string, error) {
	prompt := fmt.Sprintf(`The VMR Canada website uses URL slugs for vehicle models.
URL pattern: vmrcanada.com/used-car/values/{year}-{make}-{model}.html
The model part uses URL-encoded spaces (%%20) for multi-word names, NOT hyphens.
Hyphens in model names are preserved (e.g. CR-V stays cr-v, F-150 stays f-150).
Examples: 2018-honda-civic.html, 2020-ford-f-150.html, 2008-suzuki-grand%%20vitara.html, 2017-dodge-1500%%20ram.html

What would be the correct model slug for: %d %s %s
Reply with ONLY the model slug (lowercase, %%20 for spaces, preserve hyphens). Nothing else.`, year, makeName, model)

	result, err := ai.GenerateContentRaw(ctx, prompt, nil)
	if err != nil {
		return "", err
	}
	slug := strings.TrimSpace(strings.ToLower(result))
	// Sanitize — only allow alphanumeric, hyphens, and %20
	slug = strings.ReplaceAll(slug, " ", "%20")
	var b strings.Builder
	for i := 0; i < len(slug); i++ {
		r := rune(slug[i])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		} else if i+2 < len(slug) && slug[i:i+3] == "%20" {
			b.WriteString("%20")
			i += 2 // skip the "20" part
		}
	}
	return b.String(), nil
}
