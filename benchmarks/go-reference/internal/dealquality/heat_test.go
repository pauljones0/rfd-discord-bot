package dealquality

import (
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"testing"
)

func TestHeatScore(t *testing.T) {
	tests := []struct {
		name     string
		likes    int
		comments int
		views    int
		want     float64
	}{
		{"zero views returns 0", 10, 5, 0, 0.0},
		{"basic engagement", 10, 5, 100, 0.20},
		{"high engagement", 50, 100, 500, 0.50},
		{"low engagement", 2, 1, 1000, 0.004},
		{"negative likes clamped", -10, 5, 100, 0.10},
		{"negative comments clamped", 10, -5, 100, 0.10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HeatScore(tt.likes, tt.comments, tt.views)
			if got != tt.want {
				t.Errorf("HeatScore(%d, %d, %d) = %f, want %f",
					tt.likes, tt.comments, tt.views, got, tt.want)
			}
		})
	}
}

func TestIsWarm(t *testing.T) {
	tests := []struct {
		name     string
		likes    int
		comments int
		views    int
		hasViews bool
		want     bool
	}{
		{"warm: likes>=2 and score>0.05", 10, 5, 100, true, true},
		{"cold: likes<2", 1, 100, 100, true, false},
		{"cold: score<=0.05", 2, 0, 1000, true, false},
		{"cold: exactly 0.05", 5, 0, 100, true, false},
		{"warm: no views exactly 15", 3, 6, 0, false, true},
		{"warm: exactly at floor", 2, 2, 50, true, true},
		{"warm: no views fallback", 20, 4, 0, false, true},
		{"cold: no views fallback below threshold", 3, 4, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := models.DealInfo{
				Threads: []models.ThreadContext{
					{LikeCount: tt.likes, CommentCount: tt.comments, ViewCount: tt.views, ViewCountAvailable: tt.hasViews},
				},
			}
			if got := IsWarm(deal); got != tt.want {
				t.Errorf("IsWarm() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsHot(t *testing.T) {
	tests := []struct {
		name     string
		likes    int
		comments int
		views    int
		hasViews bool
		want     bool
	}{
		{"hot: score>0.20", 50, 100, 500, true, true},
		{"not hot: score<=0.20", 10, 5, 100, true, false},
		{"not hot: likes<2", 1, 500, 100, true, false},
		{"hot: no views fallback", 40, 0, 0, false, true},
		{"not hot: no views fallback below threshold", 20, 4, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := models.DealInfo{
				Threads: []models.ThreadContext{
					{LikeCount: tt.likes, CommentCount: tt.comments, ViewCount: tt.views, ViewCountAvailable: tt.hasViews},
				},
			}
			if got := IsHot(deal); got != tt.want {
				t.Errorf("IsHot() = %v, want %v", got, tt.want)
			}
		})
	}
}
