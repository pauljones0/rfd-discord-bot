package facebook

import "testing"

func TestIsTooVague(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		description string
		mileage     string
		want        bool
	}{
		{"single word no info", "Bravo", "", "", true},
		{"single word with spaces", "  Bravo  ", "", "", true},
		{"single word with description", "Bravo", "2015 Fiat Bravo hatchback", "", false},
		{"single word with mileage", "Bravo", "", "120000 km", false},
		{"single word with digits", "F150", "", "", false},
		{"two words no info", "Honda Civic", "", "", false},
		{"normal listing", "2019 Honda Civic", "", "", false},
		{"year only", "2019", "", "", false},
		{"empty title", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTooVague(tt.title, tt.description, tt.mileage)
			if got != tt.want {
				t.Errorf("isTooVague(%q, %q, %q) = %v, want %v",
					tt.title, tt.description, tt.mileage, got, tt.want)
			}
		})
	}
}

func TestHasDigit(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"Bravo", false},
		{"F150", true},
		{"2019 Honda", true},
		{"", false},
		{"abc123", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := hasDigit(tt.input); got != tt.want {
				t.Errorf("hasDigit(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
