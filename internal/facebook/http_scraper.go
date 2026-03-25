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
	"strconv"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// ErrLoginWall is returned when Facebook redirects to a login/checkpoint page,
// indicating the proxy IP is blocked.
var ErrLoginWall = errors.New("facebook login wall detected")

// HTTPScraper fetches Facebook Marketplace pages via plain HTTP requests,
// parsing structured data from server-rendered <script> tags. No browser needed.
type HTTPScraper struct {
	baseProxyURL string
	logger       *slog.Logger
}

// NewHTTPScraper creates an HTTP scraper. proxyURL is the Evomi base proxy URL
// (empty string for direct connections).
func NewHTTPScraper(logger *slog.Logger, proxyURL string) *HTTPScraper {
	return &HTTPScraper{
		baseProxyURL: proxyURL,
		logger:       logger,
	}
}

// newProxiedClient creates an http.Client routed through the Evomi proxy
// targeted at the given city. Uses the same password-parameter format as
// browser.go:buildProxySettings.
func (s *HTTPScraper) newProxiedClient(city string) *http.Client {
	if s.baseProxyURL == "" {
		return &http.Client{
			Timeout: 30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	parsed, err := url.Parse(s.baseProxyURL)
	if err != nil {
		s.logger.Warn("Invalid proxy URL, using direct", "processor", "facebook", "error", err)
		return &http.Client{Timeout: 30 * time.Second}
	}

	password, _ := parsed.User.Password()
	username := parsed.User.Username()
	password += "_country-CA"
	if evomiCity := EvomiCityForCity(city); evomiCity != "" {
		password += "_city-" + evomiCity
	}
	password += "_session-" + randomSessionID() + "_lifetime-10"

	proxyURL := &url.URL{
		Scheme: parsed.Scheme,
		User:   url.UserPassword(username, password),
		Host:   parsed.Host,
	}

	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// doFacebookGET performs a GET request with realistic browser headers.
func doFacebookGET(ctx context.Context, client *http.Client, targetURL string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return nil, err
	}

	profile := profiles[rand.Intn(len(profiles))]
	req.Header.Set("User-Agent", profile.userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-CA,en-US;q=0.9,en;q=0.8")
	req.Header.Set("Accept-Encoding", "identity") // no compression — we need to parse the body
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	return client.Do(req)
}

// detectProxyIPHTTP discovers the proxy's external IP via api.ipify.org.
func detectProxyIPHTTP(ctx context.Context, client *http.Client) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return ""
	}
	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(string(body))
	if ip == "" || len(ip) > 45 {
		return ""
	}
	return ip
}

// ScrapeMarketplaceHTTP fetches the Facebook Marketplace feed via HTTP and
// extracts listing data from server-rendered JSON. Handles proxy
// rotation and blocklist management identically to the Playwright version.
func ScrapeMarketplaceHTTP(ctx context.Context, logger *slog.Logger, s *HTTPScraper, cfg *FacebookScrapeConfig, blocklist ProxyBlocklist) ([]models.ScrapedAd, error) {
	if cfg.City == "" {
		return nil, fmt.Errorf("scrape config has no city configured")
	}

	category := cfg.Category
	if category == "" {
		category = "Vehicles"
	}
	radiusKm := cfg.RadiusKm
	if radiusKm <= 0 {
		radiusKm = 500
	}

	targetURL, err := BuildMarketplaceURL(cfg.City, category, radiusKm)
	if err != nil {
		return nil, fmt.Errorf("failed to build URL for %q: %w", cfg.City, err)
	}

	hasProxy := s.baseProxyURL != ""
	retries := 1
	if hasProxy && blocklist != nil {
		retries = maxProxyRetries
	}

	var lastErr error

	// Phase 1: city-targeted proxy retries
	for attempt := 0; attempt < retries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			time.Sleep(time.Duration(500+attempt*500) * time.Millisecond)
		}

		ads, proxyIP, err := scrapeMarketplaceHTTPOnce(ctx, logger, s, cfg, cfg.City, targetURL, blocklist)
		if err == nil {
			return ads, nil
		}
		lastErr = err

		if !hasProxy || blocklist == nil {
			return nil, err
		}

		// Known-blocked IP — rotate
		if strings.Contains(err.Error(), "already blocked") {
			logger.Info("Skipping known-blocked proxy IP, rotating",
				"processor", "facebook", "component", "http_scrape",
				"ip", proxyIP, "city", cfg.City,
				"attempt", attempt+1, "max_retries", retries)
			continue
		}

		// Login wall = blocked IP — save and rotate
		if errors.Is(err, ErrLoginWall) {
			if proxyIP != "" {
				if blockErr := blocklist.BlockProxyIP(ctx, proxyIP, cfg.City); blockErr != nil {
					logger.Warn("Failed to save blocked proxy IP", "processor", "facebook", "ip", proxyIP, "error", blockErr)
				}
			}
			logger.Warn("Facebook login wall (blocked IP), adding to blocklist and rotating",
				"processor", "facebook", "component", "http_scrape",
				"ip", proxyIP, "city", cfg.City,
				"attempt", attempt+1, "max_retries", retries)
			continue
		}

		// Non-proxy error — not retryable
		return nil, err
	}

	// Phase 2: country-level fallback after city pool exhausted
	if !hasProxy || lastErr == nil {
		return nil, lastErr
	}
	errMsg := lastErr.Error()
	if !errors.Is(lastErr, ErrLoginWall) && !strings.Contains(errMsg, "already blocked") {
		return nil, lastErr
	}

	logger.Info("City proxy pool exhausted, falling back to country-level proxy",
		"processor", "facebook", "component", "http_scrape", "city", cfg.City)

	for attempt := 0; attempt < maxCountryFallbackRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt > 0 {
			time.Sleep(time.Duration(500+attempt*500) * time.Millisecond)
		}

		ads, proxyIP, err := scrapeMarketplaceHTTPOnce(ctx, logger, s, cfg, "", targetURL, blocklist)
		if err == nil {
			logger.Info("Country-level proxy succeeded",
				"processor", "facebook", "component", "http_scrape",
				"city", cfg.City, "proxy_ip", proxyIP)
			return ads, nil
		}
		lastErr = err

		if strings.Contains(err.Error(), "already blocked") {
			logger.Info("Skipping known-blocked proxy IP (country fallback), rotating",
				"processor", "facebook", "component", "http_scrape",
				"ip", proxyIP, "city", cfg.City, "attempt", attempt+1)
			continue
		}
		if errors.Is(err, ErrLoginWall) {
			if proxyIP != "" && blocklist != nil {
				_ = blocklist.BlockProxyIP(ctx, proxyIP, cfg.City)
			}
			logger.Warn("Facebook login wall (country fallback), blocked IP and rotating",
				"processor", "facebook", "component", "http_scrape",
				"ip", proxyIP, "city", cfg.City, "attempt", attempt+1)
			continue
		}
		return nil, err
	}

	logger.Error("All proxy retries exhausted", "processor", "facebook", "component", "http_scrape",
		"city", cfg.City, "last_error", lastErr)
	return nil, lastErr
}

