package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/core"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const discordNotificationIngestMaxBytes = 128 * 1024

type discordNotificationIngestEvent struct {
	Type            string                      `json:"type"`
	ReceivedAt      int64                       `json:"receivedAt"`
	PackageName     string                      `json:"packageName"`
	NotificationKey string                      `json:"notificationKey"`
	Tag             string                      `json:"tag"`
	PostTime        int64                       `json:"postTime"`
	TickerText      string                      `json:"tickerText"`
	Extras          discordNotificationExtras   `json:"extras"`
	Actions         []discordNotificationAction `json:"actions"`
	MarkRead        discordMarkReadResult       `json:"markRead"`
	Source          string                      `json:"source"`
	Stage           string                      `json:"stage"`
	Message         string                      `json:"message"`
	Error           string                      `json:"error"`
}

type discordNotificationExtras struct {
	ConversationTitle   string                   `json:"conversationTitle"`
	Title               string                   `json:"title"`
	TitleBig            string                   `json:"titleBig"`
	Text                string                   `json:"text"`
	BigText             string                   `json:"bigText"`
	SubText             string                   `json:"subText"`
	SummaryText         string                   `json:"summaryText"`
	InfoText            string                   `json:"infoText"`
	TextLines           []string                 `json:"textLines"`
	Messages            []discordNotificationMsg `json:"messages"`
	PictureBase64       string                   `json:"pictureBase64"`
	Link                string                   `json:"link"`
	URI                 string                   `json:"uri"`
	DataLink            string                   `json:"dataLink"`
	ContentIntent       string                   `json:"contentIntent"`
	IsGroupConversation bool                     `json:"isGroupConversation"`
}

type discordNotificationMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   int64  `json:"time"`
	URI    string `json:"uri"`
	Type   string `json:"type"`
}

type discordNotificationAction struct {
	Title     string `json:"title"`
	HasIntent bool   `json:"hasIntent"`
}

type discordMarkReadResult struct {
	Sent               bool   `json:"sent"`
	CancelFallbackUsed bool   `json:"cancelFallbackUsed"`
	MatchedTitle       string `json:"matchedTitle"`
	Reason             string `json:"reason"`
	Error              string `json:"error"`
}

