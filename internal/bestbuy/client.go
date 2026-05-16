package bestbuy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
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
	offerAPIBaseURL               = "https://www.bestbuy.ca/api/offers/v1/products"
	validationPriceEpsilon        = 0.005
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
	SKU             string         `json:"sku"`
	Name            string         `json:"name"`
	ProductURL      string         `json:"productUrl"`
	ThumbnailImage  string         `json:"thumbnailImage"`
	RegularPrice    float64        `json:"regularPrice"`
	SalePrice       float64        `json:"salePrice"`
	SaleEndDate     string         `json:"saleEndDate"`
	CategoryID      string         `json:"categoryId"`
	CategoryName    string         `json:"categoryName"`
	SellerID        string         `json:"sellerId"`
	Seller          string         `json:"seller"`
	CustomerRating  float64        `json:"customerRating"`
	IsMarketplace   bool           `json:"isMarketplace"`
	IsClearance     bool           `json:"isClearance"`
	LastIndex       string         `json:"lastIndex"`
	IndexTimestamp  int64          `json:"indexTimestamp"`
	SearchStartDate int64          `json:"searchStartDate"`
	SearchEndDate   int64          `json:"searchEndDate"`
	InStock         *bool          `json:"inStock"`
	IsVisible       *bool          `json:"isVisible"`
	OnlineOnly      bool           `json:"onlineOnly"`
	InStoreOnly     bool           `json:"inStoreOnly"`
	IsOnSale        bool           `json:"isOnSale"`
	Advertised      bool           `json:"advertised"`
	BrandName       string         `json:"brandName"`
	ModelNumber     string         `json:"modelNumber"`
	PrimaryUPC      string         `json:"primaryUPC"`
	Specs           map[string]any `json:"specs"`
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
	CategoryID        string `json:"categoryId"`
	CategoryName      string `json:"categoryName"`
	BrandName         string `json:"brandName"`
	ModelNumber       string `json:"modelNumber"`
	PrimaryUPC        string `json:"primaryUPC"`
	SeoText           string `json:"seoText"`
	Clearance         bool   `json:"clearance"`
	InStock           *bool  `json:"inStock"`
	IsVisible         *bool  `json:"isVisible"`
	OnlineOnly        bool   `json:"onlineOnly"`
	InStoreOnly       bool   `json:"inStoreOnly"`
	IsOnSale          bool   `json:"isOnSale"`
	Advertised        bool   `json:"advertised"`
	LastIndex         string `json:"lastIndex"`
	IndexTimestamp    int64  `json:"indexTimestamp"`
	SearchStartDate   int64  `json:"searchStartDate"`
	SearchEndDate     int64  `json:"searchEndDate"`
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
	Specs map[string]any `json:"specs"`
}

type offerProduct struct {
	OfferID       string  `json:"offerId"`
	SKU           string  `json:"sku"`
	OfferEndDate  string  `json:"offerEndDate"`
	SellerID      string  `json:"sellerId"`
	SellerNameEn  string  `json:"sellerNameEn"`
	IsWinner      bool    `json:"isWinner"`
	RegularPrice  float64 `json:"regularPrice"`
	SalePrice     float64 `json:"salePrice"`
	IsMarketplace bool    `json:"isMarketplace"`
}

type OfferValidation struct {
	Product Product
	Valid   bool
	Reason  string
}

type ComparableListing struct {
	SKU          string
	Title        string
	SellerID     string
	SellerName   string
	Price        float64
	RegularPrice float64
	Condition    string
	Source       string
}

type ComputeSearchTarget struct {
	Name         string
	Query        string
	FacetFilters string
	Filters      string
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

func (c *Client) FetchComputeProducts(ctx context.Context) ([]Product, error) {
	if !c.hasBackend(BackendAlgolia) {
		return nil, nil
	}
	seen := make(map[string]Product)
	var errs []error
	for _, target := range DefaultComputeSearchTargets() {
		params := url.Values{
			"query":    {target.Query},
			"pageSize": {"1000"},
			"page":     {"1"},
		}
		if target.FacetFilters != "" {
			params.Set("facetFilters", target.FacetFilters)
		}
		if target.Filters != "" {
			params.Set("filters", target.Filters)
		}
		products, err := c.fetchAlgoliaPages(ctx, params, "compute")
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", target.Name, err))
			continue
		}
		slog.Info("Best Buy compute target fetched",
			"processor", "bestbuy_compute",
			"target", target.Name,
			"count", len(products),
		)
		for _, product := range products {
			if product.SellerID != "" {
				product.Source = "seller:" + product.SellerID
			}
			key := product.SKU + "|" + product.Source
			if key == "|" {
				key = product.Name + "|" + product.URL
			}
			if existing, ok := seen[key]; ok {
				seen[key] = mergeSellerProduct(existing, product)
				continue
			}
			seen[key] = product
		}
	}
	out := make([]Product, 0, len(seen))
	for _, product := range seen {
		out = append(out, product)
	}
	sort.Slice(out, func(i, j int) bool {
		return effectiveProductPrice(out[i]) < effectiveProductPrice(out[j])
	})
	return out, errors.Join(errs...)
}

