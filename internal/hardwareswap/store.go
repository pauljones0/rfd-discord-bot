package hardwareswap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mitchellh/mapstructure"

	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

// ServerConfig stores Discord server configuration for HardwareSwap.
type ServerConfig struct {
	FeedChannelID string    `docstore:"feed_channel_id"`
	PingChannelID string    `docstore:"ping_channel_id"`
	UpdatedAt     time.Time `docstore:"updated_at"`
}

// AlertRule represents a single user's keyword alert.
type AlertRule struct {
	ID        string    `docstore:"-"`
	UserID    string    `docstore:"user_id"`
	ServerID  string    `docstore:"server_id"`
	MustHave  []string  `docstore:"must_have"`
	AnyOf     []string  `docstore:"any_of"`
	MustNot   []string  `docstore:"must_not"`
	RawQuery  string    `docstore:"raw_query"`
	CreatedAt time.Time `docstore:"created_at"`
}

// PostRecord maps a Reddit post ID to Discord message IDs for updating/striking-through.
type PostRecord struct {
	RedditID     string            `docstore:"reddit_id"`
	CleanedTitle string            `docstore:"cleaned_title"`
	ServerMsgs   map[string]string `docstore:"server_msgs"`
	PostedAt     time.Time         `docstore:"posted_at"`
}

// AnalyticsRecord stores how an alert was created to evaluate AI effectiveness.
type AnalyticsRecord struct {
	ID                 string    `docstore:"-"`
	FlowType           string    `docstore:"flow_type"`
	OriginalUserPrompt string    `docstore:"original_user_prompt,omitempty"`
	AISuggestedQuery   string    `docstore:"ai_suggested_query,omitempty"`
	FinalSavedQuery    string    `docstore:"final_saved_query,omitempty"`
	Outcome            string    `docstore:"outcome"`
	EditCount          int       `docstore:"edit_count"`
	CreatedAt          time.Time `docstore:"created_at"`
}

// SystemPrompt stores dynamically updated AI system instructions.
type SystemPrompt struct {
	PromptText string    `docstore:"prompt_text"`
	UpdatedAt  time.Time `docstore:"updated_at"`
}

// Store provides document-store operations for the HardwareSwap processor.
type Store struct {
	documents *storage.Client
}

// NewDocumentStore creates a HardwareSwap store backed by the shared local document store.
func NewDocumentStore(client *storage.Client) *Store {
	return &Store{documents: client}
}

// --- Server Configs ---

func (s *Store) SaveServerConfig(ctx context.Context, serverID string, cfg ServerConfig) error {
	cfg.UpdatedAt = time.Now()
	return s.documents.SetDocument(ctx, "hw_servers", serverID, cfg)
}

func (s *Store) GetServerConfig(ctx context.Context, serverID string) (*ServerConfig, error) {
	var cfg ServerConfig
	ok, err := s.documents.GetDocument(ctx, "hw_servers", serverID, &cfg)
	if err != nil || !ok {
		return nil, err
	}
	return &cfg, nil
}

// --- Alerts ---

func (s *Store) AddAlert(ctx context.Context, rule AlertRule) error {
	rule.CreatedAt = time.Now()
	_, err := s.documents.AddDocument(ctx, "hw_alerts", rule)
	return err
}

func (s *Store) GetUserAlerts(ctx context.Context, serverID, userID string) ([]AlertRule, error) {
	docs, err := s.documents.ListDocuments(ctx, "hw_alerts")
	if err != nil {
		return nil, err
	}
	var alerts []AlertRule
	for _, doc := range docs {
		if documentString(doc.Data, "server_id") != serverID || documentString(doc.Data, "user_id") != userID {
			continue
		}
		var alert AlertRule
		if err := decodeHWDocument(doc.Data, &alert); err != nil {
			return nil, err
		}
		alert.ID = doc.ID
		alerts = append(alerts, alert)
	}
	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].CreatedAt.After(alerts[j].CreatedAt)
	})
	return alerts, nil
}

func (s *Store) DeleteAlert(ctx context.Context, docID string) error {
	return s.documents.DeleteDocument(ctx, "hw_alerts", docID)
}