// scrapeMarketplaceHTTPOnce performs a single HTTP scrape attempt.
func scrapeMarketplaceHTTPOnce(ctx context.Context, logger *slog.Logger, s *HTTPScraper, cfg *FacebookScrapeConfig, proxyCity, targetURL string, blocklist ProxyBlocklist) ([]models.ScrapedAd, string, error) {
	client := s.newProxiedClient(proxyCity)

	// Detect proxy IP
	proxyIP := detectProxyIPHTTP(ctx, client)

	// Pre-check blocklist
	if proxyIP != "" && blocklist != nil {
		blocked, err := blocklist.IsProxyBlocked(ctx, proxyIP)
		if err != nil {
			logger.Warn("Failed to check proxy blocklist", "processor", "facebook", "ip", proxyIP, "error", err)
		} else if blocked {
			return nil, proxyIP, fmt.Errorf("proxy IP %s already blocked for %s", proxyIP, cfg.City)
		}
	}

	logger.Info("HTTP scraping marketplace", "processor", "facebook", "component", "http_scrape",
		"city", cfg.City, "url", targetURL, "proxy_ip", proxyIP)

	resp, err := doFacebookGET(ctx, client, targetURL)
	if err != nil {
		return nil, proxyIP, fmt.Errorf("HTTP request failed for %s: %w", cfg.City, err)
	}
	defer resp.Body.Close()

	// Log response status — non-200 is always interesting
	if resp.StatusCode != http.StatusOK {
		logger.Warn("Facebook returned non-200 status", "processor", "facebook", "component", "http_scrape",
			"status", resp.StatusCode, "city", cfg.City, "proxy_ip", proxyIP)
	}

	// Check for redirect to login/checkpoint
	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "login") || strings.Contains(loc, "checkpoint") {
			return nil, proxyIP, fmt.Errorf("redirected to %s: %w", loc, ErrLoginWall)
		}
		logger.Warn("Facebook redirected to unexpected location", "processor", "facebook", "component", "http_scrape",
			"location", loc, "city", cfg.City, "proxy_ip", proxyIP)
		return nil, proxyIP, fmt.Errorf("unexpected redirect to %s", loc)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB max
	if err != nil {
		return nil, proxyIP, fmt.Errorf("failed to read response body: %w", err)
	}

	html := string(body)
	logger.Debug("Feed page response", "processor", "facebook", "component", "http_scrape",
		"body_bytes", len(body), "city", cfg.City, "proxy_ip", proxyIP)

	// Check for login wall in body
	if isLoginWall(html) {
		snippet := html
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		logger.Warn("Login wall detected in response body", "processor", "facebook", "component", "http_scrape",
			"city", cfg.City, "proxy_ip", proxyIP, "body_snippet", snippet)
		return nil, proxyIP, fmt.Errorf("login wall in response body: %w", ErrLoginWall)
	}

	// Parse listings from server-rendered JSON
	blobs := extractScriptJSONBlobs(html)
	if len(blobs) == 0 {
		snippet := html
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		logger.Warn("No script JSON blobs found in response", "processor", "facebook", "component", "http_scrape",
			"city", cfg.City, "proxy_ip", proxyIP, "body_bytes", len(body), "body_snippet", snippet)
		return nil, proxyIP, fmt.Errorf("no <script type=\"application/json\"> tags found: %w", ErrLoginWall)
	}

	logger.Debug("Parsed script blobs", "processor", "facebook", "component", "http_scrape",
		"blob_count", len(blobs), "city", cfg.City)

	var allAds []models.ScrapedAd
	seenIDs := make(map[string]bool)

	totalListingsFound := 0
	for _, blob := range blobs {
		listings := findMarketplaceListings(blob)
		totalListingsFound += len(listings)
		for _, l := range listings {
			if seenIDs[l.ID] {
				continue
			}

			// Apply brand filter
			if len(cfg.FilterBrands) > 0 {
				lwTitle := strings.ToLower(l.Title)
				found := false
				for _, b := range cfg.FilterBrands {
					if strings.Contains(lwTitle, strings.ToLower(b)) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}

			// Skip ads without a concrete price
			if l.Price <= 0 && l.PriceText != "Free" && l.PriceText != "" {
				continue
			}

			seenIDs[l.ID] = true
			allAds = append(allAds, models.ScrapedAd{
				ListingID: l.ID,
				Title:     l.Title,
				Price:     l.Price,
				URL:       fmt.Sprintf("https://www.facebook.com/marketplace/item/%s/", l.ID),
				Mileage:   l.Mileage,
				Category:  l.Category,
			})
		}
	}

	if totalListingsFound == 0 && len(blobs) > 0 {
		logger.Warn("Script blobs found but no marketplace listings extracted — Facebook may have changed JSON structure",
			"processor", "facebook", "component", "http_scrape",
			"blob_count", len(blobs), "city", cfg.City, "proxy_ip", proxyIP)
	}

	logger.Info("HTTP extracted ads", "processor", "facebook", "component", "http_scrape",
		"count", len(allAds), "raw_listings", totalListingsFound, "city", cfg.City, "proxy_ip", proxyIP)

	return allAds, proxyIP, nil
}