func DefaultComputeSearchTargets() []ComputeSearchTarget {
	ram64 := `["specs.custom0ramsize:64","specs.custom0ramsize:96","specs.custom0ramsize:128","specs.custom0ramsize:192","specs.custom0ramsize:256","specs.custom0ramsize:512","specs.custom0ramsize:1000"]`
	core12 := `["specs.custom0processorcores:12","specs.custom0processorcores:14","specs.custom0processorcores:16","specs.custom0processorcores:20","specs.custom0processorcores:24","specs.custom0processorcores:28","specs.custom0processorcores:32","specs.custom0processorcores:36","specs.custom0processorcores:48","specs.custom0processorcores:64"]`
	return []ComputeSearchTarget{
		{Name: "ram64-all", FacetFilters: "[" + ram64 + "]"},
		{Name: "core12-under-1500", FacetFilters: "[" + core12 + "]", Filters: "price.currentPrice <= 1500"},
		{Name: "core12-1500-2500", FacetFilters: "[" + core12 + "]", Filters: "price.currentPrice > 1500 AND price.currentPrice <= 2500"},
		{Name: "servers-category", FacetFilters: `["categoryIds:26200"]`},
		{Name: "commercial-servers-category", FacetFilters: `["categoryIds:32381"]`},
		{Name: "poweredge-server", Query: "PowerEdge server"},
		{Name: "proliant-server", Query: "ProLiant server"},
		{Name: "thinksystem-server", Query: "ThinkSystem server"},
		{Name: "dell-precision-workstation", Query: "Dell Precision workstation"},
		{Name: "hp-z-workstation", Query: "HP Z workstation"},
		{Name: "lenovo-thinkstation", Query: "Lenovo ThinkStation"},
		{Name: "xeon-workstation", Query: "Xeon workstation"},
		{Name: "xeon-desktop", Query: "Xeon desktop"},
		{Name: "threadripper-workstation", Query: "Threadripper workstation"},
		{Name: "quadro-workstation", Query: "Quadro workstation"},
		{Name: "rtx-a-workstation", Query: "RTX A4000 workstation"},
		{Name: "mac-studio", Query: "Mac Studio"},
		{Name: "macbook-pro-64gb", Query: "MacBook Pro 64GB"},
		{Name: "snapdragon-x-elite-laptop", Query: "Snapdragon X Elite laptop"},
		{Name: "laptop-64gb-ram", Query: "laptop 64GB RAM"},
		{Name: "laptop-128gb-ram", Query: "laptop 128GB RAM"},
	}
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
	decoded, err := c.doRawAlgoliaSearch(ctx, params)
	if err != nil {
		return nil, err
	}
	return algoliaSearchResponse(*decoded), nil
}

