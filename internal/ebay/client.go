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
// Queries each seller individually to keep result sets within Browse API limits.
func (c *Client) SearchSellerListings(ctx context.Context, sellers []string) ([]BrowseAPIItem, error) {
	if c == nil {
		return nil, fmt.Errorf("eBay client not initialized")
	}
	if len(sellers) == 0 {
		return nil, nil
	}

	var allItems []BrowseAPIItem
	for _, seller := range sellers {
		items, err := c.fetchSellerListings(ctx, seller)
		if err != nil {
			slog.Warn("Failed to fetch listings for eBay seller", "seller", seller, "error", err)
			continue // skip this seller, don't fail the whole run
		}
		allItems = append(allItems, items...)
	}

	return allItems, nil
}

// fetchSellerListings fetches all BIN listings for a single seller with pagination.
func (c *Client) fetchSellerListings(ctx context.Context, seller string) ([]BrowseAPIItem, error) {
	var allItems []BrowseAPIItem
	offset := 0

	for {
		items, hasMore, err := c.fetchSellerPage(ctx, seller, offset)
		if err != nil {
			return allItems, err
		}
		allItems = append(allItems, items...)
		if !hasMore || len(items) == 0 {
			break
		}
		offset += browsePageLimit
	}

	slog.Info("Fetched eBay seller listings", "seller", seller, "total_items", len(allItems))
	return allItems, nil
}

// fetchSellerPage fetches one page of BIN listings for a single seller from the Browse API.
func (c *Client) fetchSellerPage(ctx context.Context, seller string, offset int) ([]BrowseAPIItem, bool, error) {
	token, err := c.getToken(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get eBay token: %w", err)
	}

	params := url.Values{
		"category_ids": {"0"},
		"filter":       {fmt.Sprintf("sellers:{%s},buyingOptions:{FIXED_PRICE}", seller)},
		"sort":   {"newlyListed"},
		"limit":  {fmt.Sprintf("%d", browsePageLimit)},
		"offset": {fmt.Sprintf("%d", offset)},
	}

	reqURL := ebayBrowseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, false, fmt.Errorf("failed to create search request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-EBAY-C-MARKETPLACE-ID", "EBAY_CA")
	req.Header.Set("Content-Type", "application/json")

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

		req, _ = http.NewRequestWithContext(ctx, "GET", reqURL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-EBAY-C-MARKETPLACE-ID", "EBAY_CA")
		req.Header.Set("Content-Type", "application/json")

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

	if resp.StatusCode != http.StatusOK {
		slog.Error("eBay Browse API error",
			"status", resp.StatusCode,
			"body", string(body),
			"seller", seller,
			"offset", offset,
		)
		return nil, false, fmt.Errorf("eBay Browse API error: HTTP %d", resp.StatusCode)
	}

	var searchResp BrowseSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, false, fmt.Errorf("failed to parse search response: %w", err)
	}

	slog.Info("eBay Browse API page fetched",
		"seller", seller,
		"items_returned", len(searchResp.ItemSummaries),
		"total", searchResp.Total,
		"offset", offset,
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
