package notifier

import (
	"context"
	"encoding/json"
	"fmt"
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
		ThreadImageURL:     "https://example.com/image.jpg",
		PublishedTimestamp: time.Unix(1770954490, 0), // Stable timestamp for testing
		AIProcessed:        true,
		IsLavaHot:          true,
		Threads: []models.ThreadContext{
			{
				PostURL:      "https://forums.redflagdeals.com/deal-1",
				LikeCount:    10,
				CommentCount: 5,
				ViewCount:    100,
			},
		},
	}

	embed := formatDealToEmbed(deal)

	// Check Title format: "Title 🔥" (suffix added for hot deals)
	expectedTitle := deal.Title + " 🔥"
	if embed.Title != expectedTitle {
		t.Errorf("Title format incorrect. Got: %s, Want: %s", embed.Title, expectedTitle)
	}

	// Check URL (should prefer ActualDealURL)
	if embed.URL != deal.ActualDealURL {
		t.Errorf("URL incorrect. Got: %s, Want: %s", embed.URL, deal.ActualDealURL)
	}

	// Check Description (should contain RFD Thread link and Engagement Metrics)
	expectedDesc := fmt.Sprintf("[RFD](%s) \n\n👍 10  💬 5  👀 100", deal.Threads[0].PostURL)
	if embed.Description != expectedDesc {
		t.Errorf("Description incorrect.\nGot:  %q\nWant: %q", embed.Description, expectedDesc)
	}

	// Check Timestamp (should be set natively)
	expectedTimestamp := deal.PublishedTimestamp.Format(time.RFC3339)
	if embed.Timestamp != expectedTimestamp {
		t.Errorf("Timestamp incorrect. Got: %s, Want: %s", embed.Timestamp, expectedTimestamp)
	}

	// Check Fields (should be empty now since Engagement was moved)
	if len(embed.Fields) != 0 {
		t.Errorf("Expected 0 fields, got %d fields", len(embed.Fields))
	}
}

func TestFormatDealToEmbed_NegativeLikesStaysCold(t *testing.T) {
	tests := []struct {
		name      string
		likes     int
		comments  int
		views     int
		isLavaHot bool
		wantColor int
	}{
		{
			name:      "zero likes with high engagement stays cold",
			likes:     0,
			comments:  100,
			views:     5000,
			wantColor: colorColdDeal,
		},
		{
			name:      "negative likes with high engagement stays cold",
			likes:     -20,
			comments:  200,
			views:     10000,
			wantColor: colorColdDeal,
		},
		{
			name:      "positive likes with high engagement gets warm color",
			likes:     50,
			comments:  100,
			views:     500,
			wantColor: colorWarmDeal, // (50 + 200) / 500 = 0.5, well above 0.20 threshold
		},
		{
			name:      "negative likes but AI says hot gets hot color",
			likes:     -20,
			comments:  200,
			views:     10000,
			isLavaHot: true,
			wantColor: colorHotDeal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := models.DealInfo{
				Title:       "Test Deal",
				PostURL:     "https://forums.redflagdeals.com/test",
				AIProcessed: true,
				IsLavaHot:   tt.isLavaHot,
				Threads: []models.ThreadContext{
					{
						PostURL:      "https://forums.redflagdeals.com/test",
						LikeCount:    tt.likes,
						CommentCount: tt.comments,
						ViewCount:    tt.views,
					},
				},
			}

			embed := formatDealToEmbed(deal)

			if embed.Color != tt.wantColor {
				t.Errorf("Color = %d, want %d", embed.Color, tt.wantColor)
			}
		})
	}
}

func TestCalculateHeatScore(t *testing.T) {
	tests := []struct {
		name     string
		likes    int
		comments int
		views    int
		expected float64
	}{
		{"happy path", 10, 5, 100, 0.2},                // (10 + 2*5) / 100 = 0.2
		{"zero views", 10, 5, 0, 0.0},                  // returns 0.0
		{"negative likes clamped", -5, 5, 100, 0.1},    // (0 + 2*5) / 100 = 0.1
		{"negative comments clamped", 10, -5, 100, 0.1},// (10 + 0) / 100 = 0.1
		{"both negative", -10, -10, 100, 0.0},          // (0 + 0) / 100 = 0.0
		{"all zeros", 0, 0, 0, 0.0},                    // returns 0.0
		{"large numbers", 1000, 500, 10000, 0.2},       // (1000 + 1000) / 10000 = 0.2
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := CalculateHeatScore(tt.likes, tt.comments, tt.views)
			if score != tt.expected {
				t.Errorf("CalculateHeatScore(%d, %d, %d) = %v; expected %v", tt.likes, tt.comments, tt.views, score, tt.expected)
			}
		})
	}
}

