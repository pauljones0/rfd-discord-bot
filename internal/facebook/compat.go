package facebook

import (
	"context"
	"crypto/rand"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ProxyBlocklist allows the HTTP scraper to check and persist blocked proxy IPs.
type ProxyBlocklist interface {
	IsProxyBlocked(ctx context.Context, ip string) (bool, error)
	BlockProxyIP(ctx context.Context, ip, city string) error
}

// FacebookScrapeConfig holds the parameters for a Facebook Marketplace scrape.
type FacebookScrapeConfig struct {
	City         string
	Category     string
	RadiusKm     int
	FilterBrands []string
}

// TrimPicker chooses the best trim from the Carfax options returned for a listing.
type TrimPicker func(ctx context.Context, year int, make, model string, options []string) string

const (
	// Retry counts preserved from the previous scraper implementation.
	maxProxyRetries           = 3
	maxCountryFallbackRetries = 3
)

type browserProfile struct {
	userAgent string
}

// profiles provides realistic desktop user agents for the HTTP scraper.
var profiles = []browserProfile{
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0"},
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"},
	{userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36"},
	{userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0"},
}

// valueRangeRe matches ranges like "$12,345 - $16,789".
var valueRangeRe = regexp.MustCompile(`\$?\s*([\d,]+(?:\.\d+)?)\s*[-–—]\s*\$?\s*([\d,]+(?:\.\d+)?)`)

var singleValueRe = regexp.MustCompile(`\$?\s*([\d,]+(?:\.\d+)?)`)

func parseValueRange(valStr string) (float64, error) {
	valStr = strings.TrimSpace(valStr)
	if valStr == "" {
		return 0, fmt.Errorf("empty value string")
	}

	if m := valueRangeRe.FindStringSubmatch(valStr); len(m) >= 3 {
		minVal, err := parseMoneyNumber(m[1])
		if err != nil {
			return 0, fmt.Errorf("failed to parse range minimum %q: %w", m[1], err)
		}
		maxVal, err := parseMoneyNumber(m[2])
		if err != nil {
			return 0, fmt.Errorf("failed to parse range maximum %q: %w", m[2], err)
		}
		if minVal <= 0 || maxVal <= 0 {
			return 0, fmt.Errorf("parsed non-positive values from range: min=%.0f max=%.0f", minVal, maxVal)
		}
		return (minVal + maxVal) / 2, nil
	}

	if m := singleValueRe.FindStringSubmatch(valStr); len(m) >= 2 {
		value, err := parseMoneyNumber(m[1])
		if err != nil {
			return 0, fmt.Errorf("failed to parse single value %q: %w", m[1], err)
		}
		if value <= 0 {
			return 0, fmt.Errorf("parsed non-positive value: %.0f", value)
		}
		return value, nil
	}

	return 0, fmt.Errorf("no numeric value found in: %s", valStr)
}

func parseMoneyNumber(raw string) (float64, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	return strconv.ParseFloat(cleaned, 64)
}

// randomSessionID generates an 8-character session ID for sticky proxy sessions.
func randomSessionID() string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "session00"
	}
	for i := range buf {
		buf[i] = chars[int(buf[i])%len(chars)]
	}
	return string(buf)
}
