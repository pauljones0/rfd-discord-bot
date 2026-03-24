package facebook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

const (
	// carfaxBaseURL is the API base path for the Carfax valuation form endpoints.
	// Discovered by inspecting the form's data-resource attribute on carfax.ca.
	carfaxBaseURL = "https://www.carfax.ca/content/carfax/ca/en/whats-my-car-worth/car-value/jcr:content/root/container/container/container/flex_container/1/valuation_modal_form"

	// carfaxResultsBaseURL is the public results page (no auth needed).
	carfaxResultsBaseURL = "https://www.carfax.ca/whats-my-car-worth/car-value/car-value-results"

	// maxTokenRetries is how many times to retry an API call with a fresh token
	// when reCAPTCHA rejects the request (400 status).
	maxTokenRetries = 3
)

// CarfaxHTTPClient makes direct HTTP calls to Carfax's API endpoints,
// using reCAPTCHA tokens from the remote token service. This replaces
// the Playwright UI automation approach entirely.
type CarfaxHTTPClient struct {
	tokens     *CarfaxTokenClient
	httpClient *http.Client
	store      Store
	userAgent  string
}

// CarfaxValuer is the interface for Carfax valuation clients, allowing
// swappable implementations (HTTP vs Playwright) and easier testing.
type CarfaxValuer interface {
	GetValue(ctx context.Context, year int, make, model, trim, engine, transmission, drivetrain, bodyStyle, postalCode string, odometer int, pickTrim TrimPicker) (float64, error)
}

// carfaxYMMResponse is the JSON response from the year-make-model cascade API.
type carfaxYMMResponse struct {
	Data []string `json:"data"`
}

// carfaxValuationResponse is the JSON response from the valuation-report POST.
type carfaxValuationResponse struct {
	ReportID string `json:"reportId"`
	Success  bool   `json:"success"`
}

// NewCarfaxHTTPClient creates a Carfax client that uses direct HTTP calls
// with tokens from the remote token service.
//
// proxyURL is the Evomi residential proxy URL (same format as used by BrowserManager).
// If empty, requests go directly without a proxy (not recommended — Carfax may
// geo-block non-Canadian IPs).
func NewCarfaxHTTPClient(tokens *CarfaxTokenClient, proxyURL string, store Store) *CarfaxHTTPClient {
	transport := &http.Transport{}

	if proxyURL != "" {
		proxyTransport, err := buildCarfaxHTTPProxy(proxyURL)
		if err != nil {
			slog.Warn("Failed to configure Carfax proxy, proceeding without",
				"processor", "facebook", "error", err)
		} else {
			transport = proxyTransport
		}
	}

	// Randomize UA from common real browsers
	uas := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
	}

	return &CarfaxHTTPClient{
		tokens: tokens,
		httpClient: &http.Client{
			Timeout:   20 * time.Second,
			Transport: transport,
		},
		store:     store,
		userAgent: uas[rand.Intn(len(uas))],
	}
}

// buildCarfaxHTTPProxy configures an http.Transport with the Evomi residential proxy.
// Uses the same password-parameter format as browser.go:buildCarfaxProxySettings —
// appends _country-CA_session-{rand}_lifetime-60 to the proxy password.
func buildCarfaxHTTPProxy(baseURL string) (*http.Transport, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}

	password, _ := parsed.User.Password()
	username := parsed.User.Username()
	password += "_country-CA_session-" + randomSessionID() + "_lifetime-60"

	proxyURL := &url.URL{
		Scheme: parsed.Scheme,
		User:   url.UserPassword(username, password),
		Host:   parsed.Host,
	}

	return &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
	}, nil
}

