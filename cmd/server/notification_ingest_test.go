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
		"notificationId": 42,
		"postTime": 1780000000000,
		"extras": {
			"title": "Deals",
			"text": "Short notification text",
			"bigText": "Fuller notification text",
			"textLines": ["Short notification text", "Second line"]
		},
		"actions": [{"title": "Mark as Read", "hasIntent": true}],
		"markRead": {"sent": true, "matchedTitle": "Mark as Read", "reason": "sent"}
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
	if body.Notification.Title != "Deals" {
		t.Fatalf("title = %q, want Deals", body.Notification.Title)
	}
	if body.Notification.Message != "Fuller notification text" {
		t.Fatalf("message = %q, want big text", body.Notification.Message)
	}
	if !body.Notification.MarkReadSent {
		t.Fatal("markReadSent = false, want true")
	}
	if body.Notification.EventID == "" {
		t.Fatal("eventId should be populated")
	}
	if len(body.Notification.Lines) != 4 {
		t.Fatalf("line count = %d, want 4: %#v", len(body.Notification.Lines), body.Notification.Lines)
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
