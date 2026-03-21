package models

import "testing"

func TestIsCarfaxEligible(t *testing.T) {
	tests := []struct {
		vehicleType string
		want        bool
	}{
		{"car", true},
		{"truck", true},
		{"suv", true},
		{"van", true},
		{"", true},
		{"motorcycle", false},
		{"boat", false},
		{"atv", false},
		{"trailer", false},
		{"other", false},
	}
	for _, tt := range tests {
		t.Run(tt.vehicleType, func(t *testing.T) {
			c := &CarData{VehicleType: tt.vehicleType}
			if got := c.IsCarfaxEligible(); got != tt.want {
				t.Errorf("CarData{VehicleType: %q}.IsCarfaxEligible() = %v, want %v", tt.vehicleType, got, tt.want)
			}
		})
	}
}
