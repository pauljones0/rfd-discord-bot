package ebay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const (
	ebayAPIBase       = "https://api.ebay.com"
	ebayTokenURL      = ebayAPIBase + "/identity/v1/oauth2/token"
	ebayBrowseURL     = ebayAPIBase + "/buy/browse/v1/item_summary/search"
	ebayBrowseItemURL = ebayAPIBase + "/buy/browse/v1/item"
	ebayScope         = "https://api.ebay.com/oauth/api_scope"

	// browsePageLimit is the maximum items per page from the Browse API.
	browsePageLimit = 200

	// browseMaxPages caps pagination per marketplace group (eBay Browse API max offset is 9,800).
	browseMaxPages = 50 // 50 × 200 = 10,000 items per marketplace group
)

var browseTechCategoryIDs = []string{
	"58058", // Computers/Tablets & Networking
	"293",   // Consumer Electronics
	"15032", // Cell Phones & Accessories
	"1249",  // Video Games & Consoles
}

// Client handles eBay OAuth and Browse API interactions.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time

	couponBackends []string
}

type marketplaceCategoryGroup struct {
	marketplace     string
	categorySellers map[string][]string
}

// NewClient creates a new eBay API client.
// Returns nil if credentials are missing or placeholder (graceful degradation).
func NewClient(clientID, clientSecret string) *Client {
	if clientID == "" || clientSecret == "" ||
		strings.HasPrefix(clientID, "placeholder") || strings.HasPrefix(clientSecret, "placeholder") {
		slog.Warn("eBay API credentials not configured (missing or placeholder), eBay features disabled")
		return nil
	}

	return &Client{
		clientID:       clientID,
		clientSecret:   clientSecret,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
		couponBackends: []string{scrapebackend.BackendHTTP},
	}
}

// SetCouponBackends configures the ordered fallback list for buyer-visible
// listing-page coupon discovery. Empty input leaves the existing default.
func (c *Client) SetCouponBackends(backends []string) {
	if c == nil || len(backends) == 0 {
		return
	}
	c.couponBackends = append([]string(nil), backends...)
}

// tokenResponse represents the eBay OAuth token response.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

// getToken returns a valid access token, refreshing if necessary.
func (c *Client) getToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.accessToken != "" && time.Now().Before(c.tokenExpiry.Add(-60*time.Second)) {
		return c.accessToken, nil
	}

	slog.Info("Refreshing eBay OAuth token")

	data := url.Values{
		"grant_type": {"client_credentials"},
		"scope":      {ebayScope},
	}

	req, err := http.NewRequestWithContext(ctx, "POST", ebayTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}

	credentials := base64.StdEncoding.EncodeToString([]byte(c.clientID + ":" + c.clientSecret))
	req.Header.Set("Authorization", "Basic "+credentials)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("eBay token request failed: HTTP %d, body: %s", resp.StatusCode, string(body))
	}

	var tokenResp tokenResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	c.accessToken = tokenResp.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	slog.Info("eBay OAuth token refreshed", "expires_in_seconds", tokenResp.ExpiresIn)
	return c.accessToken, nil
}

// invalidateToken forces a token refresh on next call.
func (c *Client) invalidateToken() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accessToken = ""
	c.tokenExpiry = time.Time{}
}

