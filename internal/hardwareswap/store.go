package hardwareswap

import (
	"context"
	"log/slog"
	"sort"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// ServerConfig stores Discord server configuration for HardwareSwap.
type ServerConfig struct {
	FeedChannelID string    `firestore:"feed_channel_id"`
	PingChannelID string    `firestore:"ping_channel_id"`
	UpdatedAt     time.Time `firestore:"updated_at"`
}

// AlertRule represents a single user's keyword alert.
type AlertRule struct {
	ID        string    `firestore:"-"`
	UserID    string    `firestore:"user_id"`
	ServerID  string    `firestore:"server_id"`
	MustHave  []string  `firestore:"must_have"`
	AnyOf     []string  `firestore:"any_of"`
	MustNot   []string  `firestore:"must_not"`
	RawQuery  string    `firestore:"raw_query"`
	CreatedAt time.Time `firestore:"created_at"`
}

// PostRecord maps a Reddit post ID to Discord message IDs for updating/striking-through.
type PostRecord struct {
	RedditID     string            `firestore:"reddit_id"`
	CleanedTitle string            `firestore:"cleaned_title"`
	ServerMsgs   map[string]string `firestore:"server_msgs"`
	PostedAt     time.Time         `firestore:"posted_at"`
}

// AnalyticsRecord stores how an alert was created to evaluate AI effectiveness.
type AnalyticsRecord struct {
	ID                 string    `firestore:"-"`
	FlowType           string    `firestore:"flow_type"`
	OriginalUserPrompt string    `firestore:"original_user_prompt,omitempty"`
	AISuggestedQuery   string    `firestore:"ai_suggested_query,omitempty"`
	FinalSavedQuery    string    `firestore:"final_saved_query,omitempty"`
	Outcome            string    `firestore:"outcome"`
	EditCount          int       `firestore:"edit_count"`
	CreatedAt          time.Time `firestore:"created_at"`
}

// SystemPrompt stores dynamically updated AI system instructions.
type SystemPrompt struct {
	PromptText string    `firestore:"prompt_text"`
	UpdatedAt  time.Time `firestore:"updated_at"`
}

// Store provides Firestore operations for the HardwareSwap processor.
type Store struct {
	client *firestore.Client
}

// NewStore creates a new HardwareSwap store using an existing Firestore client.
func NewStore(client *firestore.Client) *Store {
	return &Store{client: client}
}

// --- Server Configs ---

func (s *Store) SaveServerConfig(ctx context.Context, serverID string, cfg ServerConfig) error {
	cfg.UpdatedAt = time.Now()
	_, err := s.client.Collection("hw_servers").Doc(serverID).Set(ctx, cfg)
	return err
}

func (s *Store) GetServerConfig(ctx context.Context, serverID string) (*ServerConfig, error) {
	doc, err := s.client.Collection("hw_servers").Doc(serverID).Get(ctx)
	if err != nil {
		return nil, err
	}
	var cfg ServerConfig
	if err := doc.DataTo(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// --- Alerts ---

func (s *Store) AddAlert(ctx context.Context, rule AlertRule) error {
	rule.CreatedAt = time.Now()
	_, _, err := s.client.Collection("hw_alerts").Add(ctx, rule)
	return err
}

func (s *Store) GetUserAlerts(ctx context.Context, serverID, userID string) ([]AlertRule, error) {
	var alerts []AlertRule
	iter := s.client.Collection("hw_alerts").
		Where("server_id", "==", serverID).
		Where("user_id", "==", userID).
		Documents(ctx)

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var alert AlertRule
		if err := doc.DataTo(&alert); err != nil {
			return nil, err
		}
		alert.ID = doc.Ref.ID
		alerts = append(alerts, alert)
	}

	sort.Slice(alerts, func(i, j int) bool {
		return alerts[i].CreatedAt.After(alerts[j].CreatedAt)
	})
	return alerts, nil
}

func (s *Store) DeleteAlert(ctx context.Context, docID string) error {
	_, err := s.client.Collection("hw_alerts").Doc(docID).Delete(ctx)
	return err
}

func (s *Store) DeleteAllUserAlerts(ctx context.Context, serverID, userID string) error {
	alerts, err := s.GetUserAlerts(ctx, serverID, userID)
	if err != nil {
		return err
	}
	if len(alerts) == 0 {
		return nil
	}
	batch := s.client.Batch()
	for _, alert := range alerts {
		ref := s.client.Collection("hw_alerts").Doc(alert.ID)
		batch.Delete(ref)
	}
	_, err = batch.Commit(ctx)
	return err
}

func (s *Store) GetAllAlerts(ctx context.Context) ([]AlertRule, error) {
	var alerts []AlertRule
	iter := s.client.Collection("hw_alerts").Documents(ctx)

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var alert AlertRule
		if err := doc.DataTo(&alert); err != nil {
			return nil, err
		}
		alert.ID = doc.Ref.ID
		alerts = append(alerts, alert)
	}
	return alerts, nil
}

// --- Posts ---

func (s *Store) SavePostRecords(ctx context.Context, redditID, cleanedTitle string, serverMsgs map[string]string) error {
	doc := s.client.Collection("hw_posts").Doc(redditID)
	data := map[string]interface{}{
		"reddit_id":     redditID,
		"cleaned_title": cleanedTitle,
		"posted_at":     time.Now(),
		"server_msgs":   serverMsgs,
	}
	_, err := doc.Set(ctx, data, firestore.MergeAll)
	return err
}

func (s *Store) GetPostRecord(ctx context.Context, redditID string) (*PostRecord, error) {
	doc, err := s.client.Collection("hw_posts").Doc(redditID).Get(ctx)
	if err != nil {
		return nil, err
	}
	var pr PostRecord
	if err := doc.DataTo(&pr); err != nil {
		return nil, err
	}
	return &pr, nil
}

func (s *Store) TrimOldPosts(ctx context.Context) error {
	iter := s.client.Collection("hw_posts").
		OrderBy("posted_at", firestore.Desc).
		Documents(ctx)

	count := 0
	batch := s.client.Batch()
	docsToDelete := 0

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			slog.Error("Error iterating posts during trim", "processor", "hardwareswap", "error", err)
			return err
		}
		count++
		if count > 500 {
			batch.Delete(doc.Ref)
			docsToDelete++
			if docsToDelete == 500 {
				if _, err := batch.Commit(ctx); err != nil {
					return err
				}
				batch = s.client.Batch()
				docsToDelete = 0
			}
		}
	}

	if docsToDelete > 0 {
		if _, err := batch.Commit(ctx); err != nil {
			return err
		}
		slog.Info("Trimmed old posts", "processor", "hardwareswap", "count", docsToDelete)
	}
	return nil
}

