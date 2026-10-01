package util

import (
	"testing"
)

func TestIsTechCategory(t *testing.T) {
	tests := []struct {
		category string
		want     bool
	}{
		{"Computers & Electronics", true},
		{"Cell Phones", true},
		{"Apparel", false},
		{"Groceries", false},
		{"  video games  ", true},
		{"PC & Video Games", true},
		{"", false},
		{"Automotive", false},
	}

	for _, tt := range tests {
		t.Run(tt.category, func(t *testing.T) {
			if got := IsTechCategory(tt.category); got != tt.want {
				t.Errorf("IsTechCategory(%q) = %v, want %v", tt.category, got, tt.want)
			}
		})
	}
}