// SearchSellerListings fetches Buy It Now listings from the given sellers.
// Groups sellers by marketplace and queries each group in a single paginated request
// to minimize Browse API calls.
// If sinceTime is non-zero, only items listed after that time are returned.
func (c *Client) SearchSellerListings(ctx context.Context, sellers []EbaySeller, sinceTime time.Time) ([]BrowseAPIItem, error) {
	if c == nil {
		return nil, fmt.Errorf("eBay client not initialized")
	}
	if len(sellers) == 0 {
		return nil, nil
	}

	groups := buildMarketplaceCategoryGroups(sellers)

	var allItems []BrowseAPIItem
	var groupErrs []error
	for i, g := range groups {
		if i > 0 {
			select {
			case <-ctx.Done():
				return allItems, ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
		items, err := c.fetchMarketplaceListings(ctx, g.marketplace, g.categorySellers, sinceTime)
		if err != nil {
			groupErrs = append(groupErrs, err)
			slog.Warn("Failed to fetch eBay marketplace listings",
				"marketplace", g.marketplace,
				"sellers", countDistinctSellers(g.categorySellers),
				"error", err,
			)
			continue
		}
		allItems = append(allItems, items...)
	}

	if err := c.populateCouponDetails(ctx, allItems); err != nil {
		slog.Warn("Failed to fetch some eBay coupon details", "processor", "ebay", "error", err)
	}

	if len(groupErrs) > 0 {
		return allItems, fmt.Errorf("failed to fetch %d eBay marketplace group(s): %w", len(groupErrs), errors.Join(groupErrs...))
	}
	return allItems, nil
}

func buildMarketplaceCategoryGroups(sellers []EbaySeller) []marketplaceCategoryGroup {
	seen := make(map[string]int)
	var groups []marketplaceCategoryGroup
	for _, s := range sellers {
		marketplace := s.MarketplaceID()
		idx, ok := seen[marketplace]
		if !ok {
			idx = len(groups)
			seen[marketplace] = idx
			groups = append(groups, marketplaceCategoryGroup{
				marketplace:     marketplace,
				categorySellers: make(map[string][]string),
			})
		}
		for _, categoryID := range s.EffectiveCategoryIDs() {
			groups[idx].categorySellers[categoryID] = append(groups[idx].categorySellers[categoryID], s.Username)
		}
	}

	for i := range groups {
		for categoryID, usernames := range groups[i].categorySellers {
			sort.Strings(usernames)
			groups[i].categorySellers[categoryID] = usernames
		}
	}

	return groups
}

func orderedCategoryIDs(categorySellers map[string][]string) []string {
	seen := make(map[string]struct{}, len(categorySellers))
	ordered := make([]string, 0, len(categorySellers))
	for _, id := range browseTechCategoryIDs {
		if _, ok := categorySellers[id]; ok {
			seen[id] = struct{}{}
			ordered = append(ordered, id)
		}
	}
	var extras []string
	for id := range categorySellers {
		if _, ok := seen[id]; ok {
			continue
		}
		extras = append(extras, id)
	}
	sort.Strings(extras)
	return append(ordered, extras...)
}

func countDistinctSellers(categorySellers map[string][]string) int {
	seen := make(map[string]struct{})
	for _, usernames := range categorySellers {
		for _, username := range usernames {
			seen[username] = struct{}{}
		}
	}
	return len(seen)
}

// fetchMarketplaceListings fetches all BIN listings for a group of sellers in the
// same marketplace using a single combined query with pagination.
func (c *Client) fetchMarketplaceListings(ctx context.Context, marketplace string, categorySellers map[string][]string, sinceTime time.Time) ([]BrowseAPIItem, error) {
	seen := make(map[string]struct{})
	var allItems []BrowseAPIItem
	var categoryErrs []error

	for _, categoryID := range orderedCategoryIDs(categorySellers) {
		usernames := categorySellers[categoryID]
		items, err := c.fetchCategoryListings(ctx, usernames, marketplace, categoryID, sinceTime)
		if err != nil {
			categoryErrs = append(categoryErrs, fmt.Errorf("category %s: %w", categoryID, err))
			slog.Warn("Failed to fetch eBay tech category listings",
				"marketplace", marketplace,
				"category_id", categoryID,
				"sellers", len(usernames),
				"error", err,
			)
			continue
		}
		allItems = appendUniqueBrowseItems(allItems, items, seen)
	}

	var err error
	if len(categoryErrs) > 0 {
		err = fmt.Errorf("failed to fetch %d eBay categor%s for %s: %w",
			len(categoryErrs),
			pluralSuffix(len(categoryErrs), "y", "ies"),
			marketplace,
			errors.Join(categoryErrs...),
		)
	}

	slog.Info("Fetched eBay marketplace listings",
		"marketplace", marketplace,
		"sellers", countDistinctSellers(categorySellers),
		"categories", len(categorySellers),
		"total_items", len(allItems),
	)
	return allItems, err
}

func pluralSuffix(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func (c *Client) fetchCategoryListings(ctx context.Context, usernames []string, marketplace, categoryID string, sinceTime time.Time) ([]BrowseAPIItem, error) {
	var allItems []BrowseAPIItem
	offset := 0
	sellerFilter := strings.Join(usernames, "|")

	for page := 0; page < browseMaxPages; page++ {
		items, hasMore, err := c.fetchPage(ctx, sellerFilter, marketplace, categoryID, offset, sinceTime)
		if err != nil {
			return allItems, err
		}
		allItems = append(allItems, items...)
		if !hasMore || len(items) == 0 {
			break
		}
		offset += browsePageLimit
	}

	if offset >= browseMaxPages*browsePageLimit {
		slog.Warn("eBay marketplace hit pagination cap — some items may be missing",
			"marketplace", marketplace,
			"category_id", categoryID,
			"sellers", len(usernames),
			"pages_fetched", browseMaxPages,
		)
	}

	slog.Info("Fetched eBay tech category listings",
		"marketplace", marketplace,
		"category_id", categoryID,
		"sellers", len(usernames),
		"items", len(allItems),
	)
	return allItems, nil
}

// setBrowseHeaders sets the standard headers for eBay Browse API requests.
func setBrowseHeaders(req *http.Request, token, marketplace string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-EBAY-C-MARKETPLACE-ID", marketplace)
	req.Header.Set("Content-Type", "application/json")
}

// fetchPage fetches one page of BIN listings from the Browse API.
// sellerFilter is a pipe-separated list of seller usernames (e.g. "seller1|seller2").
func buildBrowseQueryParams(categoryID, sellerFilter string, offset int, sinceTime time.Time) url.Values {
	filterParts := fmt.Sprintf("sellers:{%s},buyingOptions:{FIXED_PRICE}", sellerFilter)
	if !sinceTime.IsZero() {
		filterParts += fmt.Sprintf(",itemStartDate:[%s..]", sinceTime.UTC().Format(time.RFC3339))
	}

	return url.Values{
		"category_ids": {categoryID},
		"filter":       {filterParts},
		"limit":        {fmt.Sprintf("%d", browsePageLimit)},
		"offset":       {fmt.Sprintf("%d", offset)},
	}
}

func appendUniqueBrowseItems(dst, items []BrowseAPIItem, seen map[string]struct{}) []BrowseAPIItem {
	for _, item := range items {
		if _, exists := seen[item.ItemID]; exists {
			continue
		}
		seen[item.ItemID] = struct{}{}
		dst = append(dst, item)
	}
	return dst
}

type couponSnapshot struct {
	DiscountAmount float64
	Code           string
	Message        string
	Source         string
}

func (c *Client) populateCouponDetails(ctx context.Context, items []BrowseAPIItem) error {
	failures := 0
	enriched := 0

	for i := range items {
		if !items[i].AvailableCoupons {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		coupon, err := c.fetchItemCouponSnapshot(ctx, items[i])
		if err != nil {
			failures++
			slog.Warn("Failed to fetch eBay item coupon details",
				"processor", "ebay",
				"itemID", items[i].ItemID,
				"error", err,
			)
			continue
		}
		if coupon.DiscountAmount <= 0 {
			continue
		}

		items[i].CouponDiscount = coupon.DiscountAmount
		items[i].CouponCode = coupon.Code
		items[i].CouponMessage = coupon.Message
		items[i].CouponSource = coupon.Source
		enriched++
	}

	if enriched > 0 {
		slog.Info("Fetched eBay coupon details", "processor", "ebay", "items", enriched)
	}
	if failures > 0 {
		return fmt.Errorf("coupon detail fetch failed for %d item(s)", failures)
	}
	return nil
}

func (c *Client) fetchItemCouponSnapshot(ctx context.Context, item BrowseAPIItem) (couponSnapshot, error) {
	body, err := c.fetchBrowseItemBody(ctx, browseItemDetailURL(item), item.Marketplace)
	if err != nil {
		return couponSnapshot{}, err
	}

	var detail BrowseAPIItemDetail
	if err := json.Unmarshal(body, &detail); err != nil {
		return couponSnapshot{}, fmt.Errorf("failed to parse item detail response: %w", err)
	}
	return bestCouponSnapshot(detail.AvailableCoupons), nil
}

func browseItemDetailURL(item BrowseAPIItem) string {
	if item.ItemHref != "" {
		return item.ItemHref
	}
	return ebayBrowseItemURL + "/" + url.PathEscape(item.ItemID)
}

func (c *Client) fetchBrowseItemBody(ctx context.Context, reqURL, marketplace string) ([]byte, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get eBay token: %w", err)
	}

	body, statusCode, err := c.doBrowseItemRequest(ctx, reqURL, token, marketplace)
	if err != nil {
		return nil, err
	}
	if statusCode == http.StatusUnauthorized {
		slog.Warn("eBay item API returned 401, refreshing token and retrying")
		c.invalidateToken()

		token, err = c.getToken(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to refresh eBay token: %w", err)
		}
		body, statusCode, err = c.doBrowseItemRequest(ctx, reqURL, token, marketplace)
		if err != nil {
			return nil, err
		}
	}
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("eBay Browse item API error: HTTP %d, body: %s", statusCode, string(body))
	}

	return body, nil
}

func (c *Client) doBrowseItemRequest(ctx context.Context, reqURL, token, marketplace string) ([]byte, int, error) {
	if marketplace == "" {
		marketplace = "EBAY_CA"
	}

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create item detail request: %w", err)
	}
	setBrowseHeaders(req, token, marketplace)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("item detail request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read item detail response: %w", err)
	}
	return body, resp.StatusCode, nil
}