// GetValue fetches a Carfax valuation via direct HTTP API calls.
// This is a drop-in replacement for the old Playwright-based CarfaxClient.GetValue.
func (c *CarfaxHTTPClient) GetValue(ctx context.Context, year int, make, model, trim, engine, transmission, drivetrain, bodyStyle, postalCode string, odometer int, pickTrim TrimPicker) (float64, error) {
	start := time.Now()
	slog.Info("Carfax HTTP valuation starting",
		"processor", "facebook",
		"year", year, "make", make, "model", model, "trim", trim,
		"odometer", odometer, "postal", postalCode)

	// --- Cache check ---
	postalPrefix := strings.ToUpper(strings.TrimSpace(postalCode))
	if len(postalPrefix) > 3 {
		postalPrefix = postalPrefix[:3]
	}
	cached, err := c.store.GetCarfaxCache(ctx, year, make, model, trim, postalPrefix)
	if err != nil {
		slog.Warn("Carfax cache lookup failed", "processor", "facebook", "error", err)
	}
	if cached != nil {
		slog.Info("Carfax cache hit",
			"processor", "facebook",
			"year", year, "make", make, "model", model,
			"cached_value", cached.MidValue,
			"cache_age", time.Since(cached.CachedAt).Round(time.Hour))
		return cached.MidValue, nil
	}

	// --- Cascade: resolve exact Carfax-expected strings ---

	// Step 1: Get available makes for the year, fuzzy-match
	matchedMake, err := c.cascadeFuzzy(ctx, "Make", make, url.Values{"year": {fmt.Sprintf("%d", year)}})
	if err != nil {
		return 0, fmt.Errorf("carfax make cascade: %w", err)
	}

	// Step 2: Get available models for year+make, fuzzy-match
	matchedModel, err := c.cascadeFuzzy(ctx, "Model", model, url.Values{
		"year": {fmt.Sprintf("%d", year)},
		"make": {matchedMake},
	})
	if err != nil {
		return 0, fmt.Errorf("carfax model cascade: %w", err)
	}

	// Step 3: Get available trims (optional — let Gemini pick if callback provided)
	matchedTrim := trim
	trimParams := url.Values{
		"year":  {fmt.Sprintf("%d", year)},
		"make":  {matchedMake},
		"model": {matchedModel},
	}
	trimOptions, trimErr := c.cascadeOptions(ctx, "Trim", trimParams)
	if trimErr != nil {
		slog.Warn("Carfax trim cascade failed, using provided trim",
			"processor", "facebook", "error", trimErr)
	} else if len(trimOptions) > 0 {
		if pickTrim != nil {
			matchedTrim = pickTrim(ctx, year, matchedMake, matchedModel, trimOptions)
			slog.Info("Carfax trim selection",
				"processor", "facebook",
				"gemini_trim", trim, "selected_trim", matchedTrim,
				"available_trims", trimOptions)
		} else {
			matchedTrim = fuzzyMatch(trim, trimOptions)
		}
	}

	// Step 4: Cascade Engine, Drivetrain, Transmission, BodyStyle from the selected trim
	cascadeParams := url.Values{
		"year":  {fmt.Sprintf("%d", year)},
		"make":  {matchedMake},
		"model": {matchedModel},
		"trim":  {matchedTrim},
	}

	matchedEngine := c.cascadeOrDefault(ctx, "Engine", engine, cascadeParams)
	cascadeParams.Set("engine", matchedEngine)

	matchedDrivetrain := c.cascadeOrDefault(ctx, "Drivetrain", drivetrain, cascadeParams)
	cascadeParams.Set("drivetrain", matchedDrivetrain)

	matchedTransmission := c.cascadeOrDefault(ctx, "Transmission", transmission, cascadeParams)
	cascadeParams.Set("transmission", matchedTransmission)

	matchedBodyStyle := c.cascadeOrDefault(ctx, "BodyStyle", bodyStyle, cascadeParams)

	// --- Submit valuation ---
	reportID, err := c.submitValuation(ctx, year, matchedMake, matchedModel, matchedTrim,
		matchedEngine, matchedDrivetrain, matchedTransmission, matchedBodyStyle,
		postalCode, odometer)
	if err != nil {
		return 0, fmt.Errorf("carfax valuation submit: %w", err)
	}

	// --- Fetch results ---
	low, high, err := c.fetchResults(ctx, reportID)
	if err != nil {
		return 0, fmt.Errorf("carfax results fetch: %w", err)
	}

	// Sanity check: no passenger vehicle should value above $300k.
	// The regex can match unrelated numbers in the HTML page source,
	// producing absurd values (e.g. $11M for a 2007 Tiburon).
	const maxPlausibleValue = 300_000
	if low > maxPlausibleValue || high > maxPlausibleValue {
		slog.Error("Carfax returned implausible valuation, discarding",
			"processor", "facebook", "component", "carfax_http",
			"year", year, "make", make, "model", model,
			"low", low, "high", high,
			"report_id", reportID)
		return 0, fmt.Errorf("carfax valuation implausible: low=$%.0f high=$%.0f for %d %s %s", low, high, year, make, model)
	}

	midValue := (low + high) / 2

	// --- Cache the result ---
	cacheErr := c.store.SaveCarfaxCache(ctx, &storage.CarfaxCacheEntry{
		Year:         year,
		Make:         matchedMake,
		Model:        matchedModel,
		Trim:         matchedTrim,
		PostalPrefix: postalPrefix,
		LowValue:     low,
		HighValue:    high,
		MidValue:     midValue,
	})
	if cacheErr != nil {
		slog.Warn("Failed to cache Carfax result", "processor", "facebook", "error", cacheErr)
	}

	slog.Info("Carfax HTTP valuation succeeded",
		"processor", "facebook",
		"year", year, "make", matchedMake, "model", matchedModel, "trim", matchedTrim,
		"low", low, "high", high, "mid", midValue,
		"duration_ms", time.Since(start).Milliseconds())

	return midValue, nil
}