func (c *Client) doRawAlgoliaSearch(ctx context.Context, params url.Values) (*algoliaResponse, error) {
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
	if facetFilters := strings.TrimSpace(params.Get("facetFilters")); facetFilters != "" {
		algoliaParams.Set("facetFilters", facetFilters)
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
	return &decoded, nil
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
			CategoryID:      hit.CategoryID,
			CategoryName:    hit.CategoryName,
			SellerID:        hit.Seller.SellerID,
			Seller:          hit.Seller.SellerName,
			CustomerRating:  hit.Rating.CustomerRating,
			IsMarketplace:   hit.Seller.Marketplace,
			IsClearance:     hit.Clearance,
			LastIndex:       hit.LastIndex,
			IndexTimestamp:  hit.IndexTimestamp,
			SearchStartDate: hit.SearchStartDate,
			SearchEndDate:   hit.SearchEndDate,
			InStock:         hit.InStock,
			IsVisible:       hit.IsVisible,
			OnlineOnly:      hit.OnlineOnly,
			InStoreOnly:     hit.InStoreOnly,
			IsOnSale:        hit.IsOnSale,
			Advertised:      hit.Advertised,
			BrandName:       hit.BrandName,
			ModelNumber:     hit.ModelNumber,
			PrimaryUPC:      hit.PrimaryUPC,
			Specs:           hit.Specs,
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
	if merged.CategoryID == "" {
		merged.CategoryID = incoming.CategoryID
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
	if incoming.SearchEndDate > 0 {
		merged.SearchEndDate = incoming.SearchEndDate
	}
	if incoming.InStockKnown {
		merged.InStockKnown = true
		merged.InStock = incoming.InStock
	}
	if incoming.VisibilityKnown {
		merged.VisibilityKnown = true
		merged.IsVisible = incoming.IsVisible
	}
	merged.OnlineOnly = merged.OnlineOnly || incoming.OnlineOnly
	merged.InStoreOnly = merged.InStoreOnly || incoming.InStoreOnly
	merged.IsOnSale = merged.IsOnSale || incoming.IsOnSale
	merged.Advertised = merged.Advertised || incoming.Advertised
	if merged.BrandName == "" {
		merged.BrandName = incoming.BrandName
	}
	if merged.ModelNumber == "" {
		merged.ModelNumber = incoming.ModelNumber
	}
	if merged.PrimaryUPC == "" {
		merged.PrimaryUPC = incoming.PrimaryUPC
	}
	if merged.OfferEndDate == "" {
		merged.OfferEndDate = incoming.OfferEndDate
	}
	if len(merged.Specs) == 0 && len(incoming.Specs) > 0 {
		merged.Specs = cloneStringMap(incoming.Specs)
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
			CategoryID:      ap.CategoryID,
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
			SearchEndDate:   ap.SearchEndDate,
			InStock:         boolPtrValue(ap.InStock),
			InStockKnown:    ap.InStock != nil,
			IsVisible:       boolPtrValue(ap.IsVisible),
			VisibilityKnown: ap.IsVisible != nil,
			OnlineOnly:      ap.OnlineOnly,
			InStoreOnly:     ap.InStoreOnly,
			IsOnSale:        ap.IsOnSale,
			Advertised:      ap.Advertised,
			BrandName:       ap.BrandName,
			ModelNumber:     ap.ModelNumber,
			PrimaryUPC:      ap.PrimaryUPC,
			Specs:           normalizeSpecMap(ap.Specs),
		})
	}
	return products
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func normalizeSpecMap(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		switch typed := value.(type) {
		case string:
			out[key] = typed
		case float64:
			out[key] = strconv.FormatFloat(typed, 'f', -1, 64)
		case bool:
			out[key] = strconv.FormatBool(typed)
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				parts = append(parts, strings.TrimSpace(fmt.Sprint(item)))
			}
			out[key] = strings.Join(compactStrings(parts), ", ")
		default:
			out[key] = strings.TrimSpace(fmt.Sprint(typed))
		}
	}
	return out
}

func boolPtrValue(value *bool) bool {
	return value != nil && *value
}

func (c *Client) canValidateSellerOffers() bool {
	return c != nil && c.hasBackend(BackendAlgolia)
}