func bestCouponSnapshot(coupons []AvailableCoupon) couponSnapshot {
	var best couponSnapshot
	for _, coupon := range coupons {
		discount := parsePrice(coupon.DiscountAmount)
		if discount <= best.DiscountAmount {
			continue
		}
		best = couponSnapshot{
			DiscountAmount: discount,
			Code:           coupon.RedemptionCode,
			Message:        coupon.Message,
			Source:         "api",
		}
	}
	return best
}

// FetchPageCouponSnapshot attempts buyer-visible eBay listing-page coupon
// discovery using the configured backend fallback order.
func (c *Client) FetchPageCouponSnapshot(ctx context.Context, item BrowseAPIItem, basePrice float64) (couponSnapshot, error) {
	if c == nil {
		return couponSnapshot{}, fmt.Errorf("eBay client not initialized")
	}
	pageURL := item.ItemWebURL
	if pageURL == "" {
		marketplaceHost := "www.ebay.ca"
		if item.Marketplace == "EBAY_US" {
			marketplaceHost = "www.ebay.com"
		}
		pageURL = fmt.Sprintf("https://%s/itm/%s", marketplaceHost, ExtractItemID(item.ItemID))
	}
	if pageURL == "" {
		return couponSnapshot{}, fmt.Errorf("eBay item has no web URL")
	}

	backends := c.couponBackends
	if len(backends) == 0 {
		backends = []string{scrapebackend.BackendHTTP}
	}

	var errs []error
	for _, backend := range backends {
		result := scrapebackend.FetchHTML(ctx, scrapebackend.FetchOptions{
			Backend:         backend,
			URL:             pageURL,
			Timeout:         30 * time.Second,
			ExternalCommand: ebayCouponExternalCommand(),
			PaidCommand:     ebayCouponPaidCommand(),
		})
		if result.Error != "" {
			errs = append(errs, fmt.Errorf("%s: %s", backend, result.Error))
			continue
		}
		if result.BlockSignal != "" {
			errs = append(errs, fmt.Errorf("%s: blocked by %s", backend, result.BlockSignal))
			continue
		}

		coupon := ExtractPageCoupon(result.HTML, basePrice)
		if coupon.DiscountAmount <= 0 {
			continue
		}
		return coupon.snapshot("page:" + backend), nil
	}

	if len(errs) > 0 {
		return couponSnapshot{}, errors.Join(errs...)
	}
	return couponSnapshot{}, nil
}