func (s *Store) DeleteAllUserAlerts(ctx context.Context, serverID, userID string) error {
	alerts, err := s.GetUserAlerts(ctx, serverID, userID)
	if err != nil {
		return err
	}
	if len(alerts) == 0 {
		return nil
	}
	for _, alert := range alerts {
		if err := s.documents.DeleteDocument(ctx, "hw_alerts", alert.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) GetAllAlerts(ctx context.Context) ([]AlertRule, error) {
	docs, err := s.documents.ListDocuments(ctx, "hw_alerts")
	if err != nil {
		return nil, err
	}
	alerts := make([]AlertRule, 0, len(docs))
	for _, doc := range docs {
		var alert AlertRule
		if err := decodeHWDocument(doc.Data, &alert); err != nil {
			return nil, err
		}
		alert.ID = doc.ID
		alerts = append(alerts, alert)
	}
	return alerts, nil
}

// --- Posts ---

func (s *Store) SavePostRecords(ctx context.Context, redditID, cleanedTitle string, serverMsgs map[string]string) error {
	var current PostRecord
	ok, err := s.documents.GetDocument(ctx, "hw_posts", redditID, &current)
	if err != nil {
		return err
	}
	if ok && current.ServerMsgs != nil {
		for serverID, messageID := range current.ServerMsgs {
			if _, exists := serverMsgs[serverID]; !exists {
				serverMsgs[serverID] = messageID
			}
		}
	}
	return s.documents.SetRawDocument(ctx, "hw_posts", redditID, map[string]any{
		"reddit_id":     redditID,
		"cleaned_title": cleanedTitle,
		"posted_at":     time.Now(),
		"server_msgs":   serverMsgs,
	})
}

func (s *Store) GetPostRecord(ctx context.Context, redditID string) (*PostRecord, error) {
	var pr PostRecord
	ok, err := s.documents.GetDocument(ctx, "hw_posts", redditID, &pr)
	if err != nil || !ok {
		return nil, err
	}
	return &pr, nil
}

func (s *Store) TrimOldPosts(ctx context.Context) error {
	docs, err := s.documents.ListDocuments(ctx, "hw_posts")
	if err != nil {
		return err
	}
	sort.Slice(docs, func(i, j int) bool {
		return documentTime(docs[i].Data, "posted_at").After(documentTime(docs[j].Data, "posted_at"))
	})
	if len(docs) <= 500 {
		return nil
	}
	for _, doc := range docs[500:] {
		if err := s.documents.DeleteDocument(ctx, "hw_posts", doc.ID); err != nil {
			return err
		}
	}
	return nil
}

// --- Analytics ---

func (s *Store) SaveAnalytics(ctx context.Context, record AnalyticsRecord) error {
	record.CreatedAt = time.Now()
	_, err := s.documents.AddDocument(ctx, "hw_analytics", record)
	return err
}

func (s *Store) GetUnprocessedAnalyticsByFlow(ctx context.Context, flowType string, limit int) ([]AnalyticsRecord, error) {
	docs, err := s.documents.ListDocuments(ctx, "hw_analytics")
	if err != nil {
		return nil, err
	}
	var records []AnalyticsRecord
	for _, doc := range docs {
		if documentString(doc.Data, "flow_type") != flowType {
			continue
		}
		var rec AnalyticsRecord
		if err := decodeHWDocument(doc.Data, &rec); err != nil {
			continue
		}
		rec.ID = doc.ID
		records = append(records, rec)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].CreatedAt.Before(records[j].CreatedAt)
	})
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *Store) DeleteAnalyticsChunk(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if err := s.documents.DeleteDocument(ctx, "hw_analytics", id); err != nil {
			return err
		}
	}
	return nil
}

// --- Dynamic AI Prompts ---

func (s *Store) GetSystemPrompt(ctx context.Context, key string) (string, error) {
	var sp SystemPrompt
	ok, err := s.documents.GetDocument(ctx, "hw_system_prompts", key, &sp)
	if err != nil || !ok {
		return "", err
	}
	return sp.PromptText, nil
}

func (s *Store) SetSystemPrompt(ctx context.Context, key, promptText string) error {
	sp := SystemPrompt{
		PromptText: promptText,
		UpdatedAt:  time.Now(),
	}
	if s.documents != nil {
		return s.documents.SetDocument(ctx, "hw_system_prompts", key, sp)
	}
	return fmt.Errorf("hardwareswap document store is not configured")
}

func decodeHWDocument(data map[string]any, dst any) error {
	decoder, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		Result:           dst,
		TagName:          "docstore",
		WeaklyTypedInput: true,
		Squash:           true,
		DecodeHook:       mapstructure.StringToTimeHookFunc(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	return decoder.Decode(data)
}

func documentString(data map[string]any, key string) string {
	if v, ok := data[key]; ok {
		return strings.TrimSpace(fmt.Sprint(v))
	}
	return ""
}

func documentTime(data map[string]any, key string) time.Time {
	switch v := data[key].(type) {
	case time.Time:
		return v
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return parsed
		}
		if parsed, err := time.Parse(time.RFC3339, v); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
