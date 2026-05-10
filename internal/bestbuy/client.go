package bestbuy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const (
	BackendAlgolia = "bestbuy-algolia"

	searchBaseURL = "https://www.bestbuy.ca/api/v2/json/search"
	bestBuyBase   = "https://www.bestbuy.ca"
	defaultRegion = "SK"
	pageSize      = 48
	maxPages      = 50 // safety cap to avoid runaway pagination

	defaultAlgoliaAppID   = "NSQ22WGR1E"
	defaultAlgoliaAPIKey  = "e24267dce0d612e5641fdc4949dd9c7c"
	defaultAlgoliaIndexEN = "prod_products_en"

	algoliaIndexTimestampMinParam = "_indexTimestampMin"
	recentIndexSweepWindow        = 48 * time.Hour
)

// searchResponse is the top-level JSON returned by the Best Buy search API.
type searchResponse struct {
	CurrentPage int          `json:"currentPage"`
	Total       int          `json:"total"`
	TotalPages  int          `json:"totalPages"`
	PageSize    int          `json:"pageSize"`
	Products    []apiProduct `json:"products"`
}

// apiProduct maps the fields we care about from the search API product JSON.
type apiProduct struct {
	SKU             string  `json:"sku"`
	Name            string  `json:"name"`
	ProductURL      string  `json:"productUrl"`
	ThumbnailImage  string  `json:"thumbnailImage"`
	RegularPrice    float64 `json:"regularPrice"`
	SalePrice       float64 `json:"salePrice"`
	SaleEndDate     string  `json:"saleEndDate"`
	CategoryName    string  `json:"categoryName"`
	SellerID        string  `json:"sellerId"`
	Seller          string  `json:"seller"`
	CustomerRating  float64 `json:"customerRating"`
	IsMarketplace   bool    `json:"isMarketplace"`
	IsClearance     bool    `json:"isClearance"`
	LastIndex       string  `json:"lastIndex"`
	IndexTimestamp  int64   `json:"indexTimestamp"`
	SearchStartDate int64   `json:"searchStartDate"`
}

type algoliaResponse struct {
	Page    int          `json:"page"`
	NbHits  int          `json:"nbHits"`
	NbPages int          `json:"nbPages"`
	Hits    []algoliaHit `json:"hits"`
}

type algoliaHit struct {
	ObjectID          string `json:"objectID"`
	SKU               string `json:"sku"`
	Title             string `json:"title"`
	ImageURL          string `json:"imageUrl"`
	HighResImageURL   string `json:"highResImageUrl"`
	CategoryName      string `json:"categoryName"`
	SeoText           string `json:"seoText"`
	Clearance         bool   `json:"clearance"`
	InStock           bool   `json:"inStock"`
	LastIndex         string `json:"lastIndex"`
	IndexTimestamp    int64  `json:"indexTimestamp"`
	SearchStartDate   int64  `json:"searchStartDate"`
	PreorderStartDate string `json:"preorderStartDate"`
	Seller            struct {
		SellerID    string `json:"sellerId"`
		SellerName  string `json:"sellerName"`
		Marketplace bool   `json:"marketplace"`
	} `json:"seller"`
	Price struct {
		RegularPrice float64 `json:"regularPrice"`
		CurrentPrice float64 `json:"currentPrice"`
	} `json:"price"`
	Rating struct {
		CustomerRating float64 `json:"customerRating"`
	} `json:"rating"`
}

// Client handles HTTP requests to the Best Buy Canada search API.
type Client struct {
	httpClient *http.Client
	region     string
	backends   []string
}

// NewClient creates a new Best Buy API client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		region:     defaultRegion,
		backends:   []string{scrapebackend.BackendHTTP},
	}
}

// SetBackends configures the ordered backend fallback list for Best Buy fetches.
func (c *Client) SetBackends(backends []string) {
	if c == nil || len(backends) == 0 {
		return
	}
	c.backends = append([]string(nil), backends...)
}

// FetchSellerProducts fetches all products for a given marketplace seller.
func (c *Client) FetchSellerProducts(ctx context.Context, seller Seller) ([]Product, error) {
	params := c.sellerSearchParams(seller)

	products, err := c.fetchAllPages(ctx, params, "marketplace")
	if err != nil {
		return nil, fmt.Errorf("failed to fetch seller %s products: %w", seller.Name, err)
	}
	c.tagSellerProducts(products, seller)

	if c.hasBackend(BackendAlgolia) {
		recent, err := c.fetchRecentlyIndexedSellerProducts(ctx, params, seller)
		if err != nil {
			slog.Warn("Best Buy recent index sweep failed",
				"processor", "bestbuy",
				"seller", seller.Name,
				"sellerID", seller.ID,
				"error", err,
			)
		} else if len(recent) > 0 {
			c.tagSellerProducts(recent, seller)
			products = mergeSellerProducts(products, recent)
		}
	}

	return products, nil
}

