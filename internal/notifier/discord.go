package notifier

import (
	"bytes"
	"context"

	"encoding/base64"

	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"mime/multipart"
	"net/http"
	"net/url"

	"strconv"
	"strings"

	"time"

	"github.com/pauljones0/rfd-discord-standalone/internal/dealquality"

	"github.com/pauljones0/rfd-discord-standalone/internal/models"

	"github.com/pauljones0/rfd-discord-standalone/internal/util"
)

const (
	colorColdDeal = 2829617  // #2B2D31 (Discord dark mode embed background) — alert fired, but quiet
	colorWarmDeal = 16098851 // #F5A623 (amber)            — getting traction
	colorHotDeal  = 16723320 // #FF2D78 (magenta-pink)     — blowing up, act fast

	heatScoreThresholdWarm = 0.05
	heatScoreThresholdHot  = 0.20

	noViewsEngagementThresholdWarm = 15
	noViewsEngagementThresholdHot  = 40

	maxRetries = 3

	discordAPIBase = "https://discord.com/api/v10"
)

// Send sends a new deal notification to all subscribed channels.
// Returns a map of ChannelID -> MessageID.
func (c *Client) Send(ctx context.Context, deal models.DealInfo, subs []models.Subscription) (map[string]string, error) {
	if c.botToken == "" {
		return nil, nil // No bot token configured
	}

	payload := createDiscordPayload(deal)
	results := make(map[string]string)
	seenChannels := make(map[string]bool)

	for _, sub := range subs {
		// Several filters can match the same deal in one channel.
		if seenChannels[sub.ChannelID] {
			continue
		}
		seenChannels[sub.ChannelID] = true
		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		body, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send deal to channel", "processor", "rfd", "channel", sub.ChannelID, "error", err)
			continue
		}

		var msgResponse discordMessageResponse
		if err := json.Unmarshal(body, &msgResponse); err != nil {
			slog.Error("Failed to parse discord message response", "processor", "rfd", "channel", sub.ChannelID, "error", err)
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
	var errs []error

	for channelID, messageID := range deal.DiscordMessageIDs {
		patchURL := fmt.Sprintf("%s/channels/%s/messages/%s", discordAPIBase, channelID, messageID)
		_, err := c.doRequest(ctx, "PATCH", patchURL, payload)
		if err != nil {
			slog.Error("Failed to update deal", "processor", "rfd", "channel", channelID, "message", messageID, "error", err)
			errs = append(errs, fmt.Errorf("channel %s: %w", channelID, err))
		}
	}

	return errors.Join(errs...)
}

// Internal structures
type discordWebhookPayload struct {
	Content         string                  `json:"content"`
	Embeds          []discordEmbed          `json:"embeds"`
	Attachments     []discordAttachment     `json:"attachments,omitempty"`
	AllowedMentions *discordAllowedMentions `json:"allowed_mentions,omitempty"`
	Nonce           string                  `json:"nonce,omitempty"`
	EnforceNonce    bool                    `json:"enforce_nonce,omitempty"`

	// Internal field for multipart payload
	ImageBase64 string `json:"-"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordAttachment struct {
	ID       int    `json:"id"`
	Filename string `json:"filename"`
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
	titleURL := preferredDealURL(deal)

	// 3. Append Sentiment Emoji
	discountBacked := dealquality.RFDWarmHotEligible(deal)
	if discountBacked && deal.HasBeenHot {
		title += " 🔥"
	}

	// 5. Heat Color
	likes, comments, views, hasViews := deal.EngagementStats()
	liveWarm := discountBacked && isWarmByEngagement(likes, comments, views, hasViews)
	liveHot := discountBacked && isHotByEngagement(likes, comments, views, hasViews)
	embedColor := colorColdDeal

	if (discountBacked && deal.HasBeenHot) || liveHot {
		embedColor = colorHotDeal
	} else if (discountBacked && deal.HasBeenWarm) || liveWarm {
		embedColor = colorWarmDeal
	}

	// Construct Description
	var descriptionBuilder strings.Builder

	// Add RFD Thread link(s)
	// Because processor.sortThreads() orders these by LikeCount desc, the links
	// here naturally print in order of most popular to least popular.
	for _, thread := range deal.Threads {
		descriptionBuilder.WriteString(fmt.Sprintf("[RFD](%s) ", thread.PostURL))
	}
	descriptionBuilder.WriteString("\n\n")

	// 6. Thumbnail
	var thumbnail discordEmbedThumbnail
	if deal.ThreadImageURL != "" {
		thumbnail.URL = deal.ThreadImageURL
	}

	var footerText string
	if deal.Category != "" || deal.Retailer != "" {
		var emoji string
		if deal.Category != "" {
			emoji = util.GetCategoryEmoji(deal.Category)
		}
		footerText = strings.TrimSpace(fmt.Sprintf("%s %s", emoji, deal.Retailer))
	}

	// Add Engagement Metrics directly to description
	likeIcon := "👍"
	if likes < 0 {
		likeIcon = "👎"
	}
	descriptionBuilder.WriteString(formatEngagementLine(likeIcon, likes, comments, views, hasViews))

	var timestampStr string
	if !deal.PublishedTimestamp.IsZero() {
		timestampStr = deal.PublishedTimestamp.Format(time.RFC3339)
	}

	embed := discordEmbed{
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

	return embed
}

func preferredDealURL(deal models.DealInfo) string {
	if safeURL, ok := discordEmbedURL(deal.ActualDealURL); ok {
		return safeURL
	}
	if safeURL, ok := discordEmbedURL(deal.PostURL); ok {
		return safeURL
	}
	return ""
}

func discordEmbedURL(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.ContainsAny(raw, " \t\r\n<>") {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Host == "" {
		return "", false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false
	}
	return parsed.String(), true
}

// doRequest handles the shared retry/rate-limit/backoff loop for Discord API calls.
// It returns the response body on success.
func (c *Client) doRequest(ctx context.Context, method, targetURL string, payload discordWebhookPayload) ([]byte, error) {
	start := time.Now()

	var payloadBodyBytes []byte
	var contentType = "application/json"

	if payload.ImageBase64 != "" {
		imageBytes, err := base64.StdEncoding.DecodeString(payload.ImageBase64)
		if err == nil {
			var b bytes.Buffer
			w := multipart.NewWriter(&b)

			jsonPart, _ := w.CreateFormField("payload_json")
			jsonBytes, _ := json.Marshal(payload)
			jsonPart.Write(jsonBytes)

			filePart, _ := w.CreateFormFile("files[0]", "image.jpg")
			filePart.Write(imageBytes)

			w.Close()
			payloadBodyBytes = b.Bytes()
			contentType = w.FormDataContentType()
		}
	}

	if payloadBodyBytes == nil {
		jsonBytes, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		payloadBodyBytes = jsonBytes
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

		req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(payloadBodyBytes))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", contentType)
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
			slog.Debug("Discord API call succeeded", "method", method, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
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

	slog.Warn("Discord API call failed after retries", "method", method, "retries", maxRetries, "duration_ms", time.Since(start).Milliseconds())
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
// Comments are weighted 2x since they represent deeper engagement.
func CalculateHeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0.0
	}
	effectiveLikes := max(likes, 0)
	effectiveComments := max(comments, 0)
	engagement := float64(effectiveLikes) + 2.0*float64(effectiveComments)
	return engagement / float64(views)
}

func calculateNoViewsEngagement(likes, comments int) int {
	return max(likes, 0) + 2*max(comments, 0)
}

func isWarmByEngagement(likes, comments, views int, hasViews bool) bool {
	if likes < 2 {
		return false
	}
	if hasViews {
		return CalculateHeatScore(likes, comments, views) > heatScoreThresholdWarm
	}
	return calculateNoViewsEngagement(likes, comments) >= noViewsEngagementThresholdWarm
}

func isHotByEngagement(likes, comments, views int, hasViews bool) bool {
	if likes < 2 {
		return false
	}
	if hasViews {
		return CalculateHeatScore(likes, comments, views) > heatScoreThresholdHot
	}
	return calculateNoViewsEngagement(likes, comments) >= noViewsEngagementThresholdHot
}

func formatEngagementLine(likeIcon string, likes, comments, views int, hasViews bool) string {
	if hasViews {
		return fmt.Sprintf("%s %d  💬 %d  👀 %d", likeIcon, likes, comments, views)
	}
	return fmt.Sprintf("%s %d  💬 %d", likeIcon, likes, comments)
}

// IsWarm determines if a deal is considered warm based on community engagement.
func (c *Client) IsWarm(deal models.DealInfo) bool {
	likes, comments, views, hasViews := deal.EngagementStats()
	return isWarmByEngagement(likes, comments, views, hasViews)
}

// IsHot determines if a deal is considered hot based on community engagement.
func (c *Client) IsHot(deal models.DealInfo) bool {
	likes, comments, views, hasViews := deal.EngagementStats()
	return isHotByEngagement(likes, comments, views, hasViews)
}