// --- Analytics ---

func (s *Store) SaveAnalytics(ctx context.Context, record AnalyticsRecord) error {
	record.CreatedAt = time.Now()
	_, _, err := s.client.Collection("hw_analytics").Add(ctx, record)
	return err
}

func (s *Store) GetUnprocessedAnalyticsByFlow(ctx context.Context, flowType string, limit int) ([]AnalyticsRecord, error) {
	var records []AnalyticsRecord
	iter := s.client.Collection("hw_analytics").
		Where("flow_type", "==", flowType).
		OrderBy("created_at", firestore.Asc).
		Limit(limit).
		Documents(ctx)

	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		var rec AnalyticsRecord
		if err := doc.DataTo(&rec); err != nil {
			continue
		}
		rec.ID = doc.Ref.ID
		records = append(records, rec)
	}
	return records, nil
}

func (s *Store) DeleteAnalyticsChunk(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	batch := s.client.Batch()
	for _, id := range ids {
		ref := s.client.Collection("hw_analytics").Doc(id)
		batch.Delete(ref)
	}
	_, err := batch.Commit(ctx)
	return err
}

// --- Dynamic AI Prompts ---

func (s *Store) GetSystemPrompt(ctx context.Context, key string) (string, error) {
	doc, err := s.client.Collection("hw_system_prompts").Doc(key).Get(ctx)
	if err != nil {
		return "", err
	}
	var sp SystemPrompt
	if err := doc.DataTo(&sp); err != nil {
		return "", err
	}
	return sp.PromptText, nil
}

func (s *Store) SetSystemPrompt(ctx context.Context, key, promptText string) error {
	sp := SystemPrompt{
		PromptText: promptText,
		UpdatedAt:  time.Now(),
	}
	_, err := s.client.Collection("hw_system_prompts").Doc(key).Set(ctx, sp)
	return err
}