func (c *Client) tagSellerProducts(products []Product, seller Seller) {
	for i := range products {
		if products[i].SellerID == "" {
			products[i].SellerID = seller.ID
		}
		if products[i].SellerName == "" {
			products[i].SellerName = seller.Name
		}
		if seller.ID != "" {
			products[i].Source = "seller:" + seller.ID
		}
	}
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

func (c *Client) sellerSearchParams(seller Seller) url.Values {
	searchPath := seller.SearchPath
	if searchPath == "" {
		searchPath = "sellerName:" + seller.Name
	}
	return url.Values{
		"lang":          {"en-CA"},
		"pageSize":      {fmt.Sprintf("%d", pageSize)},
		"currentRegion": {c.region},
		"sortBy":        {"relevance"},
		"sortDir":       {"desc"},
		"path":          {searchPath},
	}
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

func (c *Client) fetchRecentlyIndexedSellerProducts(ctx context.Context, baseParams url.Values, seller Seller) ([]Product, error) {
	params := url.Values{}
	for k, v := range baseParams {
		params[k] = append([]string(nil), v...)
	}
	params.Set(algoliaIndexTimestampMinParam, strconv.FormatInt(time.Now().Add(-recentIndexSweepWindow).UnixMilli(), 10))

	products, err := c.fetchAlgoliaPages(ctx, params, "marketplace")
	if err != nil {
		return nil, err
	}
	if len(products) > 0 {
		slog.Info("Best Buy recent index sweep fetched products",
			"processor", "bestbuy",
			"seller", seller.Name,
			"count", len(products),
			"window", recentIndexSweepWindow.String(),
		)
	}
	return products, nil
}

func (c *Client) fetchAlgoliaPages(ctx context.Context, baseParams url.Values, source string) ([]Product, error) {
	var allProducts []Product

	for page := 1; page <= maxPages; page++ {
		if ctx.Err() != nil {
			return allProducts, ctx.Err()
		}
		if page > 1 {
			select {
			case <-ctx.Done():
				return allProducts, ctx.Err()
			case <-time.After(1500 * time.Millisecond):
			}
		}

		params := url.Values{}
		for k, v := range baseParams {
			params[k] = append([]string(nil), v...)
		}
		params.Set("page", fmt.Sprintf("%d", page))

		resp, err := c.doAlgoliaSearch(ctx, params)
		if err != nil {
			return allProducts, fmt.Errorf("page %d: %w", page, err)
		}
		allProducts = append(allProducts, convertProducts(resp.Products, source)...)
		if page >= resp.TotalPages {
			break
		}
	}

	return allProducts, nil
}

// doSearch performs a single search API request.
func (c *Client) doSearch(ctx context.Context, params url.Values) (*searchResponse, error) {
	reqURL := searchBaseURL + "?" + params.Encode()
	backends := c.backends
	if len(backends) == 0 {
		backends = []string{scrapebackend.BackendHTTP}
	}

	var failures []string
	for _, backend := range backends {
		var result *searchResponse
		var err error
		if backend == BackendAlgolia {
			result, err = c.doAlgoliaSearch(ctx, params)
		} else if backend == scrapebackend.BackendHTTP {
			result, err = c.doHTTPSearch(ctx, reqURL)
		} else {
			result, err = c.doBackendSearch(ctx, backend, reqURL)
		}
		if err == nil {
			return result, nil
		}
		failures = append(failures, fmt.Sprintf("%s: %s", backend, err))
	}

	return nil, fmt.Errorf("all Best Buy backends failed: %s", strings.Join(failures, "; "))
}

func (c *Client) doHTTPSearch(ctx context.Context, reqURL string) (*searchResponse, error) {
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

func (c *Client) doAlgoliaSearch(ctx context.Context, params url.Values) (*searchResponse, error) {
	algoliaParams := url.Values{}
	algoliaParams.Set("query", params.Get("query"))
	algoliaParams.Set("hitsPerPage", firstNonEmpty(params.Get("pageSize"), fmt.Sprintf("%d", pageSize)))

	page := 1
	if rawPage := params.Get("page"); rawPage != "" {
		parsed, err := strconv.Atoi(rawPage)
		if err != nil {
			return nil, fmt.Errorf("invalid page %q: %w", rawPage, err)
		}
		page = parsed
	}
	if page < 1 {
		page = 1
	}
	algoliaParams.Set("page", fmt.Sprintf("%d", page-1))

	if facet := algoliaFacetFilterFromPath(params.Get("path")); facet != "" {
		encodedFacet, err := json.Marshal([]string{facet})
		if err != nil {
			return nil, err
		}
		algoliaParams.Set("facetFilters", string(encodedFacet))
	}
	if minIndexTimestamp := strings.TrimSpace(params.Get(algoliaIndexTimestampMinParam)); minIndexTimestamp != "" {
		algoliaParams.Set("filters", "indexTimestamp >= "+minIndexTimestamp)
	}

	body, err := json.Marshal(map[string]string{"params": algoliaParams.Encode()})
	if err != nil {
		return nil, err
	}

	appID := firstNonEmpty(os.Getenv("BESTBUY_ALGOLIA_APP_ID"), defaultAlgoliaAppID)
	apiKey := firstNonEmpty(os.Getenv("BESTBUY_ALGOLIA_API_KEY"), defaultAlgoliaAPIKey)
	indexName := firstNonEmpty(os.Getenv("BESTBUY_ALGOLIA_INDEX_NAME"), defaultAlgoliaIndexEN)
	reqURL := fmt.Sprintf("https://%s-dsn.algolia.net/1/indexes/%s/query", appID, url.PathEscape(indexName))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Algolia-Application-Id", appID)
	req.Header.Set("X-Algolia-API-Key", apiKey)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Algolia request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("Algolia status %d: %s", resp.StatusCode, string(body))
	}

	var decoded algoliaResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Algolia response: %w", err)
	}
	return algoliaSearchResponse(decoded), nil
}

func algoliaFacetFilterFromPath(path string) string {
	path = strings.TrimSpace(path)
	switch {
	case path == "":
		return ""
	case strings.HasPrefix(path, "seller.sellerName:"):
		return path
	case strings.HasPrefix(path, "sellerName:"):
		return "seller.sellerName:" + strings.TrimSpace(strings.TrimPrefix(path, "sellerName:"))
	case strings.HasPrefix(path, "seller.sellerId:"):
		return path
	case strings.HasPrefix(path, "sellerId:"):
		return "seller.sellerId:" + strings.TrimSpace(strings.TrimPrefix(path, "sellerId:"))
	default:
		return path
	}
}

func algoliaSearchResponse(resp algoliaResponse) *searchResponse {
	products := make([]apiProduct, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		sku := firstNonEmpty(hit.SKU, hit.ObjectID)
		currentPrice := hit.Price.CurrentPrice
		regularPrice := hit.Price.RegularPrice
		if currentPrice == 0 {
			currentPrice = regularPrice
		}
		if regularPrice == 0 {
			regularPrice = currentPrice
		}

		imageURL := firstNonEmpty(hit.ImageURL, hit.HighResImageURL)
		products = append(products, apiProduct{
			SKU:             sku,
			Name:            hit.Title,
			ProductURL:      bestBuyProductURL(hit, sku),
			ThumbnailImage:  imageURL,
			RegularPrice:    regularPrice,
			SalePrice:       currentPrice,
			CategoryName:    hit.CategoryName,
			SellerID:        hit.Seller.SellerID,
			Seller:          hit.Seller.SellerName,
			CustomerRating:  hit.Rating.CustomerRating,
			IsMarketplace:   hit.Seller.Marketplace,
			IsClearance:     hit.Clearance,
			LastIndex:       hit.LastIndex,
			IndexTimestamp:  hit.IndexTimestamp,
			SearchStartDate: hit.SearchStartDate,
		})
	}

	return &searchResponse{
		CurrentPage: resp.Page + 1,
		Total:       resp.NbHits,
		TotalPages:  resp.NbPages,
		PageSize:    len(products),
		Products:    products,
	}
}

func (c *Client) hasBackend(backend string) bool {
	for _, candidate := range c.backends {
		if candidate == backend {
			return true
		}
	}
	return false
}

func mergeSellerProducts(products, recent []Product) []Product {
	merged := make([]Product, 0, len(products)+len(recent))
	index := make(map[string]int, len(products)+len(recent))
	add := func(product Product) {
		key := product.SKU + "|" + product.Source
		if key == "|" {
			key = product.Name + "|" + product.URL
		}
		if existingIndex, ok := index[key]; ok {
			merged[existingIndex] = mergeSellerProduct(merged[existingIndex], product)
			return
		}
		index[key] = len(merged)
		merged = append(merged, product)
	}
	for _, product := range products {
		add(product)
	}
	for _, product := range recent {
		add(product)
	}
	return merged
}

func mergeSellerProduct(existing, incoming Product) Product {
	merged := existing
	if incoming.RegularPrice > 0 {
		merged.RegularPrice = incoming.RegularPrice
	}
	if incoming.SalePrice > 0 {
		merged.SalePrice = incoming.SalePrice
	}
	if merged.Name == "" {
		merged.Name = incoming.Name
	}
	if merged.URL == "" {
		merged.URL = incoming.URL
	}
	if merged.ImageURL == "" {
		merged.ImageURL = incoming.ImageURL
	}
	if merged.CategoryName == "" {
		merged.CategoryName = incoming.CategoryName
	}
	if merged.SellerID == "" {
		merged.SellerID = incoming.SellerID
	}
	if merged.SellerName == "" {
		merged.SellerName = incoming.SellerName
	}
	if incoming.CustomerRating > 0 {
		merged.CustomerRating = incoming.CustomerRating
	}
	merged.IsMarketplace = merged.IsMarketplace || incoming.IsMarketplace
	merged.IsClearance = merged.IsClearance || incoming.IsClearance
	merged.IsOpenBox = merged.IsOpenBox || incoming.IsOpenBox
	if incoming.LastIndex != "" {
		merged.LastIndex = incoming.LastIndex
	}
	if incoming.IndexTimestamp > 0 {
		merged.IndexTimestamp = incoming.IndexTimestamp
	}
	if incoming.SearchStartDate > 0 {
		merged.SearchStartDate = incoming.SearchStartDate
	}
	return merged
}

func bestBuyProductURL(hit algoliaHit, sku string) string {
	if sku == "" {
		return ""
	}
	if hit.SeoText != "" {
		return bestBuyBase + "/en-ca/product/" + url.PathEscape(hit.SeoText) + "/" + url.PathEscape(sku)
	}
	return bestBuyBase + "/en-ca/product/" + url.PathEscape(sku)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (c *Client) doBackendSearch(ctx context.Context, backend, reqURL string) (*searchResponse, error) {
	result := scrapebackend.FetchHTML(ctx, scrapebackend.FetchOptions{
		Backend:         backend,
		URL:             reqURL,
		Timeout:         45 * time.Second,
		ExternalCommand: os.Getenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND"),
		PaidCommand:     os.Getenv("SCRAPELAB_PAID_TRIAL_COMMAND"),
	})
	if result.Error != "" {
		return nil, fmt.Errorf("%s", result.Error)
	}
	if result.BlockSignal != "" {
		return nil, fmt.Errorf("blocked by %s", result.BlockSignal)
	}

	payload := extractJSONPayload(result.HTML)
	if payload == "" {
		return nil, fmt.Errorf("backend returned no JSON payload")
	}
	var decoded searchResponse
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		return nil, fmt.Errorf("failed to decode backend response: %w", err)
	}
	return &decoded, nil
}

func extractJSONPayload(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}
	start := strings.Index(trimmed, "{")
	end := strings.LastIndex(trimmed, "}")
	if start >= 0 && end > start {
		return trimmed[start : end+1]
	}
	return ""
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
			SKU:             ap.SKU,
			Name:            ap.Name,
			URL:             productURL,
			ImageURL:        imageURL,
			RegularPrice:    ap.RegularPrice,
			SalePrice:       ap.SalePrice,
			SaleEndDate:     ap.SaleEndDate,
			CategoryName:    ap.CategoryName,
			SellerID:        ap.SellerID,
			SellerName:      ap.Seller,
			CustomerRating:  ap.CustomerRating,
			IsMarketplace:   ap.IsMarketplace,
			IsClearance:     ap.IsClearance,
			IsOpenBox:       isOpenBox,
			Source:          source,
			LastIndex:       ap.LastIndex,
			IndexTimestamp:  ap.IndexTimestamp,
			SearchStartDate: ap.SearchStartDate,
		})
	}
	return products
}
