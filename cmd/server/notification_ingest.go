package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/core"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const discordNotificationIngestMaxBytes = 128 * 1024

type discordNotificationIngestEvent struct {
	Type        string                    `json:"type"`
	ReceivedAt  int64                     `json:"receivedAt"`
	PackageName string                    `json:"packageName"`
	Tag         string                    `json:"tag"`
	PostTime    int64                     `json:"postTime"`
	TickerText  string                    `json:"tickerText"`
	Extras      discordNotificationExtras `json:"extras"`
}

type discordNotificationExtras struct {
	ConversationTitle string                   `json:"conversationTitle"`
	Messages          []discordNotificationMsg `json:"messages"`
	PictureBase64     string                   `json:"pictureBase64"`
}

type discordNotificationMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   int64  `json:"time"`
}

type normalizedDiscordNotification struct {
	SourcePackage     string
	Tag               string
	TickerText        string
	ConversationTitle string
	Messages          []core.DiscordNotificationMsg
	PictureBase64     string
	ReceivedAt        int64
	EventID           string
}

func (s *Server) DiscordNotificationIngestHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, discordNotificationIngestMaxBytes)
	defer r.Body.Close()

	var event discordNotificationIngestEvent
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&event); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if event.PackageName == "" {
		http.Error(w, "Missing packageName", http.StatusBadRequest)
		return
	}

	normalized := normalizeDiscordNotification(event)

	// Pass the batched messages to the core processor
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if s.coreProcessor != nil {
			s.coreProcessor.ProcessNotificationBatch(ctx, normalized.ConversationTitle, normalized.Tag, normalized.TickerText, normalized.Messages, normalized.PictureBase64, normalized.EventID, normalized.SourcePackage)
		}
	}()

	slog.Info("Discord notification ingested",
		"event_id", normalized.EventID,
		"source_package", normalized.SourcePackage,
	)

	// Save the raw notification to storage
	var lines []string
	for _, msg := range normalized.Messages {
		lines = append(lines, msg.Text)
	}
	rawNotif := models.CoreRawNotification{
		EventID:       normalized.EventID,
		SourcePackage: normalized.SourcePackage,
		Title:         normalized.ConversationTitle,
		Message:       normalized.TickerText,
		Lines:         lines,
		ReceivedAt:    time.Now(),
	}
	if normalized.ReceivedAt > 0 {
		rawNotif.ReceivedAt = time.UnixMilli(normalized.ReceivedAt).UTC()
	}
	if s.db != nil {
		if err := s.db.SaveCoreRawNotification(r.Context(), rawNotif); err != nil {
			slog.Error("Failed to save raw notification", "event_id", normalized.EventID, "error", err)
		}
	}

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
	var coreMsgs []core.DiscordNotificationMsg
	for _, m := range event.Extras.Messages {
		coreMsgs = append(coreMsgs, core.DiscordNotificationMsg{
			Sender: m.Sender,
			Text:   m.Text,
			Time:   m.Time,
		})
	}

	normalized := normalizedDiscordNotification{
		EventID:           notificationEventID(event),
		SourcePackage:     event.PackageName,
		Tag:               event.Tag,
		TickerText:        event.TickerText,
		ConversationTitle: event.Extras.ConversationTitle,
		Messages:          coreMsgs,
		PictureBase64:     event.Extras.PictureBase64,
		ReceivedAt:        event.ReceivedAt,
	}
	return normalized
}



func notificationEventID(event discordNotificationIngestEvent) string {
	h := sha256.New()
	_, _ = h.Write([]byte(event.PackageName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Tag))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.TickerText))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

func (s *Server) CoreRebinHandler(w http.ResponseWriter, r *http.Request) {
	if s.coreProcessor == nil {
		http.Error(w, "core processor not configured", http.StatusServiceUnavailable)
		return
	}

	slog.Info("Core bot: Manual re-binning triggered")
	if err := s.coreProcessor.Rebin(r.Context()); err != nil {
		slog.Error("Core bot: Manual re-binning failed", "error", err)
		http.Error(w, "re-binning failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "details": "Re-binning complete"})
}
