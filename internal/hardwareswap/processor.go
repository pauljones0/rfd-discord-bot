package hardwareswap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/reddit"
	"golang.org/x/sync/errgroup"
	"google.golang.org/genai"
)

// Processor orchestrates the HardwareSwap deal pipeline.
type Processor struct {
	store        *Store
	redditClient *reddit.Client
	aiClient     *ai.Client
	discordToken string
}

// NewProcessor creates a new HardwareSwap processor.
func NewProcessor(store *Store, redditClient *reddit.Client, aiClient *ai.Client, discordToken string) *Processor {
	return &Processor{
		store:        store,
		redditClient: redditClient,
		aiClient:     aiClient,
		discordToken: discordToken,
	}
}

var (
	globalMatcher = NewMatcher()
)

// ProcessHardwareSwapDeals runs the full pipeline.
func (p *Processor) ProcessHardwareSwapDeals(ctx context.Context) error {
	posts, err := p.redditClient.FetchPosts(ctx, "CanadianHardwareSwap")
	if err != nil {
		return fmt.Errorf("failed to fetch reddit: %w", err)
	}

	slog.Info("Fetched posts from Reddit", "processor", "hardwareswap", "count", len(posts))

	alerts, err := p.store.GetAllAlerts(ctx)
	if err != nil {
		return fmt.Errorf("failed to load alerts: %w", err)
	}

	cache := newConfigCache(p.store, 5*time.Minute)

	g, gCtx := errgroup.WithContext(ctx)
	g.SetLimit(10)

	for _, post := range posts {
		post := post
		g.Go(func() error {
			record, err := p.store.GetPostRecord(gCtx, post.ID)
			isNew := (record == nil || err != nil)

			if !isNew {
				p.handleExistingPostStatus(gCtx, cache, post, record)
				return nil
			}

			if isNew && post.RemovedByCategory == "" &&
				!strings.EqualFold(post.LinkFlairText, "Sold") &&
				!strings.EqualFold(post.LinkFlairText, "Closed") {
				p.processNewPost(gCtx, cache, post, alerts)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("parallel processing error: %w", err)
	}

	if err := p.store.TrimOldPosts(ctx); err != nil {
		slog.Warn("Non-fatal: failed to trim old posts", "processor", "hardwareswap", "error", err)
	}

	slog.Info("Pipeline finished", "processor", "hardwareswap")
	return nil
}

func (p *Processor) handleExistingPostStatus(ctx context.Context, cache *configCache, post reddit.Post, record *PostRecord) {
	if strings.EqualFold(post.LinkFlairText, "Sold") || strings.EqualFold(post.LinkFlairText, "Closed") {
		slog.Info("Detected SOLD/CLOSED post, updating messages",
			"processor", "hardwareswap", "reddit_id", post.ID, "count", len(record.ServerMsgs))

		for serverID, msgID := range record.ServerMsgs {
			cfg, err := cache.getServerConfig(ctx, serverID)
			if err != nil {
				slog.Warn("Could not get config for server during update",
					"processor", "hardwareswap", "server_id", serverID, "error", err)
				continue
			}

			embed := BuildClosedEmbed(record.CleanedTitle, "https://www.reddit.com/r/CanadianHardwareSwap/comments/"+post.ID, post.LinkFlairText)
			if err := editDiscordMessage(p.discordToken, cfg.FeedChannelID, msgID, embed); err != nil {
				slog.Error("Failed to edit message",
					"processor", "hardwareswap", "server_id", serverID, "msg_id", msgID, "error", err)
			}
		}
	}
}

func (p *Processor) processNewPost(ctx context.Context, cache *configCache, post reddit.Post, alerts []AlertRule) {
	slog.Info("Processing NEW post",
		"processor", "hardwareswap", "reddit_id", post.ID, "title", post.Title)

	cleaned, err := p.cleanRedditPost(ctx, post.Title, post.SelfText)
	if err != nil {
		slog.Error("Gemini failed to clean post",
			"processor", "hardwareswap", "reddit_id", post.ID, "error", err)
		return
	}

	corpus := cleaned.Title + " " + cleaned.Description + " " + cleaned.Location
	matches := findMatches(alerts, corpus)

	if len(matches) == 0 {
		return
	}

	embed := BuildDealEmbed(post, cleaned)
	buttons := BuildDealButtons(post.Permalink)

	serverMsgs := make(map[string]string)
	for serverID, userIDs := range matches {
		cfg, err := cache.getServerConfig(ctx, serverID)
		if err != nil {
			slog.Error("Could not get config for server",
				"processor", "hardwareswap", "server_id", serverID, "error", err)
			continue
		}

		msgID, err := sendDiscordEmbedWithComponents(p.discordToken, cfg.FeedChannelID, embed, buttons)
		if err != nil {
			slog.Error("Failed to post to feed channel",
				"processor", "hardwareswap", "server_id", serverID, "error", err)
			continue
		}
		serverMsgs[serverID] = msgID

		// Add reactions
		_ = addDiscordReaction(p.discordToken, cfg.FeedChannelID, msgID, "%F0%9F%91%8D")
		_ = addDiscordReaction(p.discordToken, cfg.FeedChannelID, msgID, "%F0%9F%91%8E")

		// Send pings
		if len(userIDs) > 0 && cfg.PingChannelID != "" {
			pingContent := ""
			for _, uid := range userIDs {
				pingContent += fmt.Sprintf("<@%s> ", uid)
			}
			pingContent += fmt.Sprintf("- **Match Found in the Deal Feed!** <https://discord.com/channels/%s/%s/%s>", serverID, cfg.FeedChannelID, msgID)
			_ = sendDiscordMessage(p.discordToken, cfg.PingChannelID, pingContent)
		}
	}

	if len(serverMsgs) > 0 {
		if err := p.store.SavePostRecords(ctx, post.ID, cleaned.Title, serverMsgs); err != nil {
			slog.Error("Failed to save post records",
				"processor", "hardwareswap", "reddit_id", post.ID, "error", err)
		}
	}
}

func (p *Processor) cleanRedditPost(ctx context.Context, rawTitle, rawBody string) (*CleanedPost, error) {
	prompt := CleanPostSystemInstruction + "\n\n" + fmt.Sprintf(CleanPostUserPromptTemplate, rawTitle, rawBody)
	config := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	}

	text, _, _, err := p.aiClient.GenerateContentRaw(ctx, prompt, config)
	if err != nil {
		return nil, err
	}

	var cleaned CleanedPost
	if err := json.Unmarshal([]byte(text), &cleaned); err != nil {
		return nil, fmt.Errorf("failed to parse cleaned post JSON: %w", err)
	}
	return &cleaned, nil
}

func findMatches(alerts []AlertRule, corpus string) map[string][]string {
	matches := make(map[string][]string)
	for _, alert := range alerts {
		if globalMatcher.Matches(corpus, alert.MustHave, alert.AnyOf, alert.MustNot) {
			matches[alert.ServerID] = append(matches[alert.ServerID], alert.UserID)
		}
	}
	return matches
}

// configCache provides an in-memory TTL cache for server configurations.
type configCache struct {
	items map[string]configCacheItem
	ttl   time.Duration
	store *Store
}

type configCacheItem struct {
	config    *ServerConfig
	expiresAt time.Time
}

func newConfigCache(store *Store, ttl time.Duration) *configCache {
	return &configCache{
		items: make(map[string]configCacheItem),
		ttl:   ttl,
		store: store,
	}
}

func (c *configCache) getServerConfig(ctx context.Context, serverID string) (*ServerConfig, error) {
	item, ok := c.items[serverID]
	if ok && time.Now().Before(item.expiresAt) {
		return item.config, nil
	}
	cfg, err := c.store.GetServerConfig(ctx, serverID)
	if err != nil {
		return nil, err
	}
	c.items[serverID] = configCacheItem{
		config:    cfg,
		expiresAt: time.Now().Add(c.ttl),
	}
	return cfg, nil
}

// --- Discord HTTP helpers ---
// These use raw HTTP calls matching the pattern in internal/notifier/discord.go

const discordAPIBase = "https://discord.com/api/v10"

func discordRequest(method, token, endpoint string, body interface{}) ([]byte, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, discordAPIBase+endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discord API error %d: %s", resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

func discordPost(token, endpoint string, body interface{}) error {
	_, err := discordRequest("POST", token, endpoint, body)
	return err
}

func discordPostReturnID(token, endpoint string, body interface{}) (string, error) {
	respBody, err := discordRequest("POST", token, endpoint, body)
	if err != nil {
		return "", err
	}
	var msg struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBody, &msg); err != nil {
		return "", err
	}
	return msg.ID, nil
}

func discordPatch(token, endpoint string, body interface{}) error {
	_, err := discordRequest("PATCH", token, endpoint, body)
	return err
}

func discordPut(token, endpoint string) error {
	_, err := discordRequest("PUT", token, endpoint, nil)
	return err
}

func sendDiscordMessage(token, channelID, content string) error {
	return discordPost(token, fmt.Sprintf("/channels/%s/messages", channelID),
		map[string]interface{}{"content": content})
}

func sendDiscordEmbedWithComponents(token, channelID string, embed map[string]interface{}, components []interface{}) (string, error) {
	payload := map[string]interface{}{
		"embeds":     []interface{}{embed},
		"components": components,
	}
	return discordPostReturnID(token, fmt.Sprintf("/channels/%s/messages", channelID), payload)
}

func editDiscordMessage(token, channelID, messageID string, embed map[string]interface{}) error {
	return discordPatch(token, fmt.Sprintf("/channels/%s/messages/%s", channelID, messageID),
		map[string]interface{}{
			"embeds": []interface{}{embed},
		})
}

func addDiscordReaction(token, channelID, messageID, emoji string) error {
	return discordPut(token, fmt.Sprintf("/channels/%s/messages/%s/reactions/%s/@me", channelID, messageID, emoji))
}
