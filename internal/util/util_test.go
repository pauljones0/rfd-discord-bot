package util

import (
	"testing"
)

func TestCleanReferralLink(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		changed  bool
	}{
		{
			name:     "No change",
			input:    "https://example.com/product",
			expected: "https://example.com/product",
			changed:  false,
		},
		{
			name:     "Amazon clean tag",
			input:    "https://amazon.ca/dp/12345?tag=old-tag",
			expected: "https://amazon.ca/dp/12345?tag=beauahrens0d-20",
			changed:  true,
		},
		{
			name:     "Amazon add tag",
			input:    "https://amazon.ca/dp/12345",
			expected: "https://amazon.ca/dp/12345?tag=beauahrens0d-20",
			changed:  true,
		},
		{
			name:     "BestBuy redirect",
			input:    "https://bestbuyca.o93x.net/c/123/456/789?u=https://bestbuy.ca/product",
			expected: "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u=https%3A%2F%2Fbestbuy.ca%2Fproduct",
			changed:  true,
		},
		{
			name:     "BestBuy redirect secondary param",
			input:    "https://bestbuyca.o93x.net/c/123/456/789?subId1=foo&u=https://bestbuy.ca/product",
			expected: "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u=https%3A%2F%2Fbestbuy.ca%2Fproduct",
			changed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := CleanReferralLink(tt.input, "beauahrens0d-20")
			if got != tt.expected {
				t.Errorf("CleanReferralLink() got = %v, want %v", got, tt.expected)
			}
			if changed != tt.changed {
				t.Errorf("CleanReferralLink() changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "Standard RFD URL",
			input: "https://forums.redflagdeals.com/my-deal-1234567/",
			want:  "https://forums.redflagdeals.com/my-deal-1234567",
		},
		{
			name:  "Remove www",
			input: "https://www.forums.redflagdeals.com/my-deal/",
			want:  "https://forums.redflagdeals.com/my-deal",
		},
		{
			name:  "Remove UTM params",
			input: "https://forums.redflagdeals.com/deal?utm_source=foo&utm_medium=bar",
			want:  "https://forums.redflagdeals.com/deal",
		},
		{
			name:  "Remove RFD tracking params",
			input: "https://forums.redflagdeals.com/deal?rfd_sk=tt&sd=d",
			want:  "https://forums.redflagdeals.com/deal",
		},
		{
			name:  "Non-RFD URL unchanged",
			input: "http://amazon.ca/some-product?utm_source=foo",
			want:  "http://amazon.ca/some-product?utm_source=foo",
		},
		{
			name:  "HTTP RFD URL forced to HTTPS",
			input: "http://forums.redflagdeals.com/deal-123/",
			want:  "https://forums.redflagdeals.com/deal-123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeURL(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("NormalizeURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("NormalizeURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSafeAtoi(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"Simple number", "42", 42},
		{"Zero", "0", 0},
		{"Negative", "-5", -5},
		{"With spaces", "  123  ", 123},
		{"Empty string", "", 0},
		{"Non-numeric", "abc", 0},
		{"Mixed", "12abc", 0},
		{"Plus sign", "+42", 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeAtoi(tt.input)
			if got != tt.want {
				t.Errorf("SafeAtoi(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestCleanNumericString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"Simple number", "42", "42"},
		{"With commas", "1,234", "1234"},
		{"With text", "123 views", "123"},
		{"Leading text", "+42", "42"},
		{"Empty", "", ""},
		{"No digits", "abc", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanNumericString(tt.input)
			if got != tt.want {
				t.Errorf("CleanNumericString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseSignedNumericString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"Positive", "+42", "42"},
		{"Negative", "-5", "-5"},
		{"With text", "Score: -12 points", "-12"},
		{"Plain number", "123", "123"},
		{"No number", "abc", ""},
		{"Empty", "", ""},
		{"Zero", "0", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSignedNumericString(tt.input)
			if got != tt.want {
				t.Errorf("ParseSignedNumericString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
