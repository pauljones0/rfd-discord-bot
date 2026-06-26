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

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/crux"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestFormatDealToEmbed(t *testing.T) {
	deal := models.DealInfo{
		Title:              "Great Deal",
		PostURL:            "https://forums.redflagdeals.com/deal-1",
		ActualDealURL:      "https://amazon.ca/item",
		ThreadImageURL:     "https://example.com/image.jpg",
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

	// Check Fields (should be empty now since Engagement was moved)
	if len(embed.Fields) != 0 {
		t.Errorf("Expected 0 fields, got %d fields", len(embed.Fields))
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

func TestCreateOnEveryCornerAlertPayloadUsesCompactPostText(t *testing.T) {
	alert := models.OnEveryCornerAlert{
		Kind:             models.OnEveryCornerAlertCorner,
		MatchName:        "Austria v Jordan",
		TweetText:        "@Enterprise #OnEveryCorner #Sweepstakes",
		TweetURL:         "https://x.com/intent/tweet?text=%40Enterprise+%23OnEveryCorner+%23Sweepstakes",
		VariantTweetText: "@Enterprise #OnEveryCorner #Jeu Austria ⚽🔥",
		VariantTweetURL:  "https://x.com/intent/tweet?text=%40Enterprise+%23OnEveryCorner+%23Jeu+Austria+%E2%9A%BD%F0%9F%94%A5",
		ReceivedAt:       time.Date(2026, 6, 17, 4, 14, 8, 0, time.UTC),
	}

	payload := createOnEveryCornerAlertPayload(alert)

	if payload.Content != alert.VariantTweetText {
		t.Fatalf("content = %q, want compact post text %q", payload.Content, alert.VariantTweetText)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(payload.Embeds))
	}
	if payload.AllowedMentions == nil || len(payload.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed mentions = %#v, want no parsed mentions", payload.AllowedMentions)
	}
	embed := payload.Embeds[0]
	if !strings.Contains(embed.Description, "X post: [Open compose](") {
		t.Fatalf("description missing compose link: %q", embed.Description)
	}
	if !strings.Contains(embed.Description, "Text: @Enterprise #OnEveryCorner #Jeu Austria") {
		t.Fatalf("description missing compact post text: %q", embed.Description)
	}
	if strings.Contains(embed.Description, "Safe entry:") || strings.Contains(embed.Description, "Varied entry:") || strings.Contains(embed.Description, "Varied text:") {
		t.Fatalf("description still includes old safe/varied split: %q", embed.Description)
	}
	if len(embed.Fields) != 0 {
		t.Fatalf("fields = %#v, want compact embed without fields", embed.Fields)
	}
}

func TestFormatOnEveryCornerAlertEmbedUsesTweetDefaults(t *testing.T) {
	embed := formatOnEveryCornerAlertEmbed(models.OnEveryCornerAlert{
		Kind:      models.OnEveryCornerAlertCorner,
		MatchName: "Austria v Jordan",
	})

	if !strings.Contains(embed.Description, "https://x.com/intent/tweet?text=%40Enterprise+%23OnEveryCorner+%23Sweepstakes") {
		t.Fatalf("description missing default compose URL: %q", embed.Description)
	}
	if !strings.Contains(embed.Description, "Match: Austria v Jordan") {
		t.Fatalf("description missing match: %q", embed.Description)
	}
	if len(embed.Fields) != 0 {
		t.Fatalf("fields = %#v, want compact embed without fields", embed.Fields)
	}
}

func TestFormatCoreAlertEmbedShowsTotalAndSampleObservationCounts(t *testing.T) {
	embed := formatCoreAlertEmbed(models.CoreAlert{
		StoreNames: []string{"Amazon CA"},
		Links:      []string{"https://example.com/item"},
		FiredAt:    time.Date(2026, 6, 22, 20, 0, 0, 0, time.UTC),
		Deal: models.CoreDeal{
			ProductName:      "Test Product",
			StoreName:        "Amazon CA",
			Category:         "test",
			PriceCAD:         20,
			MinPriceSeen:     20,
			P25PriceSeen:     100,
			P50PriceSeen:     100,
			HistoryCount:     10251,
			PriceSampleCount: 10000,
			AnomalyType:      "Price Error / Used",
			ReceivedAt:       time.Date(2026, 6, 22, 20, 0, 0, 0, time.UTC),
		},
	})

	if !strings.Contains(embed.Description, "Total price observations: **10251** (latest **10000** historical observations used for stats)") {
		t.Fatalf("description missing total/sample observation counts: %q", embed.Description)
	}
}

func TestCreateOnEveryCornerSystemAlertPayload(t *testing.T) {
	payload := createOnEveryCornerAlertPayload(models.OnEveryCornerAlert{
		Kind:           models.OnEveryCornerAlertSystem,
		RawTitle:       "OnEveryCorner Scoremer recovery attempted",
		SystemSeverity: "warning",
		SystemDetails:  "Scoremer polling is unhealthy; attempting to reload the browser page.",
		SystemFields: []models.CoreSystemAlertField{
			{Name: "Attempted fix", Value: "page.reload"},
			{Name: "Status", Value: "403"},
		},
		ReceivedAt: time.Date(2026, 6, 19, 20, 40, 0, 0, time.UTC),
	})

	if payload.Content != "" {
		t.Fatalf("content = %q, want empty system alert content", payload.Content)
	}
	if payload.AllowedMentions == nil || len(payload.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed mentions = %#v, want no parsed mentions", payload.AllowedMentions)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(payload.Embeds))
	}
	embed := payload.Embeds[0]
	if embed.Title != "OnEveryCorner Scoremer recovery attempted" {
		t.Fatalf("title = %q", embed.Title)
	}
	if !strings.Contains(embed.Description, "Scoremer polling is unhealthy") {
		t.Fatalf("description = %q", embed.Description)
	}
	if len(embed.Fields) < 3 {
		t.Fatalf("fields = %#v, want severity plus status fields", embed.Fields)
	}
	if embed.Footer.Text != "OnEveryCorner system" {
		t.Fatalf("footer = %q", embed.Footer.Text)
	}
}

func TestCreateCruxSystemAlertPayload(t *testing.T) {
	payload := createCruxSystemAlertPayload(crux.SystemAlert{
		Title:      "Crux Investor monitor failure",
		Severity:   "error",
		Component:  "crux-monitor",
		Details:    "failed to fetch crux page 1: http=parse:crux company list not found",
		OccurredAt: time.Date(2026, 6, 26, 20, 40, 0, 0, time.UTC),
		Fields: []crux.SystemAlertField{
			{Name: "Automatic handling", Value: "The scheduler will retry on the next poll."},
			{Name: "Alert suppression", Value: "Matching failures are suppressed for 1h0m0s."},
		},
	})

	if payload.Content != "" {
		t.Fatalf("content = %q, want empty system alert content", payload.Content)
	}
	if payload.AllowedMentions == nil || len(payload.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed mentions = %#v, want no parsed mentions", payload.AllowedMentions)
	}
	if len(payload.Embeds) != 1 {
		t.Fatalf("embeds = %d, want 1", len(payload.Embeds))
	}
	embed := payload.Embeds[0]
	if embed.Title != "Crux Investor monitor failure" {
		t.Fatalf("title = %q", embed.Title)
	}
	if !strings.Contains(embed.Description, "Severity: **ERROR**") || !strings.Contains(embed.Description, "crux company list not found") {
		t.Fatalf("description = %q", embed.Description)
	}
	if len(embed.Fields) < 3 {
		t.Fatalf("fields = %#v, want occurred plus status fields", embed.Fields)
	}
	if embed.Footer.Text != "Crux Investor system" {
		t.Fatalf("footer = %q", embed.Footer.Text)
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

func TestCalculateHeatScore(t *testing.T) {
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
			got := CalculateHeatScore(tt.likes, tt.comments, tt.views)
			if got != tt.want {
				t.Errorf("CalculateHeatScore(%d, %d, %d) = %f, want %f",
					tt.likes, tt.comments, tt.views, got, tt.want)
			}
		})
	}
}

