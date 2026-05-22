package scrapelab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

// Target is one real page/API URL to test.
type Target struct {
	Site string `json:"site"`
	Name string `json:"name,omitempty"`
	URL  string `json:"url"`
}

// Result is one backend attempt against one target.
type Result struct {
	Site            string        `json:"site"`
	Name            string        `json:"name,omitempty"`
	URL             string        `json:"url"`
	Backend         string        `json:"backend"`
	Environment     string        `json:"environment"`
	StatusCode      int           `json:"statusCode,omitempty"`
	BlockSignal     string        `json:"blockSignal,omitempty"`
	ParsedItemCount int           `json:"parsedItemCount"`
	CouponDiscount  float64       `json:"couponDiscount,omitempty"`
	CouponCode      string        `json:"couponCode,omitempty"`
	CouponMessage   string        `json:"couponMessage,omitempty"`
	Duration        time.Duration `json:"duration"`
	SamplePath      string        `json:"samplePath,omitempty"`
	Verdict         string        `json:"verdict"`
	Error           string        `json:"error,omitempty"`
	CreatedAt       time.Time     `json:"createdAt"`
}

// Options controls scrape lab execution.
type Options struct {
	Backends      []string
	Environment   string
	OutDir        string
	Timeout       time.Duration
	ChromeProfile string
}

// Run executes every target/backend combination and writes sample HTML files.
func Run(ctx context.Context, targets []Target, opts Options) ([]Result, error) {
	if len(opts.Backends) == 0 {
		opts.Backends = []string{scrapebackend.BackendHTTP}
	}
	if opts.Environment == "" {
		opts.Environment = "local"
	}
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join("docs", "scrape-lab", time.Now().Format("20060102-150405"))
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return nil, err
	}

	var results []Result
	for _, target := range targets {
		for _, backend := range opts.Backends {
			var fetch scrapebackend.FetchResult
			if normalizeSite(target.Site) == "bestbuy" && backend == bestbuy.BackendAlgolia {
				fetch = fetchBestBuyAlgolia(ctx, target)
			} else {
				fetch = scrapebackend.FetchHTML(ctx, scrapebackend.FetchOptions{
					Backend:             backend,
					URL:                 target.URL,
					Timeout:             opts.Timeout,
					ChromeProfile:       opts.ChromeProfile,
					ExternalCommand:     os.Getenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND"),
					ExternalCommandArgs: scrapebackend.CommandArgsFromEnv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS"),
					CamoufoxCommand:     os.Getenv("SCRAPELAB_CAMOUFOX_COMMAND"),
					CamoufoxCommandArgs: scrapebackend.CommandArgsFromEnv("SCRAPELAB_CAMOUFOX_COMMAND_ARGS"),
					AICrawlerCommand:    os.Getenv("SCRAPELAB_AI_CRAWLER_COMMAND"),
					AICrawlerArgs:       scrapebackend.CommandArgsFromEnv("SCRAPELAB_AI_CRAWLER_COMMAND_ARGS"),
					PaidCommand:         os.Getenv("SCRAPELAB_PAID_TRIAL_COMMAND"),
					PaidCommandArgs:     scrapebackend.CommandArgsFromEnv("SCRAPELAB_PAID_TRIAL_COMMAND_ARGS"),
					PaidEnabled:         envBool("SCRAPELAB_PAID_BROWSER_ENABLED"),
				})
			}

			result := Result{
				Site:        normalizeSite(target.Site),
				Name:        target.Name,
				URL:         target.URL,
				Backend:     backend,
				Environment: opts.Environment,
				StatusCode:  fetch.StatusCode,
				BlockSignal: fetch.BlockSignal,
				Duration:    fetch.Duration,
				Error:       fetch.Error,
				CreatedAt:   time.Now(),
			}
			if fetch.HTML != "" {
				samplePath, err := writeSample(opts.OutDir, target, backend, fetch.HTML)
				if err == nil {
					result.SamplePath = samplePath
				}
			}
			analyzeResult(&result, fetch.HTML)
			results = append(results, result)
		}
	}

	if err := WriteReports(opts.OutDir, results); err != nil {
		return results, err
	}
	return results, nil
}

