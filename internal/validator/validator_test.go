package validator

import (
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestValidator_ValidateStruct(t *testing.T) {
	v := New()

	tests := []struct {
		name    string
		deal    models.DealInfo
		wantErr bool
	}{
		{
			name: "Valid Deal",
			deal: models.DealInfo{
				Title:              "Test Deal",
				PostURL:            "https://example.com/deal",
				PublishedTimestamp: time.Now(),
				LikeCount:          10,
				CommentCount:       5,
				ViewCount:          100,
			},
			wantErr: false,
		},
		{
			name: "Missing Title",
			deal: models.DealInfo{
				PostURL:            "https://example.com/deal",
				PublishedTimestamp: time.Now(),
			},
			wantErr: true,
		},
		{
			name: "Invalid Post URL",
			deal: models.DealInfo{
				Title:              "Test Deal",
				PostURL:            "invalid-url",
				PublishedTimestamp: time.Now(),
			},
			wantErr: true,
		},
		{
			name: "Negative Likes",
			deal: models.DealInfo{
				Title:              "Test Deal",
				PostURL:            "https://example.com/deal",
				PublishedTimestamp: time.Now(),
				LikeCount:          -1,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := v.ValidateStruct(tt.deal); (err != nil) != tt.wantErr {
				t.Errorf("ValidateStruct() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