func (c *Client) ValidateSellerOffer(ctx context.Context, product Product, now time.Time) (OfferValidation, error) {
	if !c.canValidateSellerOffers() {
		product.AvailabilityCheckedAt = now
		return OfferValidation{Product: product, Valid: true}, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	updated := product
	if exact, ok, err := c.findExactAlgoliaProduct(ctx, product); err != nil {
		return OfferValidation{Product: updated, Reason: "algolia_error"}, err
	} else if ok {
		updated = mergeSellerProduct(updated, exact)
	} else {
		return OfferValidation{Product: updated, Reason: "seller_not_in_algolia"}, nil
	}

	if reason := rejectReasonFromIndexedState(updated, now); reason != "" {
		updated.AvailabilityCheckedAt = now
		return OfferValidation{Product: updated, Reason: reason}, nil
	}

	offers, err := c.fetchOffers(ctx, product.SKU)
	if err != nil {
		return OfferValidation{Product: updated, Reason: "offer_fetch_error"}, err
	}
	offer, ok := matchingOffer(offers, product)
	if !ok {
		updated.AvailabilityCheckedAt = now
		return OfferValidation{Product: updated, Reason: "seller_offer_missing"}, nil
	}
	if offerExpired(offer.OfferEndDate, now) {
		updated.OfferEndDate = offer.OfferEndDate
		updated.AvailabilityCheckedAt = now
		return OfferValidation{Product: updated, Reason: "seller_offer_expired"}, nil
	}

	offerPrice := effectiveOfferPrice(offer)
	currentPrice := effectiveProductPrice(product)
	if offerPrice > 0 && currentPrice > 0 && offerPrice > currentPrice+validationPriceEpsilon {
		updated = applyOfferToProduct(updated, offer)
		updated.AvailabilityCheckedAt = now
		return OfferValidation{Product: updated, Reason: "price_increased"}, nil
	}

	updated = applyOfferToProduct(updated, offer)
	updated.AvailabilityCheckedAt = now
	return OfferValidation{Product: updated, Valid: true}, nil
}

func (c *Client) EnrichComparables(ctx context.Context, product Product, now time.Time) (Product, error) {
	if !c.canValidateSellerOffers() {
		return product, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	comps, err := c.FindComparableListings(ctx, product, now)
	if err != nil {
		return product, err
	}
	applyComparableSummary(&product, comps)
	return product, nil
}

func (c *Client) FindComparableListings(ctx context.Context, product Product, now time.Time) ([]ComparableListing, error) {
	if !c.canValidateSellerOffers() {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now()
	}

	var comps []ComparableListing
	offers, err := c.fetchOffers(ctx, product.SKU)
	if err == nil {
		for _, offer := range offers {
			if offerExpired(offer.OfferEndDate, now) {
				continue
			}
			price := effectiveOfferPrice(offer)
			if price <= 0 {
				continue
			}
			if sameBestBuySeller(product, offer.SellerID, offer.SellerNameEn) {
				continue
			}
			comps = append(comps, ComparableListing{
				SKU:          firstNonEmpty(offer.SKU, product.SKU),
				Title:        product.Name,
				SellerID:     offer.SellerID,
				SellerName:   offer.SellerNameEn,
				Price:        price,
				RegularPrice: offer.RegularPrice,
				Condition:    productCondition(product.Name),
				Source:       "same-sku-offer",
			})
		}
	} else {
		slog.Warn("Best Buy comparable offer fetch failed",
			"processor", "bestbuy",
			"sku", product.SKU,
			"sellerID", product.SellerID,
			"error", err,
		)
	}

	query := comparableQuery(product)
	if query == "" {
		return comparableLimit(comps), nil
	}

	params := url.Values{
		"query":    {query},
		"pageSize": {"20"},
		"page":     {"1"},
	}
	if product.CategoryID != "" {
		params.Set("facetFilters", fmt.Sprintf(`["categoryIds:%s"]`, product.CategoryID))
	}
	resp, err := c.doRawAlgoliaSearch(ctx, params)
	if err != nil {
		return comparableLimit(comps), err
	}

	seen := make(map[string]struct{}, len(comps)+len(resp.Hits))
	for _, comp := range comps {
		seen[comp.SKU+"|"+comp.SellerID] = struct{}{}
	}
	candidateCondition := productCondition(product.Name)
	for _, hit := range resp.Hits {
		sku := firstNonEmpty(hit.SKU, hit.ObjectID)
		if sku == "" || sku == product.SKU {
			continue
		}
		hitProduct := productFromAlgoliaHit(hit, "marketplace")
		if reason := rejectReasonFromIndexedState(hitProduct, now); reason != "" {
			continue
		}
		price := effectiveProductPrice(hitProduct)
		if price <= 0 {
			continue
		}
		if sameBestBuySeller(product, hitProduct.SellerID, hitProduct.SellerName) {
			continue
		}
		if !comparableCondition(candidateCondition, productCondition(hitProduct.Name)) {
			continue
		}
		key := sku + "|" + hitProduct.SellerID
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		comps = append(comps, ComparableListing{
			SKU:          sku,
			Title:        hitProduct.Name,
			SellerID:     hitProduct.SellerID,
			SellerName:   hitProduct.SellerName,
			Price:        price,
			RegularPrice: hitProduct.RegularPrice,
			Condition:    productCondition(hitProduct.Name),
			Source:       "algolia",
		})
	}

	return comparableLimit(comps), nil
}

func (c *Client) findExactAlgoliaProduct(ctx context.Context, product Product) (Product, bool, error) {
	sellerID := sellerIDFromProduct(product)
	if product.SKU == "" || sellerID == "" {
		return product, true, nil
	}
	params := url.Values{
		"query":        {product.SKU},
		"pageSize":     {"10"},
		"page":         {"1"},
		"facetFilters": {fmt.Sprintf(`["seller.sellerId:%s"]`, sellerID)},
	}
	resp, err := c.doRawAlgoliaSearch(ctx, params)
	if err != nil {
		return Product{}, false, err
	}
	for _, hit := range resp.Hits {
		sku := firstNonEmpty(hit.SKU, hit.ObjectID)
		if sku != product.SKU {
			continue
		}
		return productFromAlgoliaHit(hit, product.Source), true, nil
	}
	return Product{}, false, nil
}

func productFromAlgoliaHit(hit algoliaHit, source string) Product {
	return convertProducts([]apiProduct{apiProductFromAlgoliaHit(hit)}, source)[0]
}

func apiProductFromAlgoliaHit(hit algoliaHit) apiProduct {
	sku := firstNonEmpty(hit.SKU, hit.ObjectID)
	currentPrice := hit.Price.CurrentPrice
	regularPrice := hit.Price.RegularPrice
	if currentPrice == 0 {
		currentPrice = regularPrice
	}
	if regularPrice == 0 {
		regularPrice = currentPrice
	}
	return apiProduct{
		SKU:             sku,
		Name:            hit.Title,
		ProductURL:      bestBuyProductURL(hit, sku),
		ThumbnailImage:  firstNonEmpty(hit.ImageURL, hit.HighResImageURL),
		RegularPrice:    regularPrice,
		SalePrice:       currentPrice,
		CategoryID:      hit.CategoryID,
		CategoryName:    hit.CategoryName,
		SellerID:        hit.Seller.SellerID,
		Seller:          hit.Seller.SellerName,
		CustomerRating:  hit.Rating.CustomerRating,
		IsMarketplace:   hit.Seller.Marketplace,
		IsClearance:     hit.Clearance,
		LastIndex:       hit.LastIndex,
		IndexTimestamp:  hit.IndexTimestamp,
		SearchStartDate: hit.SearchStartDate,
		SearchEndDate:   hit.SearchEndDate,
		InStock:         hit.InStock,
		IsVisible:       hit.IsVisible,
		OnlineOnly:      hit.OnlineOnly,
		InStoreOnly:     hit.InStoreOnly,
		IsOnSale:        hit.IsOnSale,
		Advertised:      hit.Advertised,
		BrandName:       hit.BrandName,
		ModelNumber:     hit.ModelNumber,
		PrimaryUPC:      hit.PrimaryUPC,
		Specs:           hit.Specs,
	}
}

func (c *Client) fetchOffers(ctx context.Context, sku string) ([]offerProduct, error) {
	if sku == "" {
		return nil, fmt.Errorf("missing sku")
	}
	reqURL := offerAPIBaseURL + "/" + url.PathEscape(sku) + "/offers?postalCode=S7K0A1"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("offer request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("offer status %d: %s", resp.StatusCode, string(body))
	}
	var offers []offerProduct
	if err := json.NewDecoder(resp.Body).Decode(&offers); err != nil {
		return nil, fmt.Errorf("decode offers: %w", err)
	}
	return offers, nil
}

func matchingOffer(offers []offerProduct, product Product) (offerProduct, bool) {
	sellerID := sellerIDFromProduct(product)
	for _, offer := range offers {
		if sellerID != "" && offer.SellerID == sellerID {
			return offer, true
		}
		if sellerID == "" && strings.EqualFold(strings.TrimSpace(offer.SellerNameEn), strings.TrimSpace(product.SellerName)) {
			return offer, true
		}
	}
	return offerProduct{}, false
}

func applyOfferToProduct(product Product, offer offerProduct) Product {
	if offer.SKU != "" {
		product.SKU = offer.SKU
	}
	if offer.SellerID != "" {
		product.SellerID = offer.SellerID
		product.Source = "seller:" + offer.SellerID
	}
	if offer.SellerNameEn != "" {
		product.SellerName = offer.SellerNameEn
	}
	if offer.RegularPrice > 0 {
		product.RegularPrice = offer.RegularPrice
	}
	if offer.SalePrice > 0 {
		product.SalePrice = offer.SalePrice
	}
	product.OfferEndDate = offer.OfferEndDate
	product.IsMarketplace = product.IsMarketplace || offer.IsMarketplace
	return product
}

func sellerIDFromProduct(product Product) string {
	if product.SellerID != "" {
		return product.SellerID
	}
	return strings.TrimPrefix(product.Source, "seller:")
}

func sameBestBuySeller(product Product, sellerID, sellerName string) bool {
	productSellerID := strings.TrimSpace(sellerIDFromProduct(product))
	if productSellerID != "" && strings.TrimSpace(sellerID) == productSellerID {
		return true
	}
	productSellerName := normalizeSellerName(product.SellerName)
	return productSellerName != "" && normalizeSellerName(sellerName) == productSellerName
}

func normalizeSellerName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(name))), " ")
}

