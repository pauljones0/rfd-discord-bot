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
		{"single word with description containing year", "Bravo", "2015 Fiat Bravo hatchback", "", false},
		{"single word with vague description", "Bravo", "nice car low km", "", true},
		{"single word with mileage only", "Bravo", "", "120000 km", true},
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

func TestIsLikelyNonCar(t *testing.T) {
	tests := []struct {
		title string
		want  bool
	}{
		// Should match — non-car keywords
		{"2023 Harley-Davidson Nightster", true},
		{"2020 Peterbilt 389", true},
		{"1984 Kawasaki KZ", true},
		{"2017 Can-Am Outlander 1000", true},
		{"2010 Gulf Streamlite", true},
		{"2026 Prime Time Manufacturing Avenger", true},
		{"2019 Jayco Jay Flight", true},
		{"Coachmen Catalina 2021", true},
		{"Forest River Rockwood", true},
		{"2018 Keystone Cougar", true},
		{"Polaris RZR 1000", true},
		// Should NOT match — real cars
		{"2019 Honda Civic", false},
		{"2022 Toyota Camry", false},
		{"2020 Ford F-150", false},
		{"2015 BMW X5", false},
	}
	for _, tt := range tests {
		t.Run(tt.title, func(t *testing.T) {
			if got := isLikelyNonCar(tt.title); got != tt.want {
				t.Errorf("isLikelyNonCar(%q) = %v, want %v", tt.title, got, tt.want)
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