// cascadeFuzzy calls the cascade API for a property, fuzzy-matches the target,
// and logs the full match decision for diagnostics.
func (c *CarfaxHTTPClient) cascadeFuzzy(ctx context.Context, property, target string, params url.Values) (string, error) {
	options, err := c.cascadeOptions(ctx, property, params)
	if err != nil {
		slog.Warn("Carfax cascade failed",
			"processor", "facebook",
			"property", property,
			"target", target,
			"params", params.Encode(),
			"error", err)
		return "", err
	}
	if len(options) == 0 {
		slog.Warn("Carfax cascade returned empty options",
			"processor", "facebook",
			"property", property,
			"target", target,
			"params", params.Encode())
		return "", fmt.Errorf("no options returned for %s (target=%q, params=%s)", property, target, params.Encode())
	}

	matched := fuzzyMatch(target, options)

	// Log the match decision — critical for debugging when things go wrong
	if !strings.EqualFold(matched, target) {
		slog.Info("Carfax fuzzy match resolved",
			"processor", "facebook",
			"property", property,
			"input", target,
			"matched", matched,
			"option_count", len(options),
			"all_options", options)
	} else {
		slog.Debug("Carfax exact match found",
			"processor", "facebook",
			"property", property,
			"value", matched)
	}

	return matched, nil
}

// optionsCacheKey builds the normalized params fragment used as a Firestore doc ID
// for cached dropdown options. Excludes the "property" param since that's part of the key prefix.
func optionsCacheKey(params url.Values) string {
	// Build a deterministic key from the cascade parent params (year, make, model, trim, etc.)
	parts := make([]string, 0, len(params))
	for _, key := range []string{"year", "make", "model", "trim", "engine", "drivetrain", "transmission"} {
		if v := params.Get(key); v != "" {
			parts = append(parts, storage.CarfaxNormalize(v))
		}
	}
	return strings.Join(parts, "_")
}

// cascadeOptions fetches dropdown options, checking the Firestore options cache first.
// On cache miss, calls the Carfax API and saves the results for future lookups.
func (c *CarfaxHTTPClient) cascadeOptions(ctx context.Context, property string, params url.Values) ([]string, error) {
	cacheKey := optionsCacheKey(params)

	// Check options cache first
	cached, err := c.store.GetCarfaxOptions(ctx, property, cacheKey)
	if err != nil {
		slog.Warn("Carfax options cache lookup failed",
			"processor", "facebook", "property", property, "error", err)
	}
	if cached != nil {
		slog.Debug("Carfax options cache hit",
			"processor", "facebook",
			"property", property,
			"cache_key", cacheKey,
			"option_count", len(cached))
		return cached, nil
	}

	// Cache miss — call the Carfax API
	params.Set("property", property)
	apiURL := carfaxBaseURL + ".year-make-model.json?" + params.Encode()

	body, err := c.carfaxGETWithRetry(ctx, apiURL)
	if err != nil {
		return nil, fmt.Errorf("cascade %s API call: %w", property, err)
	}

	var resp carfaxYMMResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cascade %s parse: %w", property, err)
	}

	// Save to options cache for future lookups (fire-and-forget — don't block on cache write)
	if len(resp.Data) > 0 {
		if saveErr := c.store.SaveCarfaxOptions(ctx, property, cacheKey, resp.Data); saveErr != nil {
			slog.Warn("Failed to cache Carfax options",
				"processor", "facebook", "property", property, "error", saveErr)
		}
	}

	slog.Debug("Carfax cascade API returned options",
		"processor", "facebook",
		"property", property,
		"option_count", len(resp.Data),
		"options", resp.Data)

	return resp.Data, nil
}