func envBool(key string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func fetchBestBuyAlgolia(ctx context.Context, target Target) scrapebackend.FetchResult {
	start := time.Now()
	client := bestbuy.NewClient()
	client.SetBackends([]string{bestbuy.BackendAlgolia})

	products, err := client.FetchSellerProducts(ctx, bestBuySellerFromTarget(target))
	result := scrapebackend.FetchResult{
		Backend:    bestbuy.BackendAlgolia,
		URL:        target.URL,
		FinalURL:   target.URL,
		StatusCode: 200,
		Duration:   time.Since(start),
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	payload, err := json.MarshalIndent(struct {
		Products []bestbuy.Product `json:"products"`
	}{Products: products}, "", "  ")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.HTML = string(payload)
	return result
}

func bestBuySellerFromTarget(target Target) bestbuy.Seller {
	searchPath := ""
	if parsed, err := url.Parse(target.URL); err == nil {
		searchPath = parsed.Query().Get("path")
	}
	name := target.Name
	if name == "" && strings.HasPrefix(searchPath, "sellerName:") {
		name = strings.TrimSpace(strings.TrimPrefix(searchPath, "sellerName:"))
	}
	return bestbuy.Seller{
		Name:       name,
		SearchPath: searchPath,
		SearchURL:  target.URL,
		IsActive:   true,
	}
}

func analyzeResult(result *Result, html string) {
	if result.Site == "ebay" && isEbayItemPage(html) {
		result.BlockSignal = ""
		analyzeEbayResult(result, html)
		return
	}
	if result.Site == "memoryexpress" && strings.HasPrefix(result.BlockSignal, "cloudflare") && isMemoryExpressClearancePage(html) {
		result.BlockSignal = ""
	}

	if result.Error != "" {
		result.Verdict = "error"
		return
	}
	if result.BlockSignal != "" {
		result.Verdict = "blocked"
		return
	}

	switch result.Site {
	case "ebay":
		analyzeEbayResult(result, html)
	case "memoryexpress":
		storeCode := memoryExpressStoreCode(result.URL)
		if storeCode == "" {
			result.Verdict = "error"
			result.Error = "could not infer Memory Express store code"
			return
		}
		products, err := memoryexpress.ParseClearanceHTML(storeCode, html)
		if err != nil {
			result.Verdict = "parse-error"
			result.Error = err.Error()
			return
		}
		result.ParsedItemCount = len(products)
		result.Verdict = "pass"
	case "bestbuy":
		count, err := countBestBuyProducts(html)
		if err != nil {
			result.Verdict = "parse-error"
			result.Error = err.Error()
			return
		}
		result.ParsedItemCount = count
		if count == 0 {
			result.Verdict = "pass-no-items"
			return
		}
		result.Verdict = "pass"
	default:
		if strings.TrimSpace(html) == "" {
			result.Verdict = "empty"
			return
		}
		result.Verdict = "pass"
	}
}

func analyzeEbayResult(result *Result, html string) {
	coupon := ebay.ExtractPageCoupon(html, 100)
	result.CouponDiscount = coupon.DiscountAmount
	result.CouponCode = coupon.Code
	result.CouponMessage = coupon.Message
	if isEbayItemPage(html) {
		result.ParsedItemCount = 1
	}
	if coupon.DiscountAmount > 0 {
		result.Verdict = "pass"
		return
	}
	result.Verdict = "pass-no-coupon"
}

func writeSample(outDir string, target Target, backend, html string) (string, error) {
	name := target.Name
	if name == "" {
		name = target.Site
	}
	name = sanitizeFilename(name + "-" + backend + ".html")
	path := filepath.Join(outDir, name)
	return path, os.WriteFile(path, []byte(html), 0o644)
}

func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func normalizeSite(site string) string {
	return strings.ToLower(strings.TrimSpace(site))
}

func isEbayItemPage(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, `property="og:type" content="ebay-objects:item"`) ||
		strings.Contains(lower, `content="ebay-objects:item"`) ||
		strings.Contains(lower, "x-item-title") ||
		strings.Contains(lower, "vim x-item-title")
}

func memoryExpressStoreCode(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for i := range parts {
		if strings.EqualFold(parts[i], "Store") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}

func isMemoryExpressClearancePage(raw string) bool {
	lower := strings.ToLower(raw)
	return strings.Contains(lower, `id="clearancecenter"`) ||
		strings.Contains(lower, "c-clli-group") ||
		strings.Contains(lower, "c-clli-item") ||
		strings.Contains(lower, "clearance centre:")
}

func countBestBuyProducts(raw string) (int, error) {
	if payload := extractJSONPayload(raw); payload != "" {
		var resp struct {
			Products []json.RawMessage `json:"products"`
		}
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return 0, err
		}
		return len(resp.Products), nil
	}

	if statePayload := extractBestBuyInitialState(raw); statePayload != "" {
		var state struct {
			Search struct {
				SearchResult struct {
					Products []json.RawMessage `json:"products"`
				} `json:"searchResult"`
			} `json:"search"`
		}
		if err := json.Unmarshal([]byte(statePayload), &state); err != nil {
			return 0, fmt.Errorf("decode __INITIAL_STATE__: %w", err)
		}
		return len(state.Search.SearchResult.Products), nil
	}

	markerCount := strings.Count(raw, `data-automation="product-listing-item"`) +
		strings.Count(raw, "productItemContainer") +
		strings.Count(raw, "product-listing-item")
	if markerCount > 0 {
		return markerCount, nil
	}
	if strings.Contains(raw, "productsContainer") || strings.Contains(raw, "productListingContainer") {
		return 0, nil
	}
	return 0, fmt.Errorf("no product JSON, app state, or product markers found")
}

func extractJSONPayload(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "<pre") {
		start := strings.Index(trimmed, ">")
		end := strings.LastIndex(strings.ToLower(trimmed), "</pre>")
		if start >= 0 && end > start {
			payload := strings.TrimSpace(trimmed[start+1 : end])
			if strings.HasPrefix(payload, "{") && strings.HasSuffix(payload, "}") {
				return payload
			}
		}
	}
	return ""
}

func extractBestBuyInitialState(raw string) string {
	const marker = "window.__INITIAL_STATE__"
	idx := strings.Index(raw, marker)
	if idx < 0 {
		return ""
	}
	afterMarker := raw[idx+len(marker):]
	equalsIdx := strings.Index(afterMarker, "=")
	if equalsIdx < 0 {
		return ""
	}
	afterEquals := afterMarker[equalsIdx+1:]
	start := strings.Index(afterEquals, "{")
	if start < 0 {
		return ""
	}
	return extractBalancedJSONObject(afterEquals[start:])
}

func extractBalancedJSONObject(raw string) string {
	depth := 0
	inString := false
	escaped := false
	for i, r := range raw {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch r {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}

		switch r {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[:i+1]
			}
		}
	}
	return ""
}