func ebayCouponExternalCommand() string {
	return firstNonEmptyEnv("EBAY_COUPON_EXTERNAL_STEALTH_COMMAND", "SCRAPELAB_EXTERNAL_STEALTH_COMMAND")
}

func ebayCouponPaidCommand() string {
	return firstNonEmptyEnv("EBAY_COUPON_PAID_TRIAL_COMMAND", "SCRAPELAB_PAID_TRIAL_COMMAND")
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func (c *Client) fetchPage(ctx context.Context, sellerFilter, marketplace, categoryID string, offset int, sinceTime time.Time) ([]BrowseAPIItem, bool, error) {
	start := time.Now()
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get eBay token: %w", err)
	}

	params := buildBrowseQueryParams(categoryID, sellerFilter, offset, sinceTime)
	reqURL := ebayBrowseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create search request: %w", err)
	}

	setBrowseHeaders(req, token, marketplace)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, fmt.Errorf("failed to read search response: %w", err)
	}

	// Handle 401 by refreshing token and retrying once
	if resp.StatusCode == http.StatusUnauthorized {
		slog.Warn("eBay API returned 401, refreshing token and retrying")
		c.invalidateToken()

		token, err = c.getToken(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("failed to refresh eBay token: %w", err)
		}

		req, err = http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create retry request: %w", err)
		}
		setBrowseHeaders(req, token, marketplace)

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, false, fmt.Errorf("retry search request failed: %w", err)
		}
		defer resp.Body.Close()

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read retry response: %w", err)
		}
	}

	// Handle 429 rate limiting with a single retry after backoff
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := 5 * time.Second
		if ra := resp.Header.Get("Retry-After"); ra != "" {
			var secs int
			if _, parseErr := fmt.Sscanf(ra, "%d", &secs); parseErr == nil && secs > 0 {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
		slog.Warn("eBay Browse API rate limited (429), retrying after backoff",
			"marketplace", marketplace,
			"category_id", categoryID,
			"retry_after", retryAfter,
		)
		select {
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-time.After(retryAfter):
		}

		req, err = http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		if err != nil {
			return nil, false, fmt.Errorf("failed to create 429 retry request: %w", err)
		}
		token, err = c.getToken(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("failed to get token for 429 retry: %w", err)
		}
		setBrowseHeaders(req, token, marketplace)

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, false, fmt.Errorf("429 retry request failed: %w", err)
		}
		defer resp.Body.Close()

		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return nil, false, fmt.Errorf("failed to read 429 retry response: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		slog.Error("eBay Browse API error",
			"status", resp.StatusCode,
			"body", string(body),
			"marketplace", marketplace,
			"category_id", categoryID,
			"offset", offset,
		)
		return nil, false, fmt.Errorf("eBay Browse API error: HTTP %d", resp.StatusCode)
	}

	var searchResp BrowseSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, false, fmt.Errorf("failed to parse search response: %w", err)
	}
	for i := range searchResp.ItemSummaries {
		searchResp.ItemSummaries[i].Marketplace = marketplace
	}

	slog.Info("eBay Browse API page fetched",
		"processor", "ebay",
		"marketplace", marketplace,
		"category_id", categoryID,
		"items_returned", len(searchResp.ItemSummaries),
		"total", searchResp.Total,
		"offset", offset,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	hasMore := searchResp.Next != ""
	return searchResp.ItemSummaries, hasMore, nil
}

// ExtractItemID extracts the numeric item ID from the eBay API's itemId field.
// The API returns IDs like "v1|256783235565|0" — we extract the middle part.
func ExtractItemID(apiItemID string) string {
	parts := strings.Split(apiItemID, "|")
	if len(parts) >= 2 {
		return parts[1]
	}
	return apiItemID
}
