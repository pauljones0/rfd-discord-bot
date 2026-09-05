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
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/time/rate"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestFormatDealToEmbed(t *testing.T) {
	deal := models.DealInfo{
		Title:              "Great Deal",
		PostURL:            "https://forums.redflagdeals.com/deal-1",
		ActualDealURL:      "https://amazon.ca/item",
		ThreadImageURL:     "https://example.com/image.jpg",
		Savings:            "Save 50%",
		PublishedTimestamp: time.Unix(1770954490, 0), // Stable timestamp for testing
		HasBeenHot:         true,
		Threads: []models.ThreadContext{
			{
				PostURL:            "https://forums.redflagdeals.com/deal-1",
				LikeCount:          10,
				CommentCount:       5,
				ViewCount:          100,
				ViewCountAvailable: true,
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

}

func TestFormatDealToEmbed_FallsBackToPostURLWhenActualDealURLInvalid(t *testing.T) {
	deal := models.DealInfo{
		Title:         "Great Deal",
		PostURL:       "https://forums.redflagdeals.com/deal-1",
		ActualDealURL: "javascript:void(0)",
		Threads: []models.ThreadContext{
			{
				PostURL:            "https://forums.redflagdeals.com/deal-1",
				LikeCount:          10,
				CommentCount:       5,
				ViewCount:          100,
				ViewCountAvailable: true,
			},
		},
	}

	embed := formatDealToEmbed(deal)
	if embed.URL != deal.PostURL {
		t.Fatalf("URL incorrect. Got: %s, Want fallback: %s", embed.URL, deal.PostURL)
	}
}

func TestFormatDealToEmbed_FallsBackWhenActualDealURLContainsSpaces(t *testing.T) {
	deal := models.DealInfo{
		Title:         "Great Deal",
		PostURL:       "https://forums.redflagdeals.com/deal-1",
		ActualDealURL: "https://example.com/not encoded",
		Threads: []models.ThreadContext{
			{
				PostURL:            "https://forums.redflagdeals.com/deal-1",
				LikeCount:          10,
				CommentCount:       5,
				ViewCount:          100,
				ViewCountAvailable: true,
			},
		},
	}

	embed := formatDealToEmbed(deal)
	if embed.URL != deal.PostURL {
		t.Fatalf("URL incorrect. Got: %s, Want fallback: %s", embed.URL, deal.PostURL)
	}
}

func TestFormatDealToEmbed_OmitsViewsWhenUnavailable(t *testing.T) {
	deal := models.DealInfo{
		Title:   "Great Deal",
		PostURL: "https://forums.redflagdeals.com/deal-1",
		Threads: []models.ThreadContext{
			{
				PostURL:      "https://forums.redflagdeals.com/deal-1",
				LikeCount:    13,
				CommentCount: 10,
			},
		},
	}

	embed := formatDealToEmbed(deal)
	expectedDesc := "[RFD](https://forums.redflagdeals.com/deal-1) \n\n👍 13  💬 10"
	if embed.Description != expectedDesc {
		t.Fatalf("Description incorrect.\nGot:  %q\nWant: %q", embed.Description, expectedDesc)
	}
}

func TestFormatDealToEmbed_Footer(t *testing.T) {
	tests := []struct {
		name       string
		category   string
		retailer   string
		wantFooter string
	}{
		{
			name:       "Category and Retailer",
			category:   "Sports & Fitness",
			retailer:   "Walmart.ca",
			wantFooter: "⚽ Walmart.ca",
		},
		{
			name:       "Only Category",
			category:   "Sports & Fitness",
			retailer:   "",
			wantFooter: "⚽",
		},
		{
			name:       "Only Retailer",
			category:   "",
			retailer:   "Amazon.ca",
			wantFooter: "Amazon.ca",
		},
		{
			name:       "Neither",
			category:   "",
			retailer:   "",
			wantFooter: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := models.DealInfo{
				Category: tt.category,
				Retailer: tt.retailer,
			}
			embed := formatDealToEmbed(deal)
			if embed.Footer.Text != tt.wantFooter {
				t.Errorf("Footer.Text = %q, want %q", embed.Footer.Text, tt.wantFooter)
			}
		})
	}
}

func TestFormatDealToEmbed_Colors(t *testing.T) {
	tests := []struct {
		name        string
		hasBeenWarm bool
		hasBeenHot  bool
		likes       int
		comments    int
		views       int
		hasViews    bool
		wantColor   int
	}{
		{
			name:  "cold deal - low engagement",
			likes: 1, comments: 0, views: 100, hasViews: true,
			wantColor: colorColdDeal,
		},
		{
			name:        "warm deal via HasBeenWarm flag",
			hasBeenWarm: true,
			likes:       0, comments: 0, views: 100, hasViews: true,
			wantColor: colorWarmDeal,
		},
		{
			name:  "warm deal via live score",
			likes: 10, comments: 5, views: 100, hasViews: true,
			wantColor: colorWarmDeal,
		},
		{
			name:  "warm deal via no-views fallback",
			likes: 20, comments: 4,
			wantColor: colorWarmDeal,
		},
		{
			name:       "hot deal via HasBeenHot flag",
			hasBeenHot: true,
			likes:      0, comments: 0, views: 100, hasViews: true,
			wantColor: colorHotDeal,
		},
		{
			name:  "hot deal via live score",
			likes: 50, comments: 100, views: 500, hasViews: true,
			wantColor: colorHotDeal,
		},
		{
			name:  "hot deal via no-views fallback",
			likes: 40, comments: 0,
			wantColor: colorHotDeal,
		},
		{
			name:        "hot overrides warm",
			hasBeenWarm: true, hasBeenHot: true,
			wantColor: colorHotDeal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deal := models.DealInfo{
				Title:       "Test Deal",
				PostURL:     "https://forums.redflagdeals.com/test",
				Savings:     "Save 50%",
				HasBeenWarm: tt.hasBeenWarm,
				HasBeenHot:  tt.hasBeenHot,
				Threads: []models.ThreadContext{
					{
						PostURL:            "https://forums.redflagdeals.com/test",
						LikeCount:          tt.likes,
						CommentCount:       tt.comments,
						ViewCount:          tt.views,
						ViewCountAvailable: tt.hasViews,
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

func TestFormatDealToEmbed_DoesNotHeatNonDiscountedRFDDeal(t *testing.T) {
	deal := models.DealInfo{
		Title:      "Launch price console",
		PostURL:    "https://forums.redflagdeals.com/test",
		Price:      "$999.99",
		HasBeenHot: true,
		Threads: []models.ThreadContext{
			{
				PostURL:            "https://forums.redflagdeals.com/test",
				LikeCount:          100,
				CommentCount:       50,
				ViewCount:          500,
				ViewCountAvailable: true,
			},
		},
	}

	embed := formatDealToEmbed(deal)
	if strings.Contains(embed.Title, "🔥") {
		t.Fatalf("title has hot marker for non-discount deal: %q", embed.Title)
	}
	if embed.Color != colorColdDeal {
		t.Fatalf("Color = %d, want cold for non-discount deal", embed.Color)
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
		var payload discordMessagePayload
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

func TestClient_Send_UsesPostURLFallbackWhenActualDealURLInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload discordMessagePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("Failed to decode request body: %v", err)
		}
		if len(payload.Embeds) != 1 {
			t.Fatalf("Expected 1 embed, got %d", len(payload.Embeds))
		}
		if payload.Embeds[0].URL != "https://forums.redflagdeals.com/deal-1" {
			t.Fatalf("Embed URL = %q, want fallback thread URL", payload.Embeds[0].URL)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"id": "12345", "channel_id": "67890"}`))
	}))
	defer server.Close()

	client := New("token")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}

	deal := models.DealInfo{
		Title:         "Test Deal",
		PostURL:       "https://forums.redflagdeals.com/deal-1",
		ActualDealURL: "javascript:void(0)",
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/deal-1", LikeCount: 1},
		},
	}

	ids, err := client.Send(context.Background(), deal, []models.Subscription{{ChannelID: "67890"}})
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
	if err == nil {
		t.Fatal("Send() swallowed the channel failure")
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

func TestRenderedTitleBoundsPreserveUnicodeAndStoredSource(t *testing.T) {
	for _, tc := range []struct {
		name, raw, clean string
		hot, truncated   bool
	}{
		{"long raw", strings.Repeat("Café 📱 ", 80), "", false, true},
		{"long cleanup", "Original title", strings.Repeat("AI 📱 ", 80), false, true},
		{"hot suffix crosses limit", strings.Repeat("é", 254), "", true, true},
		{"exact ASCII limit", strings.Repeat("a", 256), "", false, false},
		{"exact emoji limit", strings.Repeat("📱", 128), "", false, false},
		{"normal hot title", "A discounted phone", "Phone 📱", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deal := models.DealInfo{Title: tc.raw, CleanTitle: tc.clean, HasBeenHot: tc.hot,
				Savings: "50% off", PostURL: "https://forums.redflagdeals.com/thread-123",
				ActualDealURL: "https://example.com/product"}
			embed := formatDealToEmbed(deal)
			if !utf8.ValidString(embed.Title) || len(utf16.Encode([]rune(embed.Title))) > 256 {
				t.Fatalf("invalid or oversized rendered title: %q", embed.Title)
			}
			expected := tc.raw
			if tc.clean != "" {
				expected = tc.clean
			}
			if tc.hot {
				expected += " 🔥"
			}
			if tc.truncated {
				if !strings.HasSuffix(embed.Title, "…") || !strings.HasPrefix(expected, strings.TrimSuffix(embed.Title, "…")) {
					t.Fatalf("truncation corrupted the title: %q", embed.Title)
				}
			} else if embed.Title != expected {
				t.Fatalf("in-limit title changed: %q", embed.Title)
			}
			if deal.Title != tc.raw || deal.CleanTitle != tc.clean || embed.URL != deal.ActualDealURL {
				t.Fatal("rendering changed stored source or destination URL")
			}
		})
	}
}
