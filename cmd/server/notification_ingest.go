package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const discordNotificationIngestMaxBytes = 128 * 1024

type discordNotificationIngestEvent struct {
	Type            string                      `json:"type"`
	ReceivedAt      int64                       `json:"receivedAt"`
	PackageName     string                      `json:"packageName"`
	NotificationKey string                      `json:"notificationKey"`
	NotificationID  int                         `json:"notificationId"`
	Tag             string                      `json:"tag"`
	PostTime        int64                       `json:"postTime"`
	IsClearable     bool                        `json:"isClearable"`
	IsOngoing       bool                        `json:"isOngoing"`
	Category        string                      `json:"category"`
	Group           string                      `json:"group"`
	Extras          discordNotificationExtras   `json:"extras"`
	Actions         []discordNotificationAction `json:"actions"`
	MarkRead        discordNotificationMarkRead `json:"markRead"`
}

type discordNotificationExtras struct {
	Title       string   `json:"title"`
	Text        string   `json:"text"`
	BigText     string   `json:"bigText"`
	SubText     string   `json:"subText"`
	SummaryText string   `json:"summaryText"`
	InfoText    string   `json:"infoText"`
	TextLines   []string `json:"textLines"`
}

type discordNotificationAction struct {
	Title     string `json:"title"`
	HasIntent bool   `json:"hasIntent"`
}

type discordNotificationMarkRead struct {
	Sent               bool   `json:"sent"`
	CancelFallbackUsed bool   `json:"cancelFallbackUsed"`
	MatchedTitle       string `json:"matchedTitle"`
	Reason             string `json:"reason"`
	Error              string `json:"error"`
}

type normalizedDiscordNotification struct {
	EventID         string   `json:"eventId"`
	SourcePackage   string   `json:"sourcePackage"`
	Title           string   `json:"title,omitempty"`
	Message         string   `json:"message,omitempty"`
	Lines           []string `json:"lines,omitempty"`
	ReceivedAt      string   `json:"receivedAt,omitempty"`
	PostTime        string   `json:"postTime,omitempty"`
	MarkReadSent    bool     `json:"markReadSent"`
	MarkReadReason  string   `json:"markReadReason,omitempty"`
	NotificationKey string   `json:"notificationKey,omitempty"`
}

func (s *Server) DiscordNotificationIngestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, discordNotificationIngestMaxBytes)
	defer r.Body.Close()

	var event discordNotificationIngestEvent
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "invalid notification event: "+err.Error(), http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(event.PackageName) == "" {
		http.Error(w, "invalid notification event: packageName is required", http.StatusBadRequest)
		return
	}

	normalized := normalizeDiscordNotification(event)
	slog.Info("Discord notification ingested",
		"event_id", normalized.EventID,
		"source_package", normalized.SourcePackage,
		"title", normalized.Title,
		"message", normalized.Message,
		"line_count", len(normalized.Lines),
		"mark_read_sent", normalized.MarkReadSent,
		"mark_read_reason", normalized.MarkReadReason,
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"status":       "accepted",
		"notification": normalized,
	}); err != nil {
		slog.Error("Failed to encode notification ingest response", "error", err)
	}
}

func normalizeDiscordNotification(event discordNotificationIngestEvent) normalizedDiscordNotification {
	lines := notificationTextLines(event.Extras)
	title := firstNotificationText(event.Extras.Title, event.Extras.SubText)
	message := firstNotificationText(event.Extras.BigText, event.Extras.Text)
	if message == "" && len(lines) > 0 {
		message = lines[0]
	}

	normalized := normalizedDiscordNotification{
		EventID:         notificationEventID(event, title, message),
		SourcePackage:   event.PackageName,
		Title:           title,
		Message:         message,
		Lines:           lines,
		MarkReadSent:    event.MarkRead.Sent,
		MarkReadReason:  event.MarkRead.Reason,
		NotificationKey: event.NotificationKey,
	}
	if event.ReceivedAt > 0 {
		normalized.ReceivedAt = time.UnixMilli(event.ReceivedAt).UTC().Format(time.RFC3339Nano)
	}
	if event.PostTime > 0 {
		normalized.PostTime = time.UnixMilli(event.PostTime).UTC().Format(time.RFC3339Nano)
	}
	return normalized
}

func notificationTextLines(extras discordNotificationExtras) []string {
	candidates := []string{
		extras.Title,
		extras.SubText,
		extras.Text,
		extras.BigText,
		extras.SummaryText,
		extras.InfoText,
	}
	candidates = append(candidates, extras.TextLines...)

	seen := make(map[string]struct{}, len(candidates))
	lines := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		line := strings.TrimSpace(candidate)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	return lines
}

func firstNotificationText(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func notificationEventID(event discordNotificationIngestEvent, title, message string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(event.PackageName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.NotificationKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Tag))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(title))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(message))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}
