package models

import "testing"

func TestPrimaryPostURL(t *testing.T) {
	tests := []struct {
		name          string
		actualDealURL string
		postURL       string
		expected      string
	}{
		{
			name:          "ActualDealURL is populated, should return ActualDealURL",
			actualDealURL: "https://example.com/actual-deal",
			postURL:       "https://example.com/post",
			expected:      "https://example.com/actual-deal",
		},
		{
			name:          "ActualDealURL is empty, should return PostURL",
			actualDealURL: "",
			postURL:       "https://example.com/post",
			expected:      "https://example.com/post",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := &DealInfo{
				ActualDealURL: tt.actualDealURL,
				PostURL:       tt.postURL,
			}
			result := deal.PrimaryPostURL()
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}
