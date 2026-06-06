package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscordNotificationIngestHandlerAcceptsNotification(t *testing.T) {
	srv := &Server{}
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
	srv := &Server{}
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

	var body struct {
		Notification normalizedDiscordNotification `json:"notification"`
	}
	json.Unmarshal(rec.Body.Bytes(), &body)

	// Verify that the full untruncated tickerText is preserved
	if body.Notification.TickerText != "$161.18 | Amazon COM | Magic: The Gathering | Avatar: The Last Airbender" {
		t.Fatalf("expected TickerText to be full text, got: %q", body.Notification.TickerText)
	}
}


func TestDiscordNotificationIngestHandlerRejectsMissingPackage(t *testing.T) {
	srv := &Server{}
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
