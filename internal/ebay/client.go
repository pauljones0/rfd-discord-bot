package ebay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	ebayAPIBase   = "https://api.ebay.com"
	ebayTokenURL  = ebayAPIBase + "/identity/v1/oauth2/token"
	ebayBrowseURL = ebayAPIBase + "/buy/browse/v1/item_summary/search"
	ebayScope     = "https://api.ebay.com/oauth/api_scope"

	// browsePageLimit is the maximum items per page from the Browse API.
	browsePageLimit = 200

	// browseMaxPages caps pagination per marketplace group (eBay Browse API max offset is 9,800).
	browseMaxPages = 50 // 50 × 200 = 10,000 items per marketplace group
)

// Client handles eBay OAuth and Browse API interactions.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
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
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient:   &http.Client{Timeout: 30 * time.Second},
	}
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

	// Group sellers by marketplace for combined API queries.
	type mktGroup struct {
		marketplace string
		usernames   []string
	}
	seen := make(map[string]int) // marketplace -> index in groups
	var groups []mktGroup
	for _, s := range sellers {
		mkt := s.MarketplaceID()
		if idx, ok := seen[mkt]; ok {
			groups[idx].usernames = append(groups[idx].usernames, s.Username)
		} else {
			seen[mkt] = len(groups)
			groups = append(groups, mktGroup{marketplace: mkt, usernames: []string{s.Username}})
		}
	}

	var allItems []BrowseAPIItem
	for i, g := range groups {
		if i > 0 {
			select {
			case <-ctx.Done():
				return allItems, ctx.Err()
			case <-time.After(1 * time.Second):
			}
		}
		items, err := c.fetchMarketplaceListings(ctx, g.usernames, g.marketplace, sinceTime)
		if err != nil {
			slog.Warn("Failed to fetch eBay marketplace listings",
				"marketplace", g.marketplace,
				"sellers", len(g.usernames),
				"error", err,
			)
			continue
		}
		allItems = append(allItems, items...)
	}

	return allItems, nil
}

// fetchMarketplaceListings fetches all BIN listings for a group of sellers in the
// same marketplace using a single combined query with pagination.
func (c *Client) fetchMarketplaceListings(ctx context.Context, usernames []string, marketplace string, sinceTime time.Time) ([]BrowseAPIItem, error) {
	var allItems []BrowseAPIItem
	offset := 0
	sellerFilter := strings.Join(usernames, "|")

	for page := 0; page < browseMaxPages; page++ {
		items, hasMore, err := c.fetchPage(ctx, sellerFilter, marketplace, offset, sinceTime)
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
			"sellers", len(usernames),
			"pages_fetched", browseMaxPages,
		)
	}

	slog.Info("Fetched eBay marketplace listings",
		"marketplace", marketplace,
		"sellers", len(usernames),
		"total_items", len(allItems),
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
func (c *Client) fetchPage(ctx context.Context, sellerFilter, marketplace string, offset int, sinceTime time.Time) ([]BrowseAPIItem, bool, error) {
	start := time.Now()
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get eBay token: %w", err)
	}

	filterParts := fmt.Sprintf("sellers:{%s},buyingOptions:{FIXED_PRICE}", sellerFilter)
	if !sinceTime.IsZero() {
		filterParts += fmt.Sprintf(",itemStartDate:[%s..]", sinceTime.UTC().Format(time.RFC3339))
	}

	params := url.Values{
		"category_ids": {"0"},
		"filter":       {filterParts},
		"limit":        {fmt.Sprintf("%d", browsePageLimit)},
		"offset":       {fmt.Sprintf("%d", offset)},
	}

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