// cascadeOrDefault attempts to cascade a property; on failure, returns the provided default.
// Logs extensively so we know exactly what happened when things break.
func (c *CarfaxHTTPClient) cascadeOrDefault(ctx context.Context, property, defaultVal string, params url.Values) string {
	options, err := c.cascadeOptions(ctx, property, params)
	if err != nil {
		slog.Warn("Carfax cascade failed, using provided default",
			"processor", "facebook",
			"property", property,
			"default", defaultVal,
			"params", params.Encode(),
			"error", err)
		return defaultVal
	}
	if len(options) == 0 {
		slog.Warn("Carfax cascade returned no options, using provided default",
			"processor", "facebook",
			"property", property,
			"default", defaultVal)
		return defaultVal
	}
	if len(options) == 1 {
		slog.Debug("Carfax cascade: single option available",
			"processor", "facebook",
			"property", property,
			"only_option", options[0])
		return options[0]
	}

	matched := fuzzyMatch(defaultVal, options)
	if !strings.EqualFold(matched, defaultVal) {
		slog.Info("Carfax cascade fuzzy match for secondary field",
			"processor", "facebook",
			"property", property,
			"input", defaultVal,
			"matched", matched,
			"all_options", options)
	}
	return matched
}

// submitValuation POSTs to the valuation-report endpoint and returns the reportID.
func (c *CarfaxHTTPClient) submitValuation(ctx context.Context, year int, make, model, trim, engine, drivetrain, transmission, bodyStyle, postalCode string, odometer int) (string, error) {
	cleanPostal := strings.ReplaceAll(strings.TrimSpace(postalCode), " ", "")

	formData := url.Values{
		"_charset_":     {"UTF-8"},
		"language":      {"en"},
		"Year":          {fmt.Sprintf("%d", year)},
		"Make":          {make},
		"Model":         {model},
		"Trim":          {trim},
		"Engines":       {engine},
		"DriveTrains":   {drivetrain},
		"Transmissions": {transmission},
		"BodyStyles":    {bodyStyle},
		"Odometer":      {fmt.Sprintf("%d", odometer)},
		"PostalCode":    {cleanPostal},
	}

	apiURL := carfaxBaseURL + ".valuation-report.json"

	body, err := c.carfaxPOSTWithRetry(ctx, apiURL, formData.Encode())
	if err != nil {
		return "", err
	}

	var resp carfaxValuationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("valuation response parse: %w", err)
	}

	if !resp.Success || resp.ReportID == "" {
		return "", fmt.Errorf("valuation failed: success=%v reportId=%s body=%s",
			resp.Success, resp.ReportID, string(body))
	}

	return resp.ReportID, nil
}

// fetchResults GETs the results page HTML and extracts the price range.
// The results page requires no authentication — just a valid reportID.
func (c *CarfaxHTTPClient) fetchResults(ctx context.Context, reportID string) (low, high float64, err error) {
	resultsURL := carfaxResultsBaseURL + "?reportId=" + reportID

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resultsURL, nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, 0, fmt.Errorf("results page request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("results page returned %d", resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read results page: %w", err)
	}

	bodyStr := string(bodyBytes)

	// Extract the price range text from the specific area of the page rather
	// than running the regex against the full HTML. The Playwright client uses
	// a JS equivalent that finds "Estimated Private Range" label and reads the
	// next sibling. Here we do a targeted text search:
	//   1. Find the price range near "Estimated Private Range" or "Estimated Trade-In Range"
	//   2. Extract the "$X,XXX - $Y,YYY" pattern from that region only
	//   3. Fall back to full-body regex ONLY if targeted search fails
	priceText := extractCarfaxPriceRegion(bodyStr)

	if priceText != "" {
		slog.Debug("Carfax price region extracted",
			"processor", "facebook", "component", "carfax_http",
			"price_text", priceText)
	}

	// Try targeted extraction first, then full body as fallback
	source := priceText
	if source == "" {
		source = bodyStr
	}

	if m := valueRangeRe.FindStringSubmatch(source); len(m) >= 3 {
		lowStr := strings.ReplaceAll(m[1], ",", "")
		highStr := strings.ReplaceAll(m[2], ",", "")
		fmt.Sscanf(lowStr, "%f", &low)
		fmt.Sscanf(highStr, "%f", &high)
		return low, high, nil
	}

	// Fallback: try parseValueRange for single-value extraction
	midValue, err := parseValueRange(source)
	if err != nil {
		return 0, 0, fmt.Errorf("could not extract price from results page (body_length=%d)", len(bodyStr))
	}
	return midValue, midValue, nil
}