func TestClient_IsWarm(t *testing.T) {
	c := New("token")
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
			if got := c.IsWarm(deal); got != tt.want {
				t.Errorf("IsWarm() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestClient_IsHot(t *testing.T) {
	c := New("token")
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
			if got := c.IsHot(deal); got != tt.want {
				t.Errorf("IsHot() = %v, want %v", got, tt.want)
			}
		})
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

func TestClient_Send_UsesPostURLFallbackWhenActualDealURLInvalid(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload discordWebhookPayload
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

func TestFormatEbayEmbed_CompactMobile(t *testing.T) {
	listedAt := time.Date(2026, time.April, 14, 12, 30, 0, 0, time.UTC)
	item := ebay.EbayItem{
		ItemID:                   "123456789012",
		Title:                    "Lenovo ThinkPad T14 Gen 3",
		CurrentPrice:             349.99,
		PreviousPrice:            499.99,
		PriceDrop:                150.00,
		PercentDrop:              30.0,
		DropCount:                2,
		Currency:                 "CAD",
		ItemURL:                  "https://www.ebay.ca/itm/123456789012",
		ImageURL:                 "https://example.com/item.jpg",
		Seller:                   "vipoutletcanada",
		SellerFeedbackScore:      12345,
		SellerFeedbackPercentage: "99.4%",
		Condition:                "Certified Refurbished",
		Marketplace:              "EBAY_CA",
		ListedAt:                 listedAt,
	}

	embed := formatEbayEmbed(item)

	wantDesc := fmt.Sprintf("~~C$499.99~~ -> **C$349.99**  (-C$150.00, -30%%)  •  2nd drop\n[vipoutletcanada](https://www.ebay.ca/usr/vipoutletcanada) 99.4%%/12.3k  •  Certified Refurbished  •  Listed <t:%d:f>", listedAt.Unix())
	if embed.Description != wantDesc {
		t.Fatalf("Description mismatch.\nGot:  %q\nWant: %q", embed.Description, wantDesc)
	}
	if embed.URL != "https://www.ebay.ca/itm/123456789012?mkcid=1&mkrid=706-53473-19255-0&siteid=2&campid=5339131483&customid=&toolid=10001&mkevt=1" {
		t.Fatalf("URL = %q, want affiliate-formatted eBay CA URL", embed.URL)
	}
	if embed.Footer.Text != "eBay Canada • Price Drop Alert" {
		t.Fatalf("Footer.Text = %q, want %q", embed.Footer.Text, "eBay Canada • Price Drop Alert")
	}
	if embed.Timestamp != "" {
		t.Fatalf("Timestamp = %q, want empty unlabeled timestamp", embed.Timestamp)
	}
	if embed.Thumbnail.URL != item.ImageURL {
		t.Fatalf("Thumbnail.URL = %q, want %q", embed.Thumbnail.URL, item.ImageURL)
	}
}

func TestFormatEbayEmbed_MarketplaceFallbackFromItemURL(t *testing.T) {
	item := ebay.EbayItem{
		Title:     "Steam Deck OLED",
		ItemURL:   "https://www.ebay.com/itm/Valve-Steam-Deck-OLED/555555555555?hash=item",
		Seller:    "vipoutlet",
		Condition: "Open box",
	}

	embed := formatEbayEmbed(item)

	if embed.URL != "https://www.ebay.com/itm/555555555555?mkcid=1&mkrid=711-53200-19255-0&siteid=0&campid=5339131483&customid=&toolid=10001&mkevt=1" {
		t.Fatalf("URL = %q, want affiliate-formatted eBay US URL", embed.URL)
	}
	if embed.Footer.Text != "eBay US • Price Drop Alert" {
		t.Fatalf("Footer.Text = %q, want %q", embed.Footer.Text, "eBay US • Price Drop Alert")
	}
	if !strings.Contains(embed.Description, "https://www.ebay.com/usr/vipoutlet") {
		t.Fatalf("Description = %q, expected seller link to use ebay.com", embed.Description)
	}
}

func TestFormatEbayEmbed_CouponIsQuiet(t *testing.T) {
	item := ebay.EbayItem{
		Title:          "Dell Latitude",
		CurrentPrice:   180,
		PreviousPrice:  240,
		PriceDrop:      60,
		PercentDrop:    25,
		CouponDiscount: 20,
		CouponCode:     "SAVE20",
		CouponSource:   "page:paid-trial",
		Currency:       "CAD",
		ItemURL:        "https://www.ebay.ca/itm/123456789012",
	}

	embed := formatEbayEmbed(item)

	if !strings.Contains(embed.Description, "coupon included") {
		t.Fatalf("Description = %q, expected compact coupon marker", embed.Description)
	}
	if strings.Contains(embed.Description, "SAVE20") || strings.Contains(embed.Description, "page:") || strings.Contains(embed.Description, "after C$20.00 coupon") {
		t.Fatalf("Description = %q, coupon details should stay out of the compact embed", embed.Description)
	}
}

func TestFormatBestBuyEmbedPriceDrop(t *testing.T) {
	embed := formatBestBuyEmbed(bestbuy.AnalyzedProduct{
		Product: bestbuy.Product{
			SKU:          "111",
			Name:         "OLED TV",
			URL:          "https://www.bestbuy.ca/en-ca/product/111",
			RegularPrice: 1000,
			SalePrice:    700,
			SellerName:   "Tech Outlet Center",
			Source:       "seller:591375",
		},
		CleanTitle:            "OLED TV",
		IsWarm:                true,
		InitialEffectivePrice: 1000,
		PriceDropAmount:       300,
		PriceDropPct:          30,
		AlertKind:             bestbuy.AlertKindPriceDrop,
	})

	if embed.Footer.Text != "Best Buy Price Drop" {
		t.Fatalf("Footer.Text = %q, want Best Buy Price Drop", embed.Footer.Text)
	}
	if len(embed.Fields) < 2 {
		t.Fatalf("expected AI label and price fields, got %#v", embed.Fields)
	}
	if embed.Fields[0].Value != "Warm price drop" {
		t.Fatalf("AI label = %q", embed.Fields[0].Value)
	}
	if embed.Fields[1].Name != "Price Drop" || !strings.Contains(embed.Fields[1].Value, "$300.00 / 30% drop") {
		t.Fatalf("price field = %#v", embed.Fields[1])
	}
}

func TestFormatBestBuyEmbedShowsCompFloor(t *testing.T) {
	embed := formatBestBuyEmbed(bestbuy.AnalyzedProduct{
		Product: bestbuy.Product{
			SKU:                   "16554105",
			Name:                  "Apple Watch Series 8",
			URL:                   "https://www.bestbuy.ca/en-ca/product/16554105",
			RegularPrice:          529.99,
			SalePrice:             229.99,
			SellerName:            "OpenBox",
			CategoryName:          "Apple Watch",
			ComparableCount:       2,
			ComparableMedianPrice: 1111.495,
			ComparableP25Price:    667.74125,
			ComparableLowestPrice: 223.99,
			ComparableDiscountPct: 79.3,
		},
		CleanTitle: "Apple Watch Series 8 GPS 41mm",
		IsWarm:     true,
	})

	for _, field := range embed.Fields {
		if field.Name == "Best Buy Comps" {
			if !strings.Contains(field.Value, "$223.99 low") || !strings.Contains(field.Value, "$667.74 p25") {
				t.Fatalf("Best Buy Comps field = %q, want p25 and low prices", field.Value)
			}
			return
		}
	}
	t.Fatalf("Best Buy Comps field missing: %#v", embed.Fields)
}

func TestOrdinal(t *testing.T) {
	tests := map[int]string{
		1:  "1st",
		2:  "2nd",
		3:  "3rd",
		4:  "4th",
		11: "11th",
		12: "12th",
		13: "13th",
		21: "21st",
		22: "22nd",
		23: "23rd",
	}

	for input, want := range tests {
		if got := ordinal(input); got != want {
			t.Fatalf("ordinal(%d) = %q, want %q", input, got, want)
		}
	}
}
