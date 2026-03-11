package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const (
	colorColdDeal = 2829617  // #2B2D31 (Discord dark mode embed background) — alert fired, but quiet
	colorWarmDeal = 16098851 // #F5A623 (amber)            — getting traction
	colorHotDeal  = 16723320 // #FF2D78 (magenta-pink)     — blowing up, act fast

	heatScoreThresholdWarm = 0.05
	heatScoreThresholdHot  = 0.20

	maxRetries = 3
)

type Client struct {
	botToken    string
	client      *http.Client
	rateLimiter *rate.Limiter
}

func New(botToken string) *Client {
	return &Client{
		botToken:    botToken,
		client:      &http.Client{Timeout: 10 * time.Second},
		rateLimiter: rate.NewLimiter(rate.Every(60*time.Second/50), 1), // Discord allows 50 req/sec globally, let's play it safe
	}
}

// Send sends a new deal notification to all subscribed channels.
// Returns a map of ChannelID -> MessageID.
func (c *Client) Send(ctx context.Context, deal models.DealInfo, subs []models.Subscription) (map[string]string, error) {
	if c.botToken == "" {
		return nil, nil // No bot token configured
	}

	payload := createDiscordPayload(deal)
	results := make(map[string]string)

	for _, sub := range subs {
		urlStr := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages", sub.ChannelID)
		body, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send deal to channel", "channel", sub.ChannelID, "error", err)
			continue
		}

		var msgResponse discordMessageResponse
		if err := json.Unmarshal(body, &msgResponse); err != nil {
			slog.Error("Failed to parse discord message response", "error", err)
			continue
		}
		results[sub.ChannelID] = msgResponse.ID
	}

	return results, nil
}

// Update updates an existing notification in all channels it was published to.
func (c *Client) Update(ctx context.Context, deal models.DealInfo) error {
	if c.botToken == "" || len(deal.DiscordMessageIDs) == 0 {
		return nil
	}

	payload := createDiscordPayload(deal)
	var lastErr error

	for channelID, messageID := range deal.DiscordMessageIDs {
		patchURL := fmt.Sprintf("https://discord.com/api/v10/channels/%s/messages/%s", channelID, messageID)
		_, err := c.doRequest(ctx, "PATCH", patchURL, payload)
		if err != nil {
			slog.Error("Failed to update deal", "channel", channelID, "message", messageID, "error", err)
			lastErr = err
		}
	}

	return lastErr
}

// Internal structures
type discordWebhookPayload struct {
	Content string         `json:"content"`
	Embeds  []discordEmbed `json:"embeds"`
}

type discordEmbedThumbnail struct {
	URL string `json:"url,omitempty"`
}

type discordEmbedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type discordEmbedFooter struct {
	Text string `json:"text,omitempty"`
}

type discordEmbed struct {
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	URL         string                `json:"url,omitempty"`
	Timestamp   string                `json:"timestamp,omitempty"`
	Color       int                   `json:"color,omitempty"`
	Thumbnail   discordEmbedThumbnail `json:"thumbnail,omitempty"`
	Fields      []discordEmbedField   `json:"fields,omitempty"`
	Footer      discordEmbedFooter    `json:"footer,omitempty"`
}

type discordMessageResponse struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

func createDiscordPayload(deal models.DealInfo) discordWebhookPayload {
	embed := formatDealToEmbed(deal)
	return discordWebhookPayload{
		Content: "", // clear any hidden message text
		Embeds:  []discordEmbed{embed},
	}
}