// isLoginWall checks if the HTML response is a Facebook login/checkpoint page.
func isLoginWall(html string) bool {
	if strings.Contains(html, "/login/") && strings.Contains(html, "checkpoint") {
		return true
	}
	// A page with no marketplace data but a login form
	if !strings.Contains(html, "GroupCommerceProductItem") &&
		strings.Contains(html, "login_form") {
		return true
	}
	return false
}

// ScrapeListingDetailHTTP fetches an individual listing page and extracts
// structured vehicle data from server-rendered JSON. Returns a CarData
// (nil if extraction fails) and the seller description.
func ScrapeListingDetailHTTP(ctx context.Context, logger *slog.Logger, s *HTTPScraper, adURL, proxyCity string, blocklist ProxyBlocklist) (*models.CarData, string, error) {
	client := s.newProxiedClient(proxyCity)

	resp, err := doFacebookGET(ctx, client, adURL)
	if err != nil {
		return nil, "", fmt.Errorf("detail page request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logger.Warn("Detail page non-200 status", "processor", "facebook", "component", "http_scrape",
			"status", resp.StatusCode, "url", adURL)
	}

	if resp.StatusCode == http.StatusFound || resp.StatusCode == http.StatusMovedPermanently {
		loc := resp.Header.Get("Location")
		if strings.Contains(loc, "login") || strings.Contains(loc, "checkpoint") {
			logger.Warn("Detail page login wall redirect", "processor", "facebook", "component", "http_scrape",
				"url", adURL, "redirect", loc)
			return nil, "", ErrLoginWall
		}
		logger.Warn("Detail page unexpected redirect", "processor", "facebook", "component", "http_scrape",
			"url", adURL, "redirect", loc)
		return nil, "", fmt.Errorf("detail page redirected to %s", loc)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read detail page: %w", err)
	}

	html := string(body)
	if isLoginWall(html) {
		logger.Warn("Detail page login wall in body", "processor", "facebook", "component", "http_scrape",
			"url", adURL, "body_bytes", len(body))
		return nil, "", ErrLoginWall
	}

	blobs := extractScriptJSONBlobs(html)
	if len(blobs) == 0 {
		logger.Warn("Detail page has no script JSON blobs", "processor", "facebook", "component", "http_scrape",
			"url", adURL, "body_bytes", len(body))
		return nil, "", nil
	}

	var detail *listingDetailData
	for _, blob := range blobs {
		detail = findListingDetail(blob)
		if detail != nil {
			break
		}
	}

	if detail == nil {
		logger.Debug("Detail page: blobs found but no vehicle data extracted", "processor", "facebook", "component", "http_scrape",
			"url", adURL, "blob_count", len(blobs))
		return nil, "", nil
	}

	logger.Debug("Detail page vehicle data extracted", "processor", "facebook", "component", "http_scrape",
		"url", adURL, "make", detail.Make, "model", detail.Model, "trim", detail.Trim,
		"odometer", detail.Odometer, "transmission", detail.Transmission)

	// Build CarData from structured fields
	carData := &models.CarData{
		Make:         detail.Make,
		Model:        detail.Model,
		Trim:         detail.Trim,
		Odometer:     detail.Odometer,
		Transmission: mapTransmission(detail.Transmission),
		Drivetrain:   detail.Drivetrain,
		BodyStyle:    detail.BodyStyle,
		Condition:    mapCondition(detail.Condition),
	}

	// Year from title
	if detail.Title != "" {
		carData.Year = extractYearFromTitle(detail.Title)
	}

	// Map category to vehicle type
	carData.VehicleType = mapCategoryToVehicleType(detail.LeafCategory)

	// Build short description
	parts := []string{}
	if carData.Year > 0 {
		parts = append(parts, strconv.Itoa(carData.Year))
	}
	if carData.Make != "" {
		parts = append(parts, carData.Make)
	}
	if carData.Model != "" {
		parts = append(parts, carData.Model)
	}
	if carData.Odometer > 0 {
		parts = append(parts, fmt.Sprintf("%dk km", carData.Odometer/1000))
	}
	if len(parts) > 0 {
		carData.Description = strings.Join(parts, " ")
	}

	return carData, detail.Description, nil
}