// --- HTTP helpers ---

// carfaxGETWithRetry makes a GET request to a Carfax API endpoint with a reCAPTCHA token.
// Retries up to maxTokenRetries times with fresh tokens on 400 responses (reCAPTCHA rejection).
func (c *CarfaxHTTPClient) carfaxGETWithRetry(ctx context.Context, apiURL string) ([]byte, error) {
	var lastErr error
	for attempt := range maxTokenRetries {
		tokenStart := time.Now()
		token, err := c.tokens.GetToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("token fetch (attempt %d): %w", attempt+1, err)
		}
		tokenAge := time.Since(tokenStart)

		body, err := c.carfaxGET(ctx, apiURL, token)
		if err == nil {
			slog.Info("Carfax token accepted (GET)",
				"processor", "facebook", "component", "carfax_http",
				"attempt", attempt+1,
				"token_age_ms", tokenAge.Milliseconds())
			return body, nil
		}

		lastErr = err
		if !isRecaptchaRejection(err) {
			return nil, err // non-retryable error
		}

		slog.Warn("Carfax token rejected (GET), retrying with fresh token",
			"processor", "facebook", "component", "carfax_http",
			"attempt", attempt+1, "max_attempts", maxTokenRetries,
			"token_age_ms", tokenAge.Milliseconds(),
			"token_length", len(token),
			"url", truncateURL(apiURL),
			"rejection_body", recaptchaBody(err))
	}
	return nil, fmt.Errorf("carfax GET failed after %d attempts: %w", maxTokenRetries, lastErr)
}

// carfaxPOSTWithRetry makes a POST request with retry on reCAPTCHA rejection.
func (c *CarfaxHTTPClient) carfaxPOSTWithRetry(ctx context.Context, apiURL, formBody string) ([]byte, error) {
	var lastErr error
	for attempt := range maxTokenRetries {
		tokenStart := time.Now()
		token, err := c.tokens.GetToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("token fetch (attempt %d): %w", attempt+1, err)
		}
		tokenAge := time.Since(tokenStart)

		body, err := c.carfaxPOST(ctx, apiURL, token, formBody)
		if err == nil {
			slog.Info("Carfax token accepted (POST)",
				"processor", "facebook", "component", "carfax_http",
				"attempt", attempt+1,
				"token_age_ms", tokenAge.Milliseconds())
			return body, nil
		}

		lastErr = err
		if !isRecaptchaRejection(err) {
			return nil, err
		}

		slog.Warn("Carfax token rejected (POST), retrying with fresh token",
			"processor", "facebook", "component", "carfax_http",
			"attempt", attempt+1, "max_attempts", maxTokenRetries,
			"token_age_ms", tokenAge.Milliseconds(),
			"token_length", len(token),
			"rejection_body", recaptchaBody(err))
	}
	return nil, fmt.Errorf("carfax POST failed after %d attempts: %w", maxTokenRetries, lastErr)
}

func (c *CarfaxHTTPClient) carfaxGET(ctx context.Context, apiURL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	c.setCarfaxHeaders(req, token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode == 400 {
		return nil, &recaptchaError{status: 400, body: string(body)}
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("carfax returned %d: %s", resp.StatusCode, carfaxTruncate(string(body), 200))
	}

	return body, nil
}

func (c *CarfaxHTTPClient) carfaxPOST(ctx context.Context, apiURL, token, formBody string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, strings.NewReader(formBody))
	if err != nil {
		return nil, err
	}
	c.setCarfaxHeaders(req, token)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode == 400 {
		return nil, &recaptchaError{status: 400, body: string(body)}
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("carfax returned %d: %s", resp.StatusCode, carfaxTruncate(string(body), 200))
	}

	return body, nil
}