func rejectReasonFromIndexedState(product Product, now time.Time) string {
	if product.InStockKnown && !product.InStock {
		return "out_of_stock"
	}
	if product.VisibilityKnown && !product.IsVisible {
		return "not_visible"
	}
	if product.SearchEndDate > 0 && millisToTime(product.SearchEndDate).Before(now) {
		return "search_expired"
	}
	return ""
}

func offerExpired(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	expires, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return false
	}
	return expires.Before(now)
}

func effectiveOfferPrice(offer offerProduct) float64 {
	if offer.SalePrice > 0 {
		return offer.SalePrice
	}
	return offer.RegularPrice
}

func effectiveProductPrice(product Product) float64 {
	if product.SalePrice > 0 {
		return product.SalePrice
	}
	return product.RegularPrice
}

func millisToTime(ms int64) time.Time {
	return time.UnixMilli(ms)
}

var nonAlphaNumeric = regexp.MustCompile(`[^a-z0-9]+`)

func comparableQuery(product Product) string {
	switch {
	case product.PrimaryUPC != "":
		return product.PrimaryUPC
	case product.BrandName != "" && product.ModelNumber != "":
		return strings.TrimSpace(product.BrandName + " " + product.ModelNumber)
	case product.ModelNumber != "":
		return product.ModelNumber
	default:
		normalized := strings.TrimSpace(nonAlphaNumeric.ReplaceAllString(strings.ToLower(product.Name), " "))
		parts := strings.Fields(normalized)
		if len(parts) > 8 {
			parts = parts[:8]
		}
		return strings.Join(parts, " ")
	}
}