// --- JSON parsing ---

// extractScriptJSONBlobs finds all <script type="application/json"> content in HTML.
func extractScriptJSONBlobs(html string) []string {
	var blobs []string
	openTag := `<script type="application/json"`
	closeTag := `</script>`

	for {
		idx := strings.Index(html, openTag)
		if idx == -1 {
			break
		}
		// Find the end of the opening tag (the > after attributes)
		tagEnd := strings.Index(html[idx:], ">")
		if tagEnd == -1 {
			break
		}
		contentStart := idx + tagEnd + 1
		endIdx := strings.Index(html[contentStart:], closeTag)
		if endIdx == -1 {
			break
		}
		blob := html[contentStart : contentStart+endIdx]
		if len(blob) > 100 { // skip tiny blobs
			blobs = append(blobs, blob)
		}
		html = html[contentStart+endIdx+len(closeTag):]
	}
	return blobs
}

// marketplaceFeedItem is an intermediate type for parsed feed listings.
type marketplaceFeedItem struct {
	ID        string
	Title     string
	Price     float64
	PriceText string
	City      string
	Province  string
	Category  string
	Mileage   string
}

// findMarketplaceListings recursively walks a JSON blob looking for
// GroupCommerceProductItem nodes and extracts listing data.
func findMarketplaceListings(blob string) []marketplaceFeedItem {
	var parsed any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		// JSON parse failures are expected for non-data blobs (resource maps, etc.)
		return nil
	}

	var listings []marketplaceFeedItem
	walkJSON(parsed, func(obj map[string]any) {
		typename, _ := obj["__typename"].(string)
		if typename != "GroupCommerceProductItem" {
			return
		}

		id, _ := obj["id"].(string)
		if id == "" {
			return
		}

		title, _ := obj["marketplace_listing_title"].(string)
		if title == "" {
			// Try custom_title as fallback
			title, _ = obj["custom_title"].(string)
		}
		if title == "" {
			return
		}

		item := marketplaceFeedItem{
			ID:    id,
			Title: strings.TrimSpace(title),
		}

		// Price
		if listing, ok := obj["listing_price"].(map[string]any); ok {
			if amountStr, ok := listing["amount"].(string); ok {
				item.Price, _ = strconv.ParseFloat(amountStr, 64)
			}
			item.PriceText, _ = listing["formatted_amount"].(string)
		}

		// Location
		if loc, ok := obj["location"].(map[string]any); ok {
			if rg, ok := loc["reverse_geocode"].(map[string]any); ok {
				item.City, _ = rg["city"].(string)
				item.Province, _ = rg["state"].(string)
			}
		}

		// Category
		cat, _ := obj["marketplace_listing_leaf_vt_category_name"].(string)
		item.Category = cat

		// Mileage from subtitles
		if subs, ok := obj["custom_sub_titles_with_rendering_flags"].([]any); ok {
			for _, sub := range subs {
				if subMap, ok := sub.(map[string]any); ok {
					if subtitle, ok := subMap["subtitle"].(string); ok && subtitle != "" {
						item.Mileage = subtitle
						break
					}
				}
			}
		}

		listings = append(listings, item)
	})

	return listings
}

