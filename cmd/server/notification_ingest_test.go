package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/core"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type notificationTestStore struct {
	history  map[string]*models.CorePriceHistory
	catStats map[string]*models.CoreCategoryStats
	subs     []models.Subscription
	rules    []models.CoreRule
}

func newNotificationTestServer() *Server {
	store := &notificationTestStore{
		history:  make(map[string]*models.CorePriceHistory),
		catStats: make(map[string]*models.CoreCategoryStats),
		subs: []models.Subscription{
			{GuildID: "g1", ChannelID: "c1", SubscriptionType: "core", DealType: "core_alerts"},
		},
	}
	return &Server{
		coreProcessor: core.NewProcessor(store, &notificationTestNotifier{}, core.NewRateManager()),
		coreIssueLast: make(map[string]time.Time),
	}
}

func (s *notificationTestStore) GetCorePriceHistory(ctx context.Context, productName string) (*models.CorePriceHistory, bool, error) {
	history, ok := s.history[productName]
	return history, ok, nil
}

func (s *notificationTestStore) SaveCorePriceHistory(ctx context.Context, history models.CorePriceHistory) error {
	s.history[history.ProductName] = &history
	return nil
}

func (s *notificationTestStore) WipeCorePriceHistory(ctx context.Context) error {
	s.history = make(map[string]*models.CorePriceHistory)
	return nil
}

func (s *notificationTestStore) GetCoreCategoryStats(ctx context.Context, category string) (*models.CoreCategoryStats, bool, error) {
	stats, ok := s.catStats[category]
	return stats, ok, nil
}

func (s *notificationTestStore) SaveCoreCategoryStats(ctx context.Context, stats models.CoreCategoryStats) error {
	s.catStats[stats.Category] = &stats
	return nil
}

func (s *notificationTestStore) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

func (s *notificationTestStore) GetCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	return s.rules, nil
}

type notificationTestNotifier struct {
	systemAlerts []models.CoreSystemAlert
}

func (n *notificationTestNotifier) SendCoreAlert(ctx context.Context, alert models.CoreAlert, subs []models.Subscription) (map[string]string, error) {
	return map[string]string{"c1": "m1"}, nil
}

func (n *notificationTestNotifier) UpdateCoreAlert(ctx context.Context, alert models.CoreAlert) error {
	return nil
}

func (n *notificationTestNotifier) SendCoreSystemAlert(ctx context.Context, alert models.CoreSystemAlert, subs []models.Subscription) error {
	n.systemAlerts = append(n.systemAlerts, alert)
	return nil
}

func TestDiscordNotificationIngestHandlerAcceptsNotification(t *testing.T) {
	srv := newNotificationTestServer()
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`{
		"type": "notification",
		"receivedAt": 1780000000123,
		"packageName": "com.discord",
		"notificationKey": "0|com.discord|42|null|101",
		"type": "notification",
		"packageName": "com.discord",
		"tag": "discord_tag",
		"tickerText": "Fuller notification text",
		"extras": {
			"conversationTitle": "Deals",
			"messages": [
				{"sender": "User1", "text": "Msg1", "time": 100},
				{"sender": "User2", "text": "Msg2", "time": 200}
			]
		}
	}`))
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var body struct {
		Status       string                        `json:"status"`
		Notification normalizedDiscordNotification `json:"notification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if body.Status != "accepted" {
		t.Fatalf("status body = %q, want accepted", body.Status)
	}
	if body.Notification.SourcePackage != "com.discord" {
		t.Fatalf("source package = %q, want com.discord", body.Notification.SourcePackage)
	}
	if body.Notification.ConversationTitle != "Deals" {
		t.Fatalf("conversationTitle = %q, want Deals", body.Notification.ConversationTitle)
	}
	if body.Notification.TickerText != "Fuller notification text" {
		t.Fatalf("tickerText = %q, want Fuller notification text", body.Notification.TickerText)
	}
	if body.Notification.EventID == "" {
		t.Fatal("eventId should be populated")
	}
	if len(body.Notification.Messages) != 2 {
		t.Fatalf("message count = %d, want 2: %#v", len(body.Notification.Messages), body.Notification.Messages)
	}
}

func TestDiscordNotificationIngestHandlerTickerText(t *testing.T) {
	srv := newNotificationTestServer()
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`{
		"type": "notification",
		"packageName": "com.discord",
		"tickerText": "$161.18 | Amazon COM | Magic: The Gathering | Avatar: The Last Airbender",
		"extras": {
			"title": "CoreFinder",
			"bigText": "$161.18 | Amazon COM | Magic: The Gathering | Avatar: The Last..."
		}
	}`))
	rec := httptest.NewRecorder()
	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}

	var body struct {
		Notification normalizedDiscordNotification `json:"notification"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	// Verify that the full untruncated tickerText is preserved
	if body.Notification.TickerText != "$161.18 | Amazon COM | Magic: The Gathering | Avatar: The Last Airbender" {
		t.Fatalf("expected TickerText to be full text, got: %q", body.Notification.TickerText)
	}
	if len(body.Notification.Lines) == 0 || body.Notification.Lines[0] != "$161.18 | Amazon COM | Magic: The Gathering | Avatar: The Last..." {
		t.Fatalf("expected bigText to be the first candidate line, got: %#v", body.Notification.Lines)
	}
}

func TestDiscordNotificationIngestHandlerAcceptsTestEvent(t *testing.T) {
	srv := newNotificationTestServer()
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`{
		"type": "test",
		"receivedAt": 1780000000123,
		"source": "swordswallower",
		"message": "test event"
	}`))
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

func TestDiscordNotificationIngestHandlerRejectsMissingCoreProcessor(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`{
		"type": "notification",
		"packageName": "com.discord"
	}`))
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestDiscordNotificationIngestHandlerRejectsMissingPackage(t *testing.T) {
	srv := newNotificationTestServer()
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`{"type":"notification"}`))
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDiscordNotificationIngestHandlerRejectsInvalidJSON(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", strings.NewReader(`not-json`))
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestDiscordNotificationIngestHandlerRejectsWrongMethod(t *testing.T) {
	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/ingest/discord-notification", nil)
	rec := httptest.NewRecorder()

	srv.DiscordNotificationIngestHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}
