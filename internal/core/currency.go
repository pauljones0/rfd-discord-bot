package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// RateManager handles dynamic exchange rate fetching and conversion.
type RateManager struct {
	mu    sync.RWMutex
	rates map[string]float64
}

// Default fallback rates (relative to 1 CAD) in case API fetch fails.
var defaultRates = map[string]float64{
	"CAD": 1.0,
	"USD": 0.73,
	"EUR": 0.67,
	"GBP": 0.57,
	"NOK": 7.82,
	"SEK": 7.74,
	"DKK": 5.03,
	"CHF": 0.66,
	"AUD": 1.10,
	"JPY": 115.0,
	"PLN": 2.9,
}

// CountryTagToCurrency maps country/region tags in alerts to their standard currency codes.
var CountryTagToCurrency = map[string]string{
	// Canada
	"CANADA": "CAD",
	"CA":     "CAD",
	"CAN":    "CAD",

	// US
	"USA": "USD",
	"US":  "USD",
	"COM": "USD",

	// UK
	"UK":      "GBP",
	"GB":      "GBP",
	"ENGLAND": "GBP",

	// EU (Eurozone)
	"GERMANY":     "EUR",
	"DE":          "EUR",
	"FRANCE":      "EUR",
	"FR":          "EUR",
	"ITALY":       "EUR",
	"IT":          "EUR",
	"SPAIN":       "EUR",
	"ES":          "EUR",
	"EUROPE":      "EUR",
	"EU":          "EUR",
	"NETHERLANDS": "EUR",
	"NL":          "EUR",
	"BELGIUM":     "EUR",
	"BE":          "EUR",
	"AUSTRIA":     "EUR",
	"AT":          "EUR",
	"IRELAND":     "EUR",
	"IE":          "EUR",

	// Norway
	"NORWAY": "NOK",
	"NO":     "NOK",
	"NOR":    "NOK",

	// Sweden
	"SWEDEN": "SEK",
	"SE":     "SEK",
	"SWE":    "SEK",

	// Denmark
	"DENMARK": "DKK",
	"DK":      "DKK",
	"DEN":     "DKK",

	// Switzerland
	"SWITZERLAND": "CHF",
	"CH":          "CHF",
	"CHE":         "CHF",

	// Australia
	"AUSTRALIA": "AUD",
	"AU":        "AUD",
	"AUS":       "AUD",

	// Japan
	"JAPAN": "JPY",
	"JP":    "JPY",
	"JPN":   "JPY",

	// Poland
	"POLAND": "PLN",
	"PL":     "PLN",
}

// NewRateManager creates a RateManager initialized with default rates.
func NewRateManager() *RateManager {
	ratesCopy := make(map[string]float64, len(defaultRates))
	for k, v := range defaultRates {
		ratesCopy[k] = v
	}
	return &RateManager{
		rates: ratesCopy,
	}
}

// FetchRates updates exchange rates from a public API.
func (m *RateManager) FetchRates(ctx context.Context) error {
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://open.er-api.com/v6/latest/CAD", nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP error %d", resp.StatusCode)
	}

	var payload struct {
		Result   string             `json:"result"`
		Rates    map[string]float64 `json:"rates"`
		BaseCode string             `json:"base_code"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return err
	}

	if payload.Result != "success" || payload.BaseCode != "CAD" || len(payload.Rates) == 0 {
		return fmt.Errorf("invalid API payload response structure")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update rates, ensuring we preserve CAD=1.0 and overwrite others.
	for code, val := range payload.Rates {
		if val > 0 {
			m.rates[code] = val
		}
	}
	m.rates["CAD"] = 1.0

	slog.Info("RateManager: Successfully refreshed exchange rates", "total_currencies", len(m.rates))
	return nil
}

// StartAutoRefresh begins periodic rate updates in the background.
func (m *RateManager) StartAutoRefresh(ctx context.Context, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				slog.Info("RateManager: Auto-refreshing exchange rates...")
				if err := m.FetchRates(ctx); err != nil {
					slog.Error("RateManager: Failed to auto-refresh rates", "error", err)
				}
			}
		}
	}()
}

// ConvertToCAD converts a foreign price to CAD using the active rates map.
func (m *RateManager) ConvertToCAD(price float64, currency string) float64 {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "CAD" || price <= 0 {
		return price
	}

	m.mu.RLock()
	rate, ok := m.rates[currency]
	usdRate := m.rates["USD"]
	m.mu.RUnlock()

	if !ok || rate <= 0 {
		// Fallback to USD rate as standard fallback
		slog.Warn("RateManager: Unknown currency conversion requested, falling back to USD rate", "currency", currency)
		if usdRate > 0 {
			rate = usdRate
		} else {
			rate = 0.73 // absolute hard fallback
		}
	}

	return price / rate
}

var tagExtractRegex = regexp.MustCompile(`(?:[\x{2068}\x{2069}\s]|^)@([a-zA-Z0-9]+)(?:[\x{2068}\x{2069}\s]|$)`)

// ResolveCurrencyFromCountry parses country tags (e.g. @Germany) and returns the matching currency code.
func (m *RateManager) ResolveCurrencyFromCountry(text string) string {
	matches := tagExtractRegex.FindAllStringSubmatch(text, -1)
	for _, match := range matches {
		if len(match) > 1 {
			tagUpper := strings.ToUpper(match[1])
			if currencyCode, ok := CountryTagToCurrency[tagUpper]; ok {
				return currencyCode
			}
		}
	}
	return ""
}
