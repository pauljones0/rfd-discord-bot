package memoryexpress

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	baseURL   = "https://www.memoryexpress.com/Clearance/Store/"
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:128.0) Gecko/20100101 Firefox/128.0"
)

var priceRe = regexp.MustCompile(`\$([0-9,]+\.\d{2})`)

// Scrape fetches and parses clearance products for a given Memory Express store.
func Scrape(ctx context.Context, storeCode string) ([]Product, error) {
	if !ValidStoreCode(storeCode) {
		return nil, fmt.Errorf("invalid store code: %s", storeCode)
	}

	url := baseURL + storeCode
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch clearance page for %s: %w", storeCode, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d for store %s", resp.StatusCode, storeCode)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML for store %s: %w", storeCode, err)
	}

	storeName := StoreName(storeCode)
	var products []Product

	// Each category group contains products
	doc.Find(".c-clli-group").Each(func(_ int, group *goquery.Selection) {
		category := strings.TrimSpace(group.Find(".c-clli-group__header-title").Text())

		group.Find(".c-clli-item").Each(func(_ int, item *goquery.Selection) {
			p, err := parseProduct(item, storeCode, storeName, category)
			if err != nil {
				slog.Warn("Failed to parse clearance product",
					"processor", "memoryexpress",
					"store", storeCode,
					"error", err,
				)
				return
			}
			products = append(products, p)
		})
	})

	slog.Info("Scraped Memory Express clearance",
		"processor", "memoryexpress",
		"store", storeCode,
		"products", len(products),
	)

	return products, nil
}

func parseProduct(item *goquery.Selection, storeCode, storeName, category string) (Product, error) {
	var p Product
	p.StoreCode = storeCode
	p.StoreName = storeName
	p.Category = category

	// Title and URL
	titleLink := item.Find(".c-clli-item-info__title a")
	p.Title = strings.TrimSpace(titleLink.Text())
	if p.Title == "" {
		return p, fmt.Errorf("missing product title")
	}
	if href, exists := titleLink.Attr("href"); exists {
		if strings.HasPrefix(href, "/") {
			p.URL = "https://www.memoryexpress.com" + href
		} else {
			p.URL = href
		}
	}

	// SKU and ILC from codes text
	codesText := item.Find(".c-clli-item-info__codes").Text()
	p.SKU = extractField(codesText, "SKU:")
	p.ILC = extractField(codesText, "ILC:")

	if p.SKU == "" {
		return p, fmt.Errorf("missing SKU for product %q", p.Title)
	}

	// Prices
	p.RegularPrice = parsePrice(item.Find(".c-clli-item-price__regular").Text())
	p.ClearancePrice = parsePrice(item.Find(".c-clli-item-price__clearance-value").Text())
	p.SalePrice = parsePrice(item.Find(".c-clli-item-price__sale-value").Text())

	// Use best available final price
	finalPrice := p.SalePrice
	if finalPrice == 0 {
		finalPrice = p.ClearancePrice
	}
	if finalPrice == 0 {
		finalPrice = p.RegularPrice
	}

	// Calculate discount
	if p.RegularPrice > 0 && finalPrice > 0 && finalPrice < p.RegularPrice {
		p.DiscountPct = (1 - finalPrice/p.RegularPrice) * 100
	}

	// Stock
	stockText := strings.TrimSpace(item.Find(".c-clli-item__stock").Text())
	if n, err := strconv.Atoi(stockText); err == nil {
		p.Stock = n
	} else {
		p.Stock = 1 // Default to 1 if not parseable
	}

	// Image
	if img := item.Find(".c-clli-item__image img"); img.Length() > 0 {
		if src, exists := img.Attr("src"); exists {
			p.ImageURL = src
		}
	}

	return p, nil
}

// extractField extracts a value after a label from a text block.
// e.g., extractField("SKU: MX00116711 ILC: 710102841062", "SKU:") → "MX00116711"
func extractField(text, label string) string {
	idx := strings.Index(text, label)
	if idx == -1 {
		return ""
	}
	rest := strings.TrimSpace(text[idx+len(label):])
	// Take the first whitespace-delimited token
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// parsePrice extracts a dollar amount from text like "$377.99" or "$1,079.99".
func parsePrice(text string) float64 {
	matches := priceRe.FindStringSubmatch(text)
	if len(matches) < 2 {
		return 0
	}
	cleaned := strings.ReplaceAll(matches[1], ",", "")
	val, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0
	}
	return val
}
