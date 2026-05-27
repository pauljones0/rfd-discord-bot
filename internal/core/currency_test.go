package core

import (
	"math"
	"testing"
)

func TestResolveCurrencyFromCountry(t *testing.T) {
	m := NewRateManager()

	tests := []struct {
		text string
		want string
	}{
		{"$100.00 | Amazon DE | Destined Rivals... @Germany", "EUR"},
		{"$100.00 | Shop | Something @Norway", "NOK"},
		{"$100.00 | Shop | Something @NO", "NOK"},
		{"$100.00 | Shop | Something @UK", "GBP"},
		{"$100.00 | Shop | Something @GB", "GBP"},
		{"$100.00 | Shop | Something @Canada", "CAD"},
		{"$100.00 | Shop | Something @CA", "CAD"},
		{"$100.00 | Shop | Something @USA", "USD"},
		{"$100.00 | Shop | Something @US", "USD"},
		{"$100.00 | Shop | Something @COM", "USD"},
		{"$100.00 | Shop | Something @Unknown", ""},
		{"$100.00 | Shop | Something", ""},
	}

	for _, tt := range tests {
		got := m.ResolveCurrencyFromCountry(tt.text)
		if got != tt.want {
			t.Errorf("ResolveCurrencyFromCountry(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}

func TestRateManagerConvertToCAD(t *testing.T) {
	m := NewRateManager()

	tests := []struct {
		price float64
		curr  string
		want  float64 // price / rate
	}{
		{100, "CAD", 100.0},
		{100, "USD", 100.0 / 0.73},
		{100, "EUR", 100.0 / 0.67},
		{100, "GBP", 100.0 / 0.57},
		{100, "NOK", 100.0 / 7.82},
		{100, "UNKNOWN", 100.0 / 0.73}, // fall back to USD
	}

	for _, tt := range tests {
		got := m.ConvertToCAD(tt.price, tt.curr)
		if math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("ConvertToCAD(%v, %q) = %v, want %v", tt.price, tt.curr, got, tt.want)
		}
	}
}