type normalizedDiscordNotification struct {
	SourcePackage     string
	NotificationKey   string
	Tag               string
	TickerText        string
	ConversationTitle string
	Title             string
	Text              string
	BigText           string
	Lines             []string
	Messages          []core.DiscordNotificationMsg
	PictureBase64     string
	RawLink           string
	ReceivedAt        int64
	EventID           string
	MarkRead          discordMarkReadResult
	Actions           []discordNotificationAction
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

	if s.coreProcessor == nil {
		http.Error(w, "core processor not configured", http.StatusServiceUnavailable)
		return
	}

	if handled := s.handleSwordswallowerControlEvent(w, event); handled {
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
		err := s.coreProcessor.ProcessNotificationBatch(ctx, core.DiscordNotificationBatch{
			ConversationTitle: normalized.ConversationTitle,
			Tag:               normalized.Tag,
			TickerText:        normalized.TickerText,
			Lines:             normalized.Lines,
			Messages:          normalized.Messages,
			PictureBase64:     normalized.PictureBase64,
			EventID:           normalized.EventID,
			SourcePackage:     normalized.SourcePackage,
			RawLink:           normalized.RawLink,
		})
		if err != nil {
			slog.Error("Core notification batch failed", "event_id", normalized.EventID, "source_package", normalized.SourcePackage, "error", err)
			s.reportCoreSystemIssue("process:"+normalized.EventID, models.CoreSystemAlert{
				Title:         "Core notification processing failed",
				Severity:      "error",
				Component:     "core-notification-ingest",
				EventID:       normalized.EventID,
				SourcePackage: normalized.SourcePackage,
				Details:       err.Error(),
			})
		}
	}()

	slog.Info("Discord notification ingested",
		"event_id", normalized.EventID,
		"source_package", normalized.SourcePackage,
	)

	rawNotif := models.CoreRawNotification{
		EventID:       normalized.EventID,
		SourcePackage: normalized.SourcePackage,
		Title:         normalized.ConversationTitle,
		Message:       primaryRawMessage(normalized),
		Lines:         normalized.Lines,
		ReceivedAt:    time.Now(),
	}
	if normalized.ReceivedAt > 0 {
		rawNotif.ReceivedAt = time.UnixMilli(normalized.ReceivedAt).UTC()
	}
	if s.db != nil {
		if err := s.db.SaveCoreRawNotification(r.Context(), rawNotif); err != nil {
			slog.Error("Failed to save raw notification", "event_id", normalized.EventID, "error", err)
			s.reportCoreSystemIssue("raw-save:"+normalized.SourcePackage, models.CoreSystemAlert{
				Title:         "Core raw notification save failed",
				Severity:      "error",
				Component:     "core-notification-ingest",
				EventID:       normalized.EventID,
				SourcePackage: normalized.SourcePackage,
				Details:       err.Error(),
			})
		}
	}

	s.reportMarkReadIssue(normalized)

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
			URI:    m.URI,
		})
	}

	conversationTitle := firstNonEmptyString(event.Extras.ConversationTitle, event.Extras.TitleBig, event.Extras.Title)
	normalized := normalizedDiscordNotification{
		EventID:           notificationEventID(event),
		SourcePackage:     event.PackageName,
		NotificationKey:   event.NotificationKey,
		Tag:               event.Tag,
		TickerText:        event.TickerText,
		ConversationTitle: event.Extras.ConversationTitle,
		Title:             event.Extras.Title,
		Text:              event.Extras.Text,
		BigText:           event.Extras.BigText,
		Lines:             notificationCandidateLines(event),
		Messages:          coreMsgs,
		PictureBase64:     event.Extras.PictureBase64,
		RawLink:           firstNonEmptyString(event.Extras.Link, event.Extras.URI, event.Extras.DataLink),
		ReceivedAt:        event.ReceivedAt,
		MarkRead:          event.MarkRead,
		Actions:           event.Actions,
	}
	if normalized.ConversationTitle == "" {
		normalized.ConversationTitle = conversationTitle
	}
	return normalized
}

func notificationCandidateLines(event discordNotificationIngestEvent) []string {
	var lines []string
	lines = appendUniqueStrings(lines,
		event.Extras.BigText,
		event.Extras.Text,
		event.TickerText,
		event.Extras.TitleBig,
		event.Extras.Title,
		event.Extras.SubText,
		event.Extras.SummaryText,
		event.Extras.InfoText,
	)
	lines = appendUniqueStrings(lines, event.Extras.TextLines...)
	for _, msg := range event.Extras.Messages {
		lines = appendUniqueStrings(lines, msg.Text)
	}
	return lines
}

func appendUniqueStrings(values []string, candidates ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(candidates))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	return values
}

func primaryRawMessage(normalized normalizedDiscordNotification) string {
	return firstNonEmptyString(normalized.BigText, normalized.Text, normalized.TickerText)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) handleSwordswallowerControlEvent(w http.ResponseWriter, event discordNotificationIngestEvent) bool {
	eventType := strings.TrimSpace(event.Type)
	switch eventType {
	case "test":
		eventID := notificationEventID(event)
		s.reportCoreSystemIssue("test:"+event.Source, models.CoreSystemAlert{
			Title:     "Swordswallower test event received",
			Severity:  "info",
			Component: "swordswallower-listener",
			EventID:   eventID,
			Details:   firstNonEmptyString(event.Message, "The Android notification listener reached rfd-discord-bot."),
			Fields: []models.CoreSystemAlertField{
				{Name: "Source", Value: event.Source},
			},
		})
		writeNotificationIngestAccepted(w, map[string]any{
			"status":  "accepted",
			"type":    eventType,
			"eventId": eventID,
		})
		return true
	case "listener_error":
		eventID := notificationEventID(event)
		s.reportCoreSystemIssue("listener-error:"+event.Stage, models.CoreSystemAlert{
			Title:     "Swordswallower listener error",
			Severity:  "error",
			Component: "swordswallower-listener",
			EventID:   eventID,
			Details:   firstNonEmptyString(event.Error, event.Message, "Listener reported an error."),
			Fields: []models.CoreSystemAlertField{
				{Name: "Stage", Value: event.Stage},
				{Name: "Source", Value: event.Source},
			},
		})
		writeNotificationIngestAccepted(w, map[string]any{
			"status":  "accepted",
			"type":    eventType,
			"eventId": eventID,
		})
		return true
	default:
		return false
	}
}

