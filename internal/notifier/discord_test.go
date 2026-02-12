package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/time/rate"

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

	if len(embed.Fields) != 0 {
		t.Errorf("Fields should be empty, got %d fields", len(embed.Fields))
	}
}

func TestClient_Send(t *testing.T) {
	// Mock Discord Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if r.URL.Query().Get("wait") != "true" {
			t.Errorf("Expected wait=true query param")
		}
		
		// Verify payload
		var payload discordWebhookPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}
		if len(payload.Embeds) != 1 {
			t.Errorf("Expected 1 embed, got %d", len(payload.Embeds))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "12345", "channel_id": "67890"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	// Override rate limiter for tests to run fast
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)

	deal := models.DealInfo{Title: "Test Deal", PostURL: "http://example.com"}
	ctx := context.Background()
	
	id, err := client.Send(ctx, deal)
	if err != nil {
		t.Fatalf("Send() returned error: %v", err)
	}
	if id != "12345" {
		t.Errorf("Expected ID 12345, got %s", id)
	}
}

func TestClient_Update(t *testing.T) {
	messageID := "12345"
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" {
			t.Errorf("Expected PATCH request, got %s", r.Method)
		}
		// Verify URL contains message ID
		if !strings.Contains(r.URL.Path, "/messages/"+messageID) {
			t.Errorf("URL %s does not contain message ID %s", r.URL.Path, messageID)
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "12345"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)

	deal := models.DealInfo{Title: "Updated Deal", PostURL: "http://example.com"}
	ctx := context.Background()

	err := client.Update(ctx, messageID, deal)
	if err != nil {
		t.Fatalf("Update() returned error: %v", err)
	}
}