// listingDetailData holds vehicle fields from a detail page.
type listingDetailData struct {
	Make         string
	Model        string
	Trim         string
	Odometer     int
	Transmission string
	ExteriorColor string
	Drivetrain   string
	BodyStyle    string
	FuelType     string
	Condition    string
	SellerType   string
	Description  string
	Title        string
	LeafCategory string
}

// findListingDetail walks a JSON blob for vehicle detail fields.
func findListingDetail(blob string) *listingDetailData {
	var parsed any
	if err := json.Unmarshal([]byte(blob), &parsed); err != nil {
		return nil
	}

	var detail *listingDetailData
	walkJSON(parsed, func(obj map[string]any) {
		if detail != nil {
			return // already found
		}

		// Look for vehicle_make_display_name as the signal this is the right node
		make, hasMake := obj["vehicle_make_display_name"].(string)
		if !hasMake {
			return
		}

		detail = &listingDetailData{
			Make: make,
		}
		detail.Model, _ = obj["vehicle_model_display_name"].(string)
		detail.Trim, _ = obj["vehicle_trim_display_name"].(string)
		detail.Transmission, _ = obj["vehicle_transmission_type"].(string)
		detail.ExteriorColor, _ = obj["vehicle_exterior_color"].(string)
		detail.Drivetrain, _ = obj["vehicle_drivetrain"].(string)
		detail.BodyStyle, _ = obj["vehicle_body_style"].(string)
		detail.FuelType, _ = obj["vehicle_fuel_type"].(string)
		detail.Condition, _ = obj["vehicle_condition"].(string)
		detail.SellerType, _ = obj["vehicle_seller_type"].(string)

		// Odometer
		if odo, ok := obj["vehicle_odometer_data"].(map[string]any); ok {
			if val, ok := odo["value"].(float64); ok {
				detail.Odometer = int(val)
			}
		}

		// Description
		if desc, ok := obj["redacted_description"].(map[string]any); ok {
			detail.Description, _ = desc["text"].(string)
		}

		// Title
		detail.Title, _ = obj["marketplace_listing_title"].(string)

		// Category
		detail.LeafCategory, _ = obj["marketplace_listing_leaf_vt_category_name"].(string)
	})

	return detail
}