func (s *Server) reportMarkReadIssue(normalized normalizedDiscordNotification) {
	reason := strings.TrimSpace(normalized.MarkRead.Reason)
	if normalized.MarkRead.Sent || normalized.MarkRead.CancelFallbackUsed || (reason == "" && normalized.MarkRead.Error == "") {
		return
	}
	if reason != "no_match" && reason != "regex_error" && normalized.MarkRead.Error == "" {
		return
	}

	var actionTitles []string
	for _, action := range normalized.Actions {
		if strings.TrimSpace(action.Title) != "" {
			actionTitles = append(actionTitles, action.Title)
		}
	}
	fields := []models.CoreSystemAlertField{
		{Name: "Reason", Value: reason},
	}
	if normalized.MarkRead.Error != "" {
		fields = append(fields, models.CoreSystemAlertField{Name: "Error", Value: normalized.MarkRead.Error})
	}
	if len(actionTitles) > 0 {
		fields = append(fields, models.CoreSystemAlertField{Name: "Notification actions", Value: strings.Join(actionTitles, "\n")})
	}

	s.reportCoreSystemIssue("mark-read:"+normalized.SourcePackage+":"+reason, models.CoreSystemAlert{
		Title:         "Swordswallower mark-as-read did not run",
		Severity:      "warning",
		Component:     "swordswallower-listener",
		EventID:       normalized.EventID,
		SourcePackage: normalized.SourcePackage,
		Details:       "The listener received a Discord notification but could not trigger the notification-level mark-as-read action.",
		Fields:        fields,
	})
}

const coreSystemIssueRepeatInterval = 10 * time.Minute

func (s *Server) reportCoreSystemIssue(key string, alert models.CoreSystemAlert) {
	if s == nil || s.coreProcessor == nil {
		return
	}
	key = strings.TrimSpace(key)
	if key == "" {
		key = alert.Title
	}
	if !s.shouldReportCoreSystemIssue(key) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := s.coreProcessor.ReportSystemIssue(ctx, alert); err != nil {
			slog.Error("Failed to send core system issue alert", "key", key, "error", err)
		}
	}()
}

func (s *Server) shouldReportCoreSystemIssue(key string) bool {
	s.coreIssueMu.Lock()
	defer s.coreIssueMu.Unlock()
	if s.coreIssueLast == nil {
		s.coreIssueLast = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := s.coreIssueLast[key]; ok && now.Sub(last) < coreSystemIssueRepeatInterval {
		return false
	}
	s.coreIssueLast[key] = now
	return true
}

func writeNotificationIngestAccepted(w http.ResponseWriter, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("Failed to encode notification ingest response", "error", err)
	}
}

func notificationEventID(event discordNotificationIngestEvent) string {
	h := sha256.New()
	_, _ = h.Write([]byte(event.PackageName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.NotificationKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Tag))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fmt.Sprint(event.PostTime)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(fmt.Sprint(event.ReceivedAt)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.TickerText))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(event.Extras.ConversationTitle))
	for _, line := range notificationCandidateLines(event) {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(line))
	}
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