func TestClient_IsHot(t *testing.T) {
	c := New("token")

	dealWithLikes := models.DealInfo{
		Threads: []models.ThreadContext{{LikeCount: 100, CommentCount: 100, ViewCount: 100}},
	}
	if c.IsHot(dealWithLikes) {
		t.Error("IsHot should no longer be driven by like counts")
	}

	dealLavaHot := models.DealInfo{
		IsLavaHot: true,
		Threads:   []models.ThreadContext{{LikeCount: -1, CommentCount: 0, ViewCount: 100}},
	}
	if !c.IsHot(dealLavaHot) {
		t.Error("IsHot should be true when AI thinks it's Lava Hot")
	}
}

func TestClient_Send(t *testing.T) {
	// Mock Discord Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("Expected POST request, got %s", r.Method)
		}
		if !strings.Contains(r.URL.Path, "/channels/67890/messages") {
			t.Errorf("Expected URL to be for channel messages, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bot token" {
			t.Errorf("Expected Bot token auth header")
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

	client := New("token")
	// Override rate limiter for tests to run fast
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1) // Inf usually doesn't work well with URL override in the mock anymore without hacking the domain
	// Actually we didn't mock the endpoint properly since it's hardcoded to discord.com! Let's just mock the HTTP Client Transport.

	deal := models.DealInfo{Title: "Test Deal", PostURL: "http://example.com", Threads: []models.ThreadContext{{LikeCount: 1}}}
	ctx := context.Background()

	// Need to override the URL in doRequest? In discord.go, the target URL is absolute.
	// Since we mock via client.Do override later, let's fix the test HTTP client.
	client.client = server.Client() // doesn't help with URL

	// Better approach for these tests is to mock the discord client HTTP transport to redirect requests to our server.
	client.client.Transport = &rewriteTransport{target: server.URL}

	subs := []models.Subscription{{ChannelID: "67890"}}
	ids, err := client.Send(ctx, deal, subs)
	if err != nil {
		t.Fatalf("Send() returned error: %v", err)
	}
	if ids["67890"] != "12345" {
		t.Errorf("Expected ID 12345, got %s", ids["67890"])
	}
}

// rewriteTransport redirects all requests to the given URL (useful for testing absolute URLs).
type rewriteTransport struct {
	target string
}

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(r.target, "http://")
	return http.DefaultTransport.RoundTrip(req)
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

	client := New("token")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}

	deal := models.DealInfo{
		Title:             "Updated Deal",
		PostURL:           "http://example.com",
		DiscordMessageIDs: map[string]string{"67890": messageID},
		Threads:           []models.ThreadContext{{LikeCount: 1}},
	}
	ctx := context.Background()

	err := client.Update(ctx, deal)
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

	client := New("token")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}

	deal := models.DealInfo{Title: "Retry Deal", PostURL: "http://example.com", Threads: []models.ThreadContext{{LikeCount: 1}}}
	ctx := context.Background()
	subs := []models.Subscription{{ChannelID: "67890"}}

	ids, err := client.Send(ctx, deal, subs)
	if err != nil {
		t.Fatalf("Send() should have succeeded after retries, got error: %v", err)
	}
	if ids["67890"] != "retry-success" {
		t.Errorf("Expected ID 'retry-success', got %s", ids["67890"])
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

	client := New("token")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}

	deal := models.DealInfo{Title: "Rate Limited Deal", PostURL: "http://example.com", Threads: []models.ThreadContext{{LikeCount: 1}}}
	ctx := context.Background()
	subs := []models.Subscription{{ChannelID: "67890"}}

	ids, err := client.Send(ctx, deal, subs)
	if err != nil {
		t.Fatalf("Send() should have succeeded after 429 retry, got error: %v", err)
	}
	if ids["67890"] != "429-success" {
		t.Errorf("Expected ID '429-success', got %s", ids["67890"])
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

	client := New("token")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}

	deal := models.DealInfo{Title: "Bad Deal", PostURL: "http://example.com", Threads: []models.ThreadContext{{LikeCount: 1}}}
	ctx := context.Background()
	subs := []models.Subscription{{ChannelID: "67890"}}

	ids, err := client.Send(ctx, deal, subs)
	if err != nil {
		t.Fatalf("Send() returned an unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Send() should have returned empty ID map for 400 response, got %v", ids)
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

func TestClient_Send_EmptyToken(t *testing.T) {
	c := New("")
	subs := []models.Subscription{{ChannelID: "67890"}}
	ids, err := c.Send(context.Background(), models.DealInfo{Title: "Test Deal"}, subs)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("Send() with empty token should return empty map, got %v", ids)
	}
}
