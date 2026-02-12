package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	colorColdDeal    = 3092790  // #2F3136
	colorWarmDeal    = 16753920 // #FFA500
	colorHotDeal     = 16711680 // #FF0000
	colorVeryHotDeal = 16776960 // #FFFF00 (yellow)

	heatScoreThresholdCold = 0.05
	heatScoreThresholdWarm = 0.1
	heatScoreThresholdHot  = 0.25

	maxRetries = 3
)

type Client struct {
	webhookURL  string
	client      *http.Client
	rateLimiter *rate.Limiter
}

func New(webhookURL string) *Client {
	return &Client{
		webhookURL:  webhookURL,
		client:      &http.Client{Timeout: 10 * time.Second},
		rateLimiter: rate.NewLimiter(rate.Every(60*time.Second/25), 1), // 25 req/min
	}
}

// Send sends a new deal notification and returns the message ID.
func (c *Client) Send(ctx context.Context, deal models.DealInfo) (string, error) {
	if c.webhookURL == "" {
		return "", nil
	}
	embed := formatDealToEmbed(deal)

	parsedURL, err := url.Parse(c.webhookURL)
	if err != nil {
		return "", err
	}
	q := parsedURL.Query()
	q.Set("wait", "true")
	parsedURL.RawQuery = q.Encode()

	body, err := c.doRequest(ctx, "POST", parsedURL.String(), embed)
	if err != nil {
		return "", err
	}

	var msgResponse discordMessageResponse
	if err := json.Unmarshal(body, &msgResponse); err != nil {
		return "", err
	}
	return msgResponse.ID, nil
}

// Update updates an existing notification.
func (c *Client) Update(ctx context.Context, messageID string, deal models.DealInfo) error {
	if c.webhookURL == "" || messageID == "" {
		return nil
	}
	embed := formatDealToEmbed(deal)

	parsedBaseURL, err := url.Parse(c.webhookURL)
	if err != nil {
		return err
	}
	patchURL := fmt.Sprintf("%s://%s%s/messages/%s", parsedBaseURL.Scheme, parsedBaseURL.Host, parsedBaseURL.Path, messageID)

	_, err = c.doRequest(ctx, "PATCH", patchURL, embed)
	return err
}

// Internal structures
type discordWebhookPayload struct {
	Content string         `json:"content,omitempty"`
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

func formatDealToEmbed(deal models.DealInfo) discordEmbed {
	statsSuffix := fmt.Sprintf(" (%d/%d/%d)", deal.LikeCount, deal.CommentCount, deal.ViewCount)
	title := deal.Title + statsSuffix

	var description string
	if deal.ActualDealURL != "" {
		description = fmt.Sprintf("[Link to Item](%s)", deal.ActualDealURL)
	}

	var thumbnail discordEmbedThumbnail
	if deal.ThreadImageURL != "" {
		thumbnail.URL = deal.ThreadImageURL
	}

	var isoTimestamp string
	if !deal.PublishedTimestamp.IsZero() {
		isoTimestamp = deal.PublishedTimestamp.Format(time.RFC3339)
	}

	heatScore := calculateHeatScore(deal.LikeCount, deal.CommentCount, deal.ViewCount)
	embedColor := getHeatColor(heatScore)

	// Item link in Description for a clickable mobile-friendly target.
	return discordEmbed{
		Title:       title,
		URL:         deal.PostURL,
		Description: description,
		Timestamp:   isoTimestamp,
		Color:       embedColor,
		Thumbnail:   thumbnail,
	}
}

// doRequest handles the shared retry/rate-limit/backoff loop for Discord API calls.
// It returns the response body on success.
func (c *Client) doRequest(ctx context.Context, method, targetURL string, embed discordEmbed) ([]byte, error) {
	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
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

func calculateHeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0.0
	}
	return float64(likes+comments) / float64(views)
}

func getHeatColor(heatScore float64) int {
	if heatScore > heatScoreThresholdHot {
		return colorVeryHotDeal
	}
	if heatScore > heatScoreThresholdWarm {
		return colorHotDeal
	}
	if heatScore > heatScoreThresholdCold {
		return colorWarmDeal
	}
	return colorColdDeal
}

