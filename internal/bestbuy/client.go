package bestbuy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	searchBaseURL = "https://www.bestbuy.ca/api/v2/json/search"
	bestBuyBase   = "https://www.bestbuy.ca"
	defaultRegion = "SK"
	pageSize      = 48
	maxPages      = 50 // safety cap to avoid runaway pagination
)

// searchResponse is the top-level JSON returned by the Best Buy search API.
type searchResponse struct {
	CurrentPage int              `json:"currentPage"`
	Total       int              `json:"total"`
	TotalPages  int              `json:"totalPages"`
	PageSize    int              `json:"pageSize"`
	Products    []apiProduct     `json:"products"`
}

// apiProduct maps the fields we care about from the search API product JSON.
type apiProduct struct {
	SKU            string  `json:"sku"`
	Name           string  `json:"name"`
	ProductURL     string  `json:"productUrl"`
	ThumbnailImage string  `json:"thumbnailImage"`
	RegularPrice   float64 `json:"regularPrice"`
	SalePrice      float64 `json:"salePrice"`
	SaleEndDate    string  `json:"saleEndDate"`
	CategoryName   string  `json:"categoryName"`
	SellerID       string  `json:"sellerId"`
	Seller         string  `json:"seller"`
	CustomerRating float64 `json:"customerRating"`
	IsMarketplace  bool    `json:"isMarketplace"`
	IsClearance    bool    `json:"isClearance"`
}

// Client handles HTTP requests to the Best Buy Canada search API.
type Client struct {
	httpClient *http.Client
	region     string
}

// NewClient creates a new Best Buy API client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		region:     defaultRegion,
	}
}

// FetchSellerProducts fetches all products for a given marketplace seller.
func (c *Client) FetchSellerProducts(ctx context.Context, seller Seller) ([]Product, error) {
	params := url.Values{
		"lang":          {"en-CA"},
		"pageSize":      {fmt.Sprintf("%d", pageSize)},
		"currentRegion": {c.region},
		"sortBy":        {"relevance"},
		"sortDir":       {"desc"},
		"path":          {"sellerName:" + seller.Name},
	}

	products, err := c.fetchAllPages(ctx, params, "marketplace")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch seller %s products: %w", seller.Name, err)
	}

	// Tag the seller ID since the API might not always return it
	for i := range products {
		if products[i].SellerID == "" {
			products[i].SellerID = seller.ID
		}
		if products[i].SellerName == "" {
			products[i].SellerName = seller.Name
		}
	}

	return products, nil
}

// FetchOpenBoxProducts fetches Geek Squad Certified Open Box products via keyword search.
func (c *Client) FetchOpenBoxProducts(ctx context.Context) ([]Product, error) {
	params := url.Values{
		"lang":          {"en-CA"},
		"pageSize":      {fmt.Sprintf("%d", pageSize)},
		"currentRegion": {c.region},
		"sortBy":        {"relevance"},
		"sortDir":       {"desc"},
		"query":         {"geek squad open box"},
	}

	return c.fetchAllPages(ctx, params, "openbox")
}

// fetchAllPages paginates through all pages of a search query and returns combined products.
func (c *Client) fetchAllPages(ctx context.Context, baseParams url.Values, source string) ([]Product, error) {
	var allProducts []Product

	for page := 1; page <= maxPages; page++ {
		if ctx.Err() != nil {
			return allProducts, ctx.Err()
		}

		// Delay between page fetches to be respectful
		if page > 1 {
			select {
			case <-ctx.Done():
				return allProducts, ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
		}

		params := url.Values{}
		for k, v := range baseParams {
			params[k] = v
		}
		params.Set("page", fmt.Sprintf("%d", page))

		resp, err := c.doSearch(ctx, params)
		if err != nil {
			return allProducts, fmt.Errorf("page %d: %w", page, err)
		}

		products := convertProducts(resp.Products, source)
		allProducts = append(allProducts, products...)

		slog.Debug("Best Buy API page fetched",
			"processor", "bestbuy",
			"source", source,
			"page", page,
			"totalPages", resp.TotalPages,
			"pageProducts", len(resp.Products),
			"totalProducts", resp.Total,
		)

		if page >= resp.TotalPages {
			break
		}
	}

	return allProducts, nil
}

// doSearch performs a single search API request.
func (c *Client) doSearch(ctx context.Context, params url.Values) (*searchResponse, error) {
	reqURL := searchBaseURL + "?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(body))
	}

	var result searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

// convertProducts transforms API products into our domain Product type.
func convertProducts(apiProducts []apiProduct, source string) []Product {
	products := make([]Product, 0, len(apiProducts))
	for _, ap := range apiProducts {
		productURL := ap.ProductURL
		if productURL != "" && !strings.HasPrefix(productURL, "http") {
			productURL = bestBuyBase + productURL
		}

		imageURL := ap.ThumbnailImage
		if imageURL != "" && !strings.HasPrefix(imageURL, "http") {
			imageURL = "https:" + imageURL
		}

		isOpenBox := strings.HasPrefix(ap.Name, "Open Box") ||
			strings.Contains(strings.ToLower(ap.Name), "geek squad")

		products = append(products, Product{
			SKU:            ap.SKU,
			Name:           ap.Name,
			URL:            productURL,
			ImageURL:       imageURL,
			RegularPrice:   ap.RegularPrice,
			SalePrice:      ap.SalePrice,
			SaleEndDate:    ap.SaleEndDate,
			CategoryName:   ap.CategoryName,
			SellerID:       ap.SellerID,
			SellerName:     ap.Seller,
			CustomerRating: ap.CustomerRating,
			IsMarketplace:  ap.IsMarketplace,
			IsClearance:    ap.IsClearance,
			IsOpenBox:      isOpenBox,
			Source:          source,
		})
	}
	return products
}
