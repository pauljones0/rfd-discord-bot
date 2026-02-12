package notifier

import (
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestFormatDealToEmbed(t *testing.T) {
	deal := models.DealInfo{
		Title:              "Great Deal",
		PostURL:            "https://forums.redflagdeals.com/deal-1",
		ActualDealURL:      "https://amazon.ca/item",
		LikeCount:          10,
		CommentCount:       5,
		ViewCount:          100,
		ThreadImageURL:     "https://example.com/image.jpg",
		PublishedTimestamp: time.Now(),
	}

	embed := formatDealToEmbed(deal)

	// Check Title format: "Title (L/C/V)"
	expectedTitleSuffix := " (10/5/100)"
	if embed.Title != deal.Title+expectedTitleSuffix {
		t.Errorf("Title format incorrect. Got: %s, Want suffix: %s", embed.Title, expectedTitleSuffix)
	}

	// Check URL (should be PostURL)
	if embed.URL != deal.PostURL {
		t.Errorf("URL incorrect. Got: %s, Want: %s", embed.URL, deal.PostURL)
	}

	// Check Description (should contain Item Link)
	expectedDesc := "[Link to Item](https://amazon.ca/item)"
	if embed.Description != expectedDesc {
		t.Errorf("Description incorrect. Got: %s, Want: %s", embed.Description, expectedDesc)
	}

	// Check Fields (should be empty)
	if len(embed.Fields) != 0 {
		t.Errorf("Fields should be empty, got %d fields", len(embed.Fields))
	}
}