// walkJSON recursively walks a JSON value, calling fn for every map[string]any encountered.
func walkJSON(v any, fn func(map[string]any)) {
	switch val := v.(type) {
	case map[string]any:
		fn(val)
		for _, child := range val {
			walkJSON(child, fn)
		}
	case []any:
		for _, item := range val {
			walkJSON(item, fn)
		}
	}
}

// --- Helpers ---

var yearRegex = regexp.MustCompile(`\b(19[6-9]\d|20[0-3]\d)\b`)

func extractYearFromTitle(title string) int {
	match := yearRegex.FindString(title)
	if match == "" {
		return 0
	}
	year, _ := strconv.Atoi(match)
	return year
}

func mapTransmission(fbTransmission string) string {
	switch strings.ToUpper(fbTransmission) {
	case "AUTOMATIC":
		return "Automatic"
	case "MANUAL":
		return "Manual"
	default:
		return fbTransmission
	}
}

func mapCondition(fbCondition string) string {
	switch strings.ToUpper(fbCondition) {
	case "NEW":
		return "New"
	case "USED":
		return "Used"
	case "USED_LIKE_NEW":
		return "Like New"
	case "USED_GOOD":
		return "Good"
	case "USED_FAIR":
		return "Fair"
	default:
		return fbCondition
	}
}

func mapCategoryToVehicleType(category string) string {
	switch category {
	case "Cars & Trucks":
		return "car"
	case "Motorcycles & Scooters":
		return "motorcycle"
	case "Powersport Vehicles":
		return "powersport"
	case "Boats":
		return "boat"
	case "RV / Campers":
		return "rv"
	case "Trailers":
		return "trailer"
	default:
		return ""
	}
}
