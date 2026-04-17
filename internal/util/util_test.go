package util

import (
	"testing"
)

func TestCleanReferralLink(t *testing.T) {
	bestBuyPrefix := "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
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
		{
			name:     "BestBuy direct link",
			input:    "https://bestbuy.ca/en-ca/product/12345",
			expected: "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u=https%3A%2F%2Fbestbuy.ca%2Fen-ca%2Fproduct%2F12345",
			changed:  true,
		},
		{
			name:     "Linksynergy with valid MURL",
			input:    "https://click.linksynergy.com/link?murl=https%3A%2F%2Fexample.com%2Fproduct",
			expected: "https://example.com/product",
			changed:  true,
		},
		{
			name:     "Linksynergy with non-HTTP MURL rejected",
			input:    "https://click.linksynergy.com/link?murl=javascript%3Aalert(1)",
			expected: "https://click.linksynergy.com/link?murl=javascript%3Aalert(1)",
			changed:  false,
		},
		{
			name:     "Redirectingat with valid URL",
			input:    "https://go.redirectingat.com/?url=https%3A%2F%2Fexample.com%2Fdeal",
			expected: "https://example.com/deal",
			changed:  true,
		},
		{
			name:     "eBay US direct link gets affiliate params",
			input:    "https://www.ebay.com/itm/Apple-MacBook-Pro-16-inch-M3-Pro/123456789012?amdata=enc%3A123",
			expected: "https://www.ebay.com/itm/123456789012?mkcid=1&mkrid=711-53200-19255-0&siteid=0&campid=5339131483&customid=&toolid=10001&mkevt=1",
			changed:  true,
		},
		{
			name:     "eBay CA direct link gets affiliate params",
			input:    "https://www.ebay.ca/itm/134954474751?_skw=laptop&_trkparms=ispr%3D1&hash=item1f6bf870ff:g:abc",
			expected: "https://www.ebay.ca/itm/134954474751?mkcid=1&mkrid=706-53473-19255-0&siteid=2&campid=5339131483&customid=&toolid=10001&mkevt=1",
			changed:  true,
		},
		{
			name:     "Redirectingat eBay link gets unwrapped and affiliate params",
			input:    "https://go.redirectingat.com/?url=https%3A%2F%2Fwww.ebay.ca%2Fitm%2F134954474751%3F_trkparms%3Dispr%253D1",
			expected: "https://www.ebay.ca/itm/134954474751?mkcid=1&mkrid=706-53473-19255-0&siteid=2&campid=5339131483&customid=&toolid=10001&mkevt=1",
			changed:  true,
		},
		{
			name:     "Redirectingat with non-HTTP URL rejected",
			input:    "https://go.redirectingat.com/?url=ftp%3A%2F%2Fexample.com%2Ffile",
			expected: "https://go.redirectingat.com/?url=ftp%3A%2F%2Fexample.com%2Ffile",
			changed:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := CleanReferralLink(tt.input, "beauahrens0d-20", bestBuyPrefix)
			if got != tt.expected {
				t.Errorf("CleanReferralLink() got = %v, want %v", got, tt.expected)
			}
			if changed != tt.changed {
				t.Errorf("CleanReferralLink() changed = %v, want %v", changed, tt.changed)
			}
		})
	}
}

func TestIsHTTPURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Valid HTTPS", "https://example.com", true},
		{"Valid HTTP", "http://example.com", true},
		{"Case sensitive HTTPS (fail)", "HTTPS://example.com", false},
		{"Case sensitive HTTP (fail)", "HTTP://example.com", false},
		{"FTP scheme", "ftp://example.com", false},
		{"Javascript scheme", "javascript:alert(1)", false},
		{"Empty string", "", false},
		{"Just prefix HTTPS", "https://", true},
		{"Just prefix HTTP", "http://", true},
		{"Leading space", " https://example.com", false},
		{"Malformed - missing slashes", "https:example.com", false},
		{"No scheme", "example.com", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isHTTPURL(tt.input); got != tt.want {
				t.Errorf("isHTTPURL(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestIsEbayItemID(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"Valid 12 digit ID", "123456789012", true},
		{"Valid 10 digit ID", "1234567890", true},
		{"Too short", "123456789", false},
		{"Too long", "12345678901234", false},
		{"Contains letters", "12345abc9012", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEbayItemID(tt.input); got != tt.want {
				t.Errorf("isEbayItemID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	allowedDomains := []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com", "www.forums.redflagdeals.com"}

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
			got, err := NormalizeURL(tt.input, allowedDomains)
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