func (c *CarfaxHTTPClient) setCarfaxHeaders(req *http.Request, token string) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-CA,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Referer", "https://www.carfax.ca/whats-my-car-worth/car-value")
	if token != "" {
		req.Header.Set("x-recaptcha-token", token)
	}
}

// --- Fuzzy matching ---

// cleanAlphanumRe strips non-alphanumeric characters for fuzzy comparison.
var cleanAlphanumRe = regexp.MustCompile(`[^a-z0-9]`)

// fuzzyMatch finds the best match for target in options using a 3-tier strategy:
// 1. Exact match (case-insensitive)
// 2. Substring match (either direction)
// 3. Cleaned alphanumeric match
// Falls back to the first option if nothing matches.
func fuzzyMatch(target string, options []string) string {
	if len(options) == 0 {
		return target
	}
	if target == "" {
		return options[0]
	}

	lowerTarget := strings.ToLower(strings.TrimSpace(target))
	cleanTarget := cleanAlphanumRe.ReplaceAllString(lowerTarget, "")

	// Tier 1: exact case-insensitive match
	for _, opt := range options {
		if strings.EqualFold(strings.TrimSpace(opt), strings.TrimSpace(target)) {
			return opt
		}
	}

	// Tier 2: substring match (either direction)
	for _, opt := range options {
		lowerOpt := strings.ToLower(strings.TrimSpace(opt))
		if strings.Contains(lowerOpt, lowerTarget) || strings.Contains(lowerTarget, lowerOpt) {
			return opt
		}
	}

	// Tier 3: cleaned alphanumeric match
	for _, opt := range options {
		cleanOpt := cleanAlphanumRe.ReplaceAllString(strings.ToLower(opt), "")
		if strings.Contains(cleanOpt, cleanTarget) || strings.Contains(cleanTarget, cleanOpt) {
			return opt
		}
	}

	// No match — use first option
	slog.Debug("Carfax fuzzy match: no match found, using first option",
		"processor", "facebook", "target", target, "first_option", options[0])
	return options[0]
}

// --- Error types ---

type recaptchaError struct {
	status int
	body   string
}

func (e *recaptchaError) Error() string {
	return fmt.Sprintf("reCAPTCHA rejected (status %d): %s", e.status, carfaxTruncate(e.body, 100))
}

func isRecaptchaRejection(err error) bool {
	if err == nil {
		return false
	}
	_, ok := err.(*recaptchaError)
	return ok
}

// recaptchaBody extracts the truncated response body from a recaptchaError.
func recaptchaBody(err error) string {
	var re *recaptchaError
	if errors.As(err, &re) {
		return carfaxTruncate(re.body, 200)
	}
	return ""
}

// --- Utilities ---

func truncateURL(u string) string {
	if len(u) > 80 {
		return u[:80] + "..."
	}
	return u
}

func carfaxTruncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// htmlTagRe strips HTML tags to extract visible text from a region.
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// extractCarfaxPriceRegion finds the price range text near the
// "Estimated Private Range" or "Estimated Trade-In Range" labels on
// the Carfax results page. Returns a short text region (~500 chars)
// containing the price, or empty string if not found.
//
// This avoids running the price regex against the full HTML body,
// which matches SVG path data and other unrelated numeric patterns.
func extractCarfaxPriceRegion(body string) string {
	markers := []string{
		"Estimated Private Range",
		"Estimated Trade-In Range",
		"Estimated Value Range",
		"Private Party Value",
	}

	for _, marker := range markers {
		idx := strings.Index(body, marker)
		if idx == -1 {
			continue
		}
		// Grab a region around the marker — the price is usually within ~500 chars after
		start := idx
		end := idx + 500
		if end > len(body) {
			end = len(body)
		}
		region := body[start:end]
		// Strip HTML tags to get visible text only
		text := htmlTagRe.ReplaceAllString(region, " ")
		text = strings.Join(strings.Fields(text), " ")
		return text
	}

	return ""
}