func productCondition(title string) string {
	lower := strings.ToLower(title)
	switch {
	case strings.Contains(lower, "open box"):
		return "open_box"
	case strings.Contains(lower, "refurbished (fair)") || strings.Contains(lower, "refurbished fair"):
		return "refurbished_fair"
	case strings.Contains(lower, "refurbished (good)") || strings.Contains(lower, "refurbished good"):
		return "refurbished_good"
	case strings.Contains(lower, "refurbished (excellent)") || strings.Contains(lower, "refurbished excellent"):
		return "refurbished_excellent"
	case strings.Contains(lower, "refurbished"):
		return "refurbished"
	default:
		return "new_or_unknown"
	}
}

func comparableCondition(candidate, comp string) bool {
	if candidate == "" || candidate == "new_or_unknown" || comp == "" || comp == "new_or_unknown" {
		return true
	}
	if candidate == comp {
		return true
	}
	return strings.HasPrefix(candidate, "refurbished") && strings.HasPrefix(comp, "refurbished")
}

func comparableLimit(comps []ComparableListing) []ComparableListing {
	sort.SliceStable(comps, func(i, j int) bool {
		if comps[i].Price == comps[j].Price {
			return comps[i].Title < comps[j].Title
		}
		return comps[i].Price < comps[j].Price
	})
	if len(comps) > 8 {
		return comps[:8]
	}
	return comps
}

func applyComparableSummary(product *Product, comps []ComparableListing) {
	if product == nil || len(comps) == 0 {
		return
	}
	prices := make([]float64, 0, len(comps))
	for _, comp := range comps {
		if comp.Price > 0 {
			prices = append(prices, comp.Price)
		}
	}
	if len(prices) == 0 {
		return
	}
	sort.Float64s(prices)
	lowest := prices[0]
	median := prices[len(prices)/2]
	if len(prices)%2 == 0 {
		median = (prices[len(prices)/2-1] + prices[len(prices)/2]) / 2
	}
	current := effectiveProductPrice(*product)
	discountPct := 0.0
	if median > 0 && current > 0 && current < median {
		discountPct = (median - current) / median * 100
	}

	product.ComparableCount = len(prices)
	product.ComparableLowestPrice = lowest
	product.ComparableMedianPrice = median
	product.ComparableDiscountPct = discountPct
	product.ComparableSummary = fmt.Sprintf("Best Buy comps: $%.2f median / $%.2f low across %d active comparable%s excluding this seller; candidate is %.0f%% below median.",
		median, lowest, len(prices), pluralSuffix(len(prices)), discountPct)
}

func pluralSuffix(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