func formatDealToEmbed(deal models.DealInfo) discordEmbed {
	// 1. Determine Title
	title := deal.Title
	if deal.CleanTitle != "" {
		title = deal.CleanTitle
	}

	// 2. Determine Title URL (Product Link vs Thread Link)
	titleURL := deal.PostURL
	if deal.ActualDealURL != "" {
		titleURL = deal.ActualDealURL
	}

	// 3. Append Sentiment Emoji
	if deal.AIProcessed && deal.IsLavaHot {
		title += " 🔥"
	}

	likes, comments, views := deal.Stats()

	// Construct Description
	var descriptionBuilder strings.Builder

	// Add RFD Thread link(s)
	// Because processor.sortThreads() orders these by LikeCount desc, the links
	// here naturally print in order of most popular to least popular.
	for _, thread := range deal.Threads {
		descriptionBuilder.WriteString(fmt.Sprintf("[RFD](%s) ", thread.PostURL))
	}
	descriptionBuilder.WriteString("\n\n")

	// 5. Heat Color
	heatScore := CalculateHeatScore(likes, comments, views)
	embedColor := colorColdDeal

	if deal.HasBeenHot || deal.IsLavaHot {
		embedColor = colorHotDeal
	} else if likes >= 2 {
		if deal.HasBeenWarm {
			embedColor = colorWarmDeal
		} else {
			embedColor = getHeatColor(heatScore)
		}
	}

	// 6. Thumbnail
	var thumbnail discordEmbedThumbnail
	if deal.ThreadImageURL != "" {
		thumbnail.URL = deal.ThreadImageURL
	}

	footerText := "❌ Unknown"
	if deal.Category != "" {
		footerText = fmt.Sprintf("%s %s", util.GetCategoryEmoji(deal.Category), deal.Category)
	}

	// Add Engagement Metrics directly to description
	likeIcon := "👍"
	if likes < 0 {
		likeIcon = "👎"
	}
	descriptionBuilder.WriteString(fmt.Sprintf("%s %d  💬 %d  👀 %d", likeIcon, likes, comments, views))

	var timestampStr string
	if !deal.PublishedTimestamp.IsZero() {
		timestampStr = deal.PublishedTimestamp.Format(time.RFC3339)
	}

	emailEmbed := discordEmbed{
		Title:       title,
		URL:         titleURL,
		Description: descriptionBuilder.String(),
		Timestamp:   timestampStr,
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: footerText, // Generalized category footer
		},
	}

	return emailEmbed
}

// doRequest handles the shared retry/rate-limit/backoff loop for Discord API calls.
// It returns the response body on success.
func (c *Client) doRequest(ctx context.Context, method, targetURL string, payload discordWebhookPayload) ([]byte, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			slog.Warn("Retrying Discord request", "method", method, "attempt", attempt, "error", lastErr)
		}

		// Rate limit to avoid hitting Discord's webhook rate limits.
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter wait: %w", err)
		}

		req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bot "+c.botToken)

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		bodyBytes, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()

		if readErr != nil {
			lastErr = fmt.Errorf("failed to read discord response body: %w", readErr)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return bodyBytes, nil
		}

		lastErr = fmt.Errorf("discord %s failed: %s, body: %s", method, resp.Status, string(bodyBytes))

		if backoff := retryBackoff(resp, attempt); backoff > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			continue
		}

		// Non-retryable status code
		return nil, lastErr
	}

	return nil, fmt.Errorf("discord %s failed after %d retries: %w", method, maxRetries, lastErr)
}

// retryBackoff returns a backoff duration if the response is retryable (429 or 5xx).
// Returns 0 if the response should not be retried.
func retryBackoff(resp *http.Response, attempt int) time.Duration {
	if resp.StatusCode == http.StatusTooManyRequests {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				return time.Duration(seconds) * time.Second
			}
		}
		return time.Duration(1<<attempt) * time.Second
	}

	if resp.StatusCode >= 500 {
		return time.Duration(1<<attempt) * time.Second
	}

	return 0
}

// CalculateHeatScore determines the heat of a deal based on engagement.
func CalculateHeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0.0
	}
	// Clamp negatives — downvoted deals shouldn't generate heat
	effectiveLikes := max(likes, 0)
	effectiveComments := max(comments, 0)
	// Comments are weighted 2x since they represent deeper engagement
	engagement := float64(effectiveLikes) + 2.0*float64(effectiveComments)
	return engagement / float64(views)
}

// IsWarm determines if a deal is considered warm.
func (c *Client) IsWarm(deal models.DealInfo) bool {
	likes, comments, views := deal.Stats()
	return likes >= 2 && CalculateHeatScore(likes, comments, views) > heatScoreThresholdWarm
}

// IsHot determines if a deal is considered hot.
func (c *Client) IsHot(deal models.DealInfo) bool {
	return deal.IsLavaHot
}

func getHeatColor(heatScore float64) int {
	if heatScore > heatScoreThresholdWarm {
		return colorWarmDeal
	}
	return colorColdDeal
}
