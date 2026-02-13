package notifier

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
		AuthorName:         "testuser",
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

	// Check Engagement Field
	if len(embed.Fields) != 2 {
		t.Errorf("Expected 2 fields (Posted By + Engagement), got %d fields", len(embed.Fields))
	}

	foundEngagement := false
	for _, field := range embed.Fields {
		if field.Name == "Engagement" {
			foundEngagement = true
			expectedValue := "👍 10  💬 5  👀 100"
			if field.Value != expectedValue {
				t.Errorf("Engagement field value incorrect. Got: %s, Want: %s", field.Value, expectedValue)
			}
		}
	}
	if !foundEngagement {
		t.Error("Engagement field not found")
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

func TestClient_Send_RetriesOn5xx(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"message": "server error"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "retry-success", "channel_id": "67890"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)

	deal := models.DealInfo{Title: "Retry Deal", PostURL: "http://example.com"}
	ctx := context.Background()

	id, err := client.Send(ctx, deal)
	if err != nil {
		t.Fatalf("Send() should have succeeded after retries, got error: %v", err)
	}
	if id != "retry-success" {
		t.Errorf("Expected ID 'retry-success', got %s", id)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("Expected 3 attempts (2 failures + 1 success), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestClient_Send_RetriesOn429(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := atomic.AddInt32(&attempts, 1)
		if attempt == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message": "rate limited"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "429-success", "channel_id": "67890"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)

	deal := models.DealInfo{Title: "Rate Limited Deal", PostURL: "http://example.com"}
	ctx := context.Background()

	id, err := client.Send(ctx, deal)
	if err != nil {
		t.Fatalf("Send() should have succeeded after 429 retry, got error: %v", err)
	}
	if id != "429-success" {
		t.Errorf("Expected ID '429-success', got %s", id)
	}
}

func TestClient_Send_NoRetryOn4xx(t *testing.T) {
	var attempts int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"message": "bad request"}`))
	}))
	defer server.Close()

	client := New(server.URL)
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)

	deal := models.DealInfo{Title: "Bad Deal", PostURL: "http://example.com"}
	ctx := context.Background()

	_, err := client.Send(ctx, deal)
	if err == nil {
		t.Fatal("Send() should have returned error for 400 response")
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("Expected 1 attempt (no retry for 400), got %d", atomic.LoadInt32(&attempts))
	}
}

func TestRetryBackoff(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		retryAfter string
		attempt    int
		wantZero   bool
	}{
		{"429 with Retry-After", 429, "2", 0, false},
		{"429 without Retry-After", 429, "", 0, false},
		{"500 error", 500, "", 0, false},
		{"503 error", 503, "", 1, false},
		{"400 error", 400, "", 0, true},
		{"404 error", 404, "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.statusCode,
				Header:     http.Header{},
			}
			if tt.retryAfter != "" {
				resp.Header.Set("Retry-After", tt.retryAfter)
			}

			backoff := retryBackoff(resp, tt.attempt)
			if tt.wantZero && backoff != 0 {
				t.Errorf("Expected zero backoff for status %d, got %v", tt.statusCode, backoff)
			}
			if !tt.wantZero && backoff == 0 {
				t.Errorf("Expected non-zero backoff for status %d, got 0", tt.statusCode)
			}
		})
	}
}

func TestClient_Send_EmptyWebhookURL(t *testing.T) {
	c := New("")
	id, err := c.Send(context.Background(), models.DealInfo{Title: "Test Deal"})
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if id != "" {
		t.Errorf("Send() with empty webhook should return empty ID, got %q", id)
	}
}
