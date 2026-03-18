package ebay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// eBay Finding API — designed specifically for fetching seller listings.
	ebayFindingAPIBase = "https://svcs.ebay.com/services/search/FindingService/v1"
	ebayGlobalID       = "EBAY-ENCA" // Canadian marketplace

	// Maximum items per page from the Finding API (hard limit is 100).
	findingPageLimit = 100
)

// Client handles eBay Finding API interactions.
type Client struct {
	appID      string
	httpClient *http.Client
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
		appID:      clientID,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// findingPrice is the price object in the Finding API JSON response.
type findingPrice struct {
	CurrencyID string `json:"@currencyId"`
	Value      string `json:"__value__"`
}

// findingItem is a single listing from the Finding API JSON response.
type findingItem struct {
	ItemID      []string `json:"itemId"`
	Title       []string `json:"title"`
	ViewItemURL []string `json:"viewItemURL"`
	GalleryURL  []string `json:"galleryURL"`
	Condition   []struct {
		ConditionDisplayName []string `json:"conditionDisplayName"`
	} `json:"condition"`
	PrimaryCategory []struct {
		CategoryID []string `json:"categoryId"`
	} `json:"primaryCategory"`
	ListingInfo []struct {
		StartTime []string `json:"startTime"`
	} `json:"listingInfo"`
	SellingStatus []struct {
		CurrentPrice []findingPrice `json:"currentPrice"`
	} `json:"sellingStatus"`
	SellerInfo []struct {
		SellerUserName []string `json:"sellerUserName"`
	} `json:"sellerInfo"`
}

// findingResponse is the top-level Finding API JSON response.
type findingResponse struct {
	FindItemsBySellerResponse []struct {
		Ack          []string `json:"ack"`
		SearchResult []struct {
			Count string        `json:"@count"`
			Items []findingItem `json:"item"`
		} `json:"searchResult"`
		PaginationOutput []struct {
			TotalPages []string `json:"totalPages"`
		} `json:"paginationOutput"`
		ErrorMessage []struct {
			Error []struct {
				Message []string `json:"message"`
			} `json:"error"`
		} `json:"errorMessage"`
	} `json:"findItemsBySellerResponse"`
}

// SearchSellerListings fetches all Buy It Now listings from the given sellers
// using the eBay Finding API. Queries each seller individually with pagination.
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

// fetchSellerListings fetches all BIN listings for a single seller, paginating as needed.
func (c *Client) fetchSellerListings(ctx context.Context, seller string) ([]BrowseAPIItem, error) {
	var allItems []BrowseAPIItem
	page := 1
	for {
		items, totalPages, err := c.fetchSellerPage(ctx, seller, page)
		if err != nil {
			return allItems, err
		}
		allItems = append(allItems, items...)
		if page >= totalPages || len(items) == 0 {
			break
		}
		page++
	}
	slog.Info("Fetched eBay seller listings", "seller", seller, "total_items", len(allItems))
	return allItems, nil
}

// fetchSellerPage fetches one page of BIN listings for a seller from the Finding API.
func (c *Client) fetchSellerPage(ctx context.Context, seller string, page int) ([]BrowseAPIItem, int, error) {
	params := url.Values{
		"OPERATION-NAME":                 {"findItemsBySeller"},
		"SERVICE-VERSION":                {"1.0.0"},
		"SECURITY-APPNAME":               {c.appID},
		"RESPONSE-DATA-FORMAT":           {"JSON"},
		"GLOBAL-ID":                      {ebayGlobalID},
		"sellerID":                       {seller},
		"itemFilter(0).name":             {"ListingType"},
		"itemFilter(0).value":            {"FixedPrice"},
		"outputSelector":                 {"SellerInfo"},
		"sortOrder":                      {"StartTimeNewest"},
		"paginationInput.entriesPerPage": {fmt.Sprintf("%d", findingPageLimit)},
		"paginationInput.pageNumber":     {fmt.Sprintf("%d", page)},
	}

	reqURL := ebayFindingAPIBase + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create Finding API request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("Finding API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read Finding API response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("eBay Finding API HTTP %d: %s", resp.StatusCode, string(body))
	}

	var findResp findingResponse
	if err := json.Unmarshal(body, &findResp); err != nil {
		return nil, 0, fmt.Errorf("failed to parse Finding API response: %w", err)
	}

	if len(findResp.FindItemsBySellerResponse) == 0 {
		return nil, 0, nil
	}

	sellerResp := findResp.FindItemsBySellerResponse[0]

	// Check for API-level errors
	if len(sellerResp.Ack) > 0 && sellerResp.Ack[0] != "Success" && sellerResp.Ack[0] != "Warning" {
		errMsg := sellerResp.Ack[0]
		if len(sellerResp.ErrorMessage) > 0 && len(sellerResp.ErrorMessage[0].Error) > 0 &&
			len(sellerResp.ErrorMessage[0].Error[0].Message) > 0 {
			errMsg = sellerResp.ErrorMessage[0].Error[0].Message[0]
		}
		return nil, 0, fmt.Errorf("Finding API error for seller %q: %s", seller, errMsg)
	}

	// Parse total pages
	totalPages := 1
	if len(sellerResp.PaginationOutput) > 0 && len(sellerResp.PaginationOutput[0].TotalPages) > 0 {
		if tp, err := strconv.Atoi(sellerResp.PaginationOutput[0].TotalPages[0]); err == nil {
			totalPages = tp
		}
	}

	// Map Finding API items to BrowseAPIItem
	var items []BrowseAPIItem
	if len(sellerResp.SearchResult) > 0 {
		for _, fi := range sellerResp.SearchResult[0].Items {
			items = append(items, mapFindingItem(fi, seller))
		}
	}

	slog.Info("eBay Finding API page fetched",
		"seller", seller,
		"page", page,
		"total_pages", totalPages,
		"items_returned", len(items),
	)

	return items, totalPages, nil
}

// mapFindingItem converts a Finding API item to the shared BrowseAPIItem format.
func mapFindingItem(fi findingItem, sellerUsername string) BrowseAPIItem {
	item := BrowseAPIItem{}

	if len(fi.ItemID) > 0 {
		item.ItemID = fi.ItemID[0]
	}
	if len(fi.Title) > 0 {
		item.Title = fi.Title[0]
	}
	if len(fi.ViewItemURL) > 0 {
		item.ItemWebURL = fi.ViewItemURL[0]
	}
	if len(fi.GalleryURL) > 0 {
		item.Image = &Image{ImageURL: fi.GalleryURL[0]}
	}
	if len(fi.Condition) > 0 && len(fi.Condition[0].ConditionDisplayName) > 0 {
		item.Condition = fi.Condition[0].ConditionDisplayName[0]
	}
	if len(fi.PrimaryCategory) > 0 && len(fi.PrimaryCategory[0].CategoryID) > 0 {
		item.CategoryID = fi.PrimaryCategory[0].CategoryID[0]
	}
	if len(fi.ListingInfo) > 0 && len(fi.ListingInfo[0].StartTime) > 0 {
		item.ItemCreationDate = fi.ListingInfo[0].StartTime[0]
	}
	if len(fi.SellingStatus) > 0 && len(fi.SellingStatus[0].CurrentPrice) > 0 {
		p := fi.SellingStatus[0].CurrentPrice[0]
		item.Price = &Price{Value: p.Value, Currency: p.CurrencyID}
	}

	// Prefer the username returned by the API; fall back to the one we queried with.
	username := sellerUsername
	if len(fi.SellerInfo) > 0 && len(fi.SellerInfo[0].SellerUserName) > 0 {
		username = fi.SellerInfo[0].SellerUserName[0]
	}
	item.Seller = &SellerInfo{Username: username}

	return item
}

// ExtractItemID extracts the numeric item ID from an eBay item ID string.
// The Browse API returns IDs like "v1|256783235565|0" — we extract the middle part.
// The Finding API returns plain numeric IDs directly.
func ExtractItemID(apiItemID string) string {
	parts := strings.Split(apiItemID, "|")
	if len(parts) >= 2 {
		return parts[1]
	}
	return apiItemID
}
