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

	"golang.org/x/time/rate"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
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
	Content     string              `json:"content"`
	Embeds      []discordEmbed      `json:"embeds"`
	Attachments []discordAttachment `json:"attachments,omitempty"`

	// Internal field for multipart payload
	ImageBase64 string `json:"-"`
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
	if deal.HasBeenHot {
		title += " 🔥"
	}

	// 5. Heat Color
	likes, comments, views, hasViews := deal.EngagementStats()
	liveWarm := isWarmByEngagement(likes, comments, views, hasViews)
	liveHot := isHotByEngagement(likes, comments, views, hasViews)
	embedColor := colorColdDeal

	if deal.HasBeenHot || liveHot {
		embedColor = colorHotDeal
	} else if deal.HasBeenWarm || liveWarm {
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

func (c *Client) sendEmbedToSubscriptions(ctx context.Context, processor, title string, embed discordEmbed, subs []models.Subscription) error {
	if c.botToken == "" {
		return nil
	}

	payload := discordWebhookPayload{
		Content: "",
		Embeds:  []discordEmbed{embed},
	}

	for i, sub := range subs {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		_, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send deal to channel",
				"processor", processor,
				"channel", sub.ChannelID,
				"title", title,
				"error", err,
			)
		} else {
			slog.Info("Deal sent",
				"processor", processor,
				"channel", sub.ChannelID,
				"title", title,
			)
		}
	}

	return nil
}

// --- Facebook Deal Notifications ---

// SendFacebookDeal sends a Facebook car deal notification to all subscribed channels.
func (c *Client) SendFacebookDeal(ctx context.Context, title, url, summary, knownIssues string, askingPrice, carfaxValue, vmrRetail float64, isWarm, isLavaHot bool, subs []models.Subscription) error {
	if c.botToken == "" {
		return nil
	}

	embed := formatFacebookEmbed(title, url, summary, knownIssues, askingPrice, carfaxValue, vmrRetail, isWarm, isLavaHot)
	payload := discordWebhookPayload{
		Content: "",
		Embeds:  []discordEmbed{embed},
	}

	for i, sub := range subs {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}
		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		_, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send Facebook deal to channel", "processor", "facebook", "channel", sub.ChannelID, "title", title, "error", err)
		} else {
			slog.Info("Facebook deal sent", "processor", "facebook", "channel", sub.ChannelID, "title", title)
		}
	}

	return nil
}

func formatFacebookEmbed(title, url, summary, knownIssues string, askingPrice, carfaxValue, vmrRetail float64, isWarm, isLavaHot bool) discordEmbed {
	if isLavaHot {
		title += " 🔥"
	}

	embedColor := colorWarmDeal
	if isLavaHot {
		embedColor = colorHotDeal
	}

	var fields []discordEmbedField

	// Dense single-line pricing with multiple valuation sources
	priceVal := formatPriceShort(askingPrice)
	if carfaxValue > 0 && vmrRetail > 0 {
		avg := (carfaxValue + vmrRetail) / 2
		discount := (1 - askingPrice/avg) * 100
		priceVal = fmt.Sprintf("%s / %s Carfax / %s VMR", priceVal, formatPriceShort(carfaxValue), formatPriceShort(vmrRetail))
		if discount > 0 {
			priceVal += fmt.Sprintf(" / %.0f%% off avg", discount)
		}
	} else if carfaxValue > 0 {
		discount := (1 - askingPrice/carfaxValue) * 100
		priceVal = fmt.Sprintf("%s / %s Carfax", priceVal, formatPriceShort(carfaxValue))
		if discount > 0 {
			priceVal += fmt.Sprintf(" / %.0f%% off", discount)
		}
	} else if vmrRetail > 0 {
		discount := (1 - askingPrice/vmrRetail) * 100
		priceVal = fmt.Sprintf("%s / %s VMR", priceVal, formatPriceShort(vmrRetail))
		if discount > 0 {
			priceVal += fmt.Sprintf(" / %.0f%% off", discount)
		}
	}
	fields = append(fields, discordEmbedField{Name: "Price", Value: priceVal})

	if knownIssues != "" {
		fields = append(fields, discordEmbedField{Name: "⚠️ Watch Out", Value: knownIssues})
	}

	return discordEmbed{
		Title:       title,
		URL:         url,
		Description: summary,
		Color:       embedColor,
		Fields:      fields,
		Footer: discordEmbedFooter{
			Text: "FB Marketplace Car Deal",
		},
	}
}

// formatPriceShort formats a price as compact shorthand (e.g. 12500 → "12.5k").
func formatPriceShort(v float64) string {
	if v < 1000 {
		return fmt.Sprintf("$%.0f", v)
	}
	k := v / 1000
	if k == float64(int(k)) {
		return fmt.Sprintf("%.0fk", k)
	}
	return fmt.Sprintf("%.1fk", k)
}

// SendCoreAlert sends a new Core deal notification to all subscribed channels.
func (c *Client) SendCoreAlert(ctx context.Context, alert models.CoreAlert, subs []models.Subscription) (map[string]string, error) {
	if c.botToken == "" {
		return nil, nil
	}

	payload := createCoreAlertPayload(alert)
	results := make(map[string]string)
	sentChannels := make(map[string]bool)

	for _, sub := range subs {
		if sentChannels[sub.ChannelID] {
			continue
		}
		sentChannels[sub.ChannelID] = true

		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		body, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send Core deal to channel", "processor", "core", "channel", sub.ChannelID, "product", alert.Deal.ProductName, "error", err)
			continue
		}

		var msgResponse discordMessageResponse
		if err := json.Unmarshal(body, &msgResponse); err != nil {
			slog.Error("Failed to parse discord message response for Core deal", "processor", "core", "channel", sub.ChannelID, "error", err)
			continue
		}
		results[sub.ChannelID] = msgResponse.ID
	}

	return results, nil
}

// UpdateCoreAlert updates an existing Core deal notification.
func (c *Client) UpdateCoreAlert(ctx context.Context, alert models.CoreAlert) error {
	if c.botToken == "" || len(alert.MessageIDs) == 0 {
		return nil
	}

	payload := createCoreAlertPayload(alert)
	var errs []error

	for channelID, messageID := range alert.MessageIDs {
		patchURL := fmt.Sprintf("%s/channels/%s/messages/%s", discordAPIBase, channelID, messageID)
		_, err := c.doRequest(ctx, "PATCH", patchURL, payload)
		if err != nil {
			slog.Error("Failed to update Core deal", "processor", "core", "channel", channelID, "message", messageID, "error", err)
			errs = append(errs, fmt.Errorf("channel %s: %w", channelID, err))
		}
	}

	return errors.Join(errs...)
}

// SendCoreSystemAlert sends operational Core pipeline failures to Core channels.
func (c *Client) SendCoreSystemAlert(ctx context.Context, alert models.CoreSystemAlert, subs []models.Subscription) error {
	if c.botToken == "" {
		return nil
	}

	payload := createCoreSystemAlertPayload(alert)
	sentChannels := make(map[string]bool)
	var errs []error

	for _, sub := range subs {
		if sentChannels[sub.ChannelID] {
			continue
		}
		sentChannels[sub.ChannelID] = true

		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		if _, err := c.doRequest(ctx, "POST", urlStr, payload); err != nil {
			slog.Error("Failed to send Core system alert", "processor", "core", "channel", sub.ChannelID, "title", alert.Title, "error", err)
			errs = append(errs, fmt.Errorf("channel %s: %w", sub.ChannelID, err))
		}
	}

	return errors.Join(errs...)
}

func createCoreAlertPayload(alert models.CoreAlert) discordWebhookPayload {
	embed := formatCoreAlertEmbed(alert)
	payload := discordWebhookPayload{
		Content: "",
		Embeds:  []discordEmbed{embed},
	}
	if alert.Deal.ImageBase64 != "" {
		payload.ImageBase64 = alert.Deal.ImageBase64
		payload.Attachments = []discordAttachment{
			{ID: 0, Filename: "image.jpg"},
		}
		payload.Embeds[0].Thumbnail = discordEmbedThumbnail{
			URL: "attachment://image.jpg",
		}
	}
	return payload
}

func createCoreSystemAlertPayload(alert models.CoreSystemAlert) discordWebhookPayload {
	return discordWebhookPayload{
		Content: "",
		Embeds:  []discordEmbed{formatCoreSystemAlertEmbed(alert)},
	}
}

func formatCoreSystemAlertEmbed(alert models.CoreSystemAlert) discordEmbed {
	title := strings.TrimSpace(alert.Title)
	if title == "" {
		title = "Core notification pipeline issue"
	}
	severity := strings.ToLower(strings.TrimSpace(alert.Severity))
	if severity == "" {
		severity = "warning"
	}
	component := strings.TrimSpace(alert.Component)
	if component == "" {
		component = "core-notification-ingest"
	}
	occurredAt := alert.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	var desc strings.Builder
	desc.WriteString("Severity: **")
	desc.WriteString(strings.ToUpper(severity))
	desc.WriteString("**\n")
	desc.WriteString("Component: `")
	desc.WriteString(discordLimit(component, 256))
	desc.WriteString("`")
	if alert.Details != "" {
		desc.WriteString("\n\n")
		desc.WriteString(discordLimit(alert.Details, 3500))
	}

	fields := []discordEmbedField{
		{Name: "Occurred", Value: occurredAt.UTC().Format(time.RFC3339), Inline: true},
	}
	if alert.EventID != "" {
		fields = append(fields, discordEmbedField{Name: "Event", Value: discordLimit(alert.EventID, 1024), Inline: true})
	}
	if alert.SourcePackage != "" {
		fields = append(fields, discordEmbedField{Name: "Source", Value: discordLimit(alert.SourcePackage, 1024), Inline: true})
	}
	for _, field := range alert.Fields {
		if len(fields) >= 25 {
			break
		}
		name := strings.TrimSpace(field.Name)
		value := strings.TrimSpace(field.Value)
		if name == "" || value == "" {
			continue
		}
		fields = append(fields, discordEmbedField{
			Name:   discordLimit(name, 256),
			Value:  discordLimit(value, 1024),
			Inline: false,
		})
	}

	return discordEmbed{
		Title:       discordLimit(title, 256),
		Description: desc.String(),
		Timestamp:   occurredAt.UTC().Format(time.RFC3339),
		Color:       coreSystemSeverityColor(severity),
		Fields:      fields,
		Footer: discordEmbedFooter{
			Text: "Core System Alert",
		},
	}
}

func coreSystemSeverityColor(severity string) int {
	switch severity {
	case "critical", "error":
		return 15158332 // red
	case "info", "test":
		return 3447003 // blue
	default:
		return 15844367 // yellow
	}
}

func discordLimit(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if max <= 0 || len(runes) <= max {
		return value
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func formatCoreAlertEmbed(alert models.CoreAlert) discordEmbed {
	deal := alert.Deal
	title := deal.ProductName
	if deal.AnomalyType != "" && deal.AnomalyType != "Normal" && deal.AnomalyType != "Deal" {
		title = fmt.Sprintf("🚨 [%s] %s", strings.ToUpper(deal.AnomalyType), deal.ProductName)
	}

	var descBuilder strings.Builder

	// Price display
	descBuilder.WriteString(fmt.Sprintf("Price: **C$%.2f**", deal.PriceCAD))
	if deal.OriginalCurr != "CAD" && deal.OriginalPrice > 0 {
		descBuilder.WriteString(fmt.Sprintf("  •  Original: **%s %.2f**", strings.ToUpper(deal.OriginalCurr), deal.OriginalPrice))
	}
	descBuilder.WriteString("\n\n")

	// Store and Links Amalagamation
	if len(alert.StoreNames) > 0 {
		descBuilder.WriteString("**Available At:**\n")
		for i, store := range alert.StoreNames {
			link := ""
			if i < len(alert.Links) && alert.Links[i] != "" {
				link = alert.Links[i]
			}
			if link != "" {
				descBuilder.WriteString(fmt.Sprintf("• [%s](%s)\n", store, link))
			} else {
				descBuilder.WriteString(fmt.Sprintf("• **%s**\n", store))
			}
		}
		descBuilder.WriteString("\n")
	}

	// Historical comparison details
	descBuilder.WriteString("📊 **Historical Stats:**\n")
	descBuilder.WriteString(fmt.Sprintf("• Min price seen: **C$%.2f**\n", deal.MinPriceSeen))
	descBuilder.WriteString(fmt.Sprintf("• Median (p50): **C$%.2f**\n", deal.P50PriceSeen))
	descBuilder.WriteString(fmt.Sprintf("• 25th percentile (p25): **C$%.2f**\n", deal.P25PriceSeen))
	descBuilder.WriteString(fmt.Sprintf("• Total price observations: **%d**\n", deal.HistoryCount))

	if deal.BoxPlot != "" {
		descBuilder.WriteString("\n**Price Distribution:**\n")
		descBuilder.WriteString(fmt.Sprintf("```\n%s\n```\n", deal.BoxPlot))
		descBuilder.WriteString("*( `[`/`]` = Min/Max, `█` = IQR, `|` = Median, `▼` = Current Price )*\n")
	}

	if deal.Category != "" {
		descBuilder.WriteString(fmt.Sprintf("\nCategory: %s", deal.Category))
	}

	embedColor := 7098599 // #6C5CE7 (premium dark violet - default)

	// Color Hierarchy: Price Error > Steal > Normal
	// Since CoreDeal doesn't have engagement/heat yet, we focus on deal "amount"
	switch deal.AnomalyType {
	case "Price Error / Used":
		embedColor = 15158332 // #E74C3C (vibrant red) - critical
	case "Steal":
		embedColor = 15844367 // #F1C40F (vibrant gold) - high value
	case "Deal":
		embedColor = 3447003 // #3498DB (blue) - solid deal
	}

	return discordEmbed{
		Title:       title,
		URL:         deal.Link,
		Description: descBuilder.String(),
		Color:       embedColor,
		Footer: discordEmbedFooter{
			Text: "Core Deal Alert • Local Price Analysis",
		},
	}
}

// --- eBay Deal Notifications ---

// SendEbayDeal sends a new eBay deal notification to all subscribed channels.
// Returns a map of ChannelID -> MessageID.
func (c *Client) SendEbayDeal(ctx context.Context, item ebay.EbayItem, subs []models.Subscription) (map[string]string, error) {
	if c.botToken == "" {
		return nil, nil
	}

	payload := createEbayPayload(item)
	results := make(map[string]string)
	sentChannels := make(map[string]bool)

	for _, sub := range subs {
		if sentChannels[sub.ChannelID] {
			continue
		}
		sentChannels[sub.ChannelID] = true

		urlStr := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		body, err := c.doRequest(ctx, "POST", urlStr, payload)
		if err != nil {
			slog.Error("Failed to send eBay deal to channel", "processor", "ebay", "channel", sub.ChannelID, "item", item.Title, "error", err)
			continue
		}

		var msgResponse discordMessageResponse
		if err := json.Unmarshal(body, &msgResponse); err != nil {
			slog.Error("Failed to parse discord message response for eBay deal", "processor", "ebay", "channel", sub.ChannelID, "error", err)
			continue
		}
		results[sub.ChannelID] = msgResponse.ID
	}

	return results, nil
}

func createEbayPayload(item ebay.EbayItem) discordWebhookPayload {
	embed := formatEbayEmbed(item)
	return discordWebhookPayload{
		Content: "",
		Embeds:  []discordEmbed{embed},
	}
}

func formatEbayEmbed(item ebay.EbayItem) discordEmbed {
	title := item.Title
	var descBuilder strings.Builder
	itemURL := item.ItemURL

	if itemURL != "" {
		itemURL = util.CleanProductURL(itemURL)
		if cleanedURL, changed := util.CleanReferralLink(itemURL, "", ""); changed {
			itemURL = cleanedURL
		}
	}

	if item.OriginalPrice > 0 && item.OriginalPrice != item.PreviousPrice {
		descBuilder.WriteString(fmt.Sprintf("~~%s~~ (orig)  •  ", formatEbayMoney(item.OriginalPrice, item.Currency)))
	}

	if item.PreviousPrice > 0 && item.CurrentPrice > 0 && item.PriceDrop > 0 {
		descBuilder.WriteString(fmt.Sprintf(
			"~~%s~~ -> **%s**  (-%s, -%.0f%%)",
			formatEbayMoney(item.PreviousPrice, item.Currency),
			formatEbayMoney(item.CurrentPrice, item.Currency),
			formatEbayMoney(item.PriceDrop, item.Currency),
			item.PercentDrop,
		))
	} else if item.CurrentPrice > 0 {
		descBuilder.WriteString(fmt.Sprintf("**%s**", formatEbayMoney(item.CurrentPrice, item.Currency)))
	}
	if item.DropCount > 0 {
		if descBuilder.Len() > 0 {
			descBuilder.WriteString("  •  ")
		}
		descBuilder.WriteString(fmt.Sprintf("%s drop", ordinal(item.DropCount)))
	}

	if item.OriginalPrice > 0 && item.OriginalPrice > item.CurrentPrice && item.OriginalPrice != item.PreviousPrice {
		totalDrop := item.OriginalPrice - item.CurrentPrice
		totalPct := (totalDrop / item.OriginalPrice) * 100
		descBuilder.WriteString(fmt.Sprintf("\n*(total savings: -%s, -%.0f%%)*", formatEbayMoney(item.OriginalPrice-item.CurrentPrice, item.Currency), totalPct))
	}

	if item.IsGoodDeal && item.SoldMedian > 0 {
		descBuilder.WriteString(fmt.Sprintf("\n✅ **Good Deal!** (Median sold: %s)", formatEbayMoney(item.SoldMedian, item.Currency)))
	} else if item.SoldMedian > 0 {
		descBuilder.WriteString(fmt.Sprintf("\n*(Market median: %s)*", formatEbayMoney(item.SoldMedian, item.Currency)))
	}

	if item.CouponDiscount > 0 {
		couponLabel := "coupon included"
		if strings.Contains(item.CouponCode, "+") {
			couponLabel = "multiple coupons included"
		}
		if descBuilder.Len() > 0 {
			descBuilder.WriteString("  •  ")
		}
		descBuilder.WriteString(couponLabel)
	}

	var meta []string
	if item.Seller != "" {
		sellerValue := item.Seller
		if sellerURL := ebaySellerProfileURL(item); sellerURL != "" {
			sellerValue = fmt.Sprintf("[%s](%s)", item.Seller, sellerURL)
		}
		if item.SellerFeedbackPercentage != "" && item.SellerFeedbackScore > 0 {
			sellerValue += fmt.Sprintf(" %s/%s", item.SellerFeedbackPercentage, formatCountCompact(item.SellerFeedbackScore))
		} else if item.SellerFeedbackPercentage != "" {
			sellerValue += " " + item.SellerFeedbackPercentage
		} else if item.SellerFeedbackScore > 0 {
			sellerValue += " " + formatCountCompact(item.SellerFeedbackScore)
		}
		meta = append(meta, sellerValue)
	}
	if item.Condition != "" {
		meta = append(meta, item.Condition)
	}
	if !item.ListedAt.IsZero() {
		meta = append(meta, fmt.Sprintf("Listed <t:%d:f>", item.ListedAt.Unix()))
	}
	if len(meta) > 0 {
		if descBuilder.Len() > 0 {
			descBuilder.WriteString("\n")
		}
		descBuilder.WriteString(strings.Join(meta, "  •  "))
	}

	var thumbnail discordEmbedThumbnail
	if item.ImageURL != "" {
		thumbnail.URL = item.ImageURL
	}

	return discordEmbed{
		Title:       title,
		URL:         itemURL,
		Description: descBuilder.String(),
		Color:       colorWarmDeal,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: ebayMarketplaceLabel(item) + " • Price Drop Alert",
		},
	}
}

func formatEbayMoney(amount float64, currency string) string {
	switch strings.ToUpper(currency) {
	case "", "CAD":
		return fmt.Sprintf("C$%.2f", amount)
	case "USD":
		return fmt.Sprintf("US$%.2f", amount)
	default:
		return fmt.Sprintf("%s %.2f", strings.ToUpper(currency), amount)
	}
}

func formatCountCompact(n int) string {
	if n >= 1000 {
		value := float64(n) / 1000
		if n%1000 == 0 {
			return fmt.Sprintf("%.0fk", value)
		}
		return fmt.Sprintf("%.1fk", value)
	}
	return strconv.Itoa(n)
}

func ordinal(n int) string {
	if n <= 0 {
		return strconv.Itoa(n)
	}
	if n%100 >= 11 && n%100 <= 13 {
		return fmt.Sprintf("%dth", n)
	}
	switch n % 10 {
	case 1:
		return fmt.Sprintf("%dst", n)
	case 2:
		return fmt.Sprintf("%dnd", n)
	case 3:
		return fmt.Sprintf("%drd", n)
	default:
		return fmt.Sprintf("%dth", n)
	}
}

func ebayMarketplaceLabel(item ebay.EbayItem) string {
	switch item.Marketplace {
	case "EBAY_CA":
		return "eBay Canada"
	case "EBAY_US":
		return "eBay US"
	}

	if host := ebayItemHost(item.ItemURL); host != "" {
		switch {
		case strings.Contains(host, "ebay.ca"):
			return "eBay Canada"
		case strings.Contains(host, "ebay.com"):
			return "eBay US"
		}
	}

	return "eBay"
}

func ebaySellerProfileURL(item ebay.EbayItem) string {
	if item.Seller == "" {
		return ""
	}

	host := ebayItemHost(item.ItemURL)
	switch item.Marketplace {
	case "EBAY_CA":
		host = "www.ebay.ca"
	case "EBAY_US":
		host = "www.ebay.com"
	}
	if host == "" {
		host = "www.ebay.ca"
	}

	return fmt.Sprintf("https://%s/usr/%s", host, item.Seller)
}

func ebayItemHost(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Host
}

// SendMemExpressDeal sends a Memory Express clearance deal to subscribed Discord channels.
func (c *Client) SendMemExpressDeal(ctx context.Context, product memoryexpress.AnalyzedProduct, subs []models.Subscription) error {
	embed := formatMemExpressEmbed(product)
	title := product.CleanTitle
	if title == "" {
		title = product.Title
	}
	return c.sendEmbedToSubscriptions(ctx, "memoryexpress", title, embed, subs)
}

func formatMemExpressEmbed(product memoryexpress.AnalyzedProduct) discordEmbed {
	title := product.CleanTitle
	if title == "" {
		title = product.Title
	}
	if product.IsLavaHot {
		title += " 🔥"
	}

	embedColor := colorWarmDeal
	if product.IsLavaHot {
		embedColor = colorHotDeal
	}

	// Build a compact description like RFD embeds — no fields, just inline text.
	var desc strings.Builder

	// Price line: ~~$199.99~~ → **$99.99** (50% off)
	finalPrice := product.SalePrice
	if finalPrice == 0 {
		finalPrice = product.ClearancePrice
	}
	desc.WriteString(fmt.Sprintf("~~$%.2f~~ → **$%.2f**", product.RegularPrice, finalPrice))
	if product.DiscountPct > 0 {
		desc.WriteString(fmt.Sprintf(" (%.0f%% off)", product.DiscountPct))
	}
	desc.WriteString("\n\n")

	// Metadata line: 📍 Store  •  📦 3 in stock  •  Category
	desc.WriteString(fmt.Sprintf("📍 %s", product.StoreName))
	if product.Stock > 0 {
		desc.WriteString(fmt.Sprintf("  •  📦 %d in stock", product.Stock))
	}
	if product.Category != "" {
		desc.WriteString(fmt.Sprintf("  •  %s", product.Category))
	}

	var thumbnail discordEmbedThumbnail
	if product.ImageURL != "" {
		thumbnail.URL = product.ImageURL
	}

	return discordEmbed{
		Title:       title,
		URL:         product.URL,
		Description: desc.String(),
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: "Memory Express Clearance",
		},
	}
}

// SendBestBuyDeal sends a Best Buy deal notification to all eligible subscriptions.
func (c *Client) SendBestBuyDeal(ctx context.Context, product bestbuy.AnalyzedProduct, subs []models.Subscription) error {
	embed := formatBestBuyEmbed(product)
	title := product.CleanTitle
	if title == "" {
		title = product.Name
	}
	return c.sendEmbedToSubscriptions(ctx, "bestbuy", title, embed, subs)
}

func (c *Client) SendBestBuyComputeIssue(ctx context.Context, issue bestbuy.ComputeIssue, subs []models.Subscription) error {
	embed := formatBestBuyComputeIssueEmbed(issue)
	title := issue.Title
	if title == "" {
		title = "Best Buy compute eBay verification issue"
	}
	return c.sendEmbedToSubscriptions(ctx, "bestbuy_compute", title, embed, subs)
}

func formatBestBuyEmbed(product bestbuy.AnalyzedProduct) discordEmbed {
	title := product.CleanTitle
	if title == "" {
		title = product.Name
	}
	if product.IsLavaHot {
		title += " [Hot]"
	}

	embedColor := colorColdDeal
	aiLabel := "New listing"
	if product.AlertKind == bestbuy.AlertKindPriceDrop {
		aiLabel = "Price drop"
	} else if product.AlertKind == bestbuy.AlertKindComputeOutlier {
		aiLabel = "Compute outlier"
	}
	if product.IsWarm {
		embedColor = colorWarmDeal
		if product.AlertKind == bestbuy.AlertKindPriceDrop {
			aiLabel = "Warm price drop"
		} else if product.AlertKind == bestbuy.AlertKindComputeOutlier {
			aiLabel = "Warm compute outlier"
		} else {
			aiLabel = "Warm deal"
		}
	}
	if product.IsLavaHot {
		embedColor = colorHotDeal
		if product.AlertKind == bestbuy.AlertKindPriceDrop {
			aiLabel = "Lava hot price drop"
		} else if product.AlertKind == bestbuy.AlertKindComputeOutlier {
			aiLabel = "Lava hot compute outlier"
		} else {
			aiLabel = "Lava hot deal"
		}
	}

	var fields []discordEmbedField
	fields = append(fields, discordEmbedField{Name: "AI Label", Value: aiLabel, Inline: true})

	// Price field with strikethrough original
	if product.AlertKind == bestbuy.AlertKindPriceDrop {
		fields = append(fields, discordEmbedField{Name: "Price Drop", Value: formatBestBuyPriceDrop(product)})
	} else if product.SalePrice > 0 && product.SalePrice < product.RegularPrice {
		priceVal := fmt.Sprintf("~~$%.2f~~ → **$%.2f**", product.RegularPrice, product.SalePrice)
		if product.DiscountPct > 0 {
			priceVal += fmt.Sprintf(" (%.0f%% off)", product.DiscountPct)
		}
		fields = append(fields, discordEmbedField{Name: "Price", Value: priceVal})
	} else if product.RegularPrice > 0 {
		fields = append(fields, discordEmbedField{Name: "Price", Value: fmt.Sprintf("**$%.2f**", product.RegularPrice)})
	}

	// Seller
	if product.SellerName != "" {
		fields = append(fields, discordEmbedField{Name: "Seller", Value: product.SellerName, Inline: true})
	}

	// Category
	if product.CategoryName != "" {
		fields = append(fields, discordEmbedField{Name: "Category", Value: product.CategoryName, Inline: true})
	}
	if product.ComparableCount > 0 && product.ComparableMedianPrice > 0 {
		compValue := fmt.Sprintf("$%.2f median", product.ComparableMedianPrice)
		if product.ComparableP25Price > 0 && product.ComparableP25Price < product.ComparableMedianPrice {
			compValue += fmt.Sprintf(" • $%.2f p25", product.ComparableP25Price)
		}
		if product.ComparableLowestPrice > 0 && product.ComparableLowestPrice < product.ComparableMedianPrice {
			compValue += fmt.Sprintf(" • $%.2f low", product.ComparableLowestPrice)
		}
		if product.ComparableDiscountPct > 0 {
			compValue += fmt.Sprintf(" • %.0f%% below comps", product.ComparableDiscountPct)
		}
		fields = append(fields, discordEmbedField{Name: "Best Buy Comps", Value: compValue, Inline: true})
	}
	if product.SoldCompCount > 0 && product.SoldCompMedianPrice > 0 {
		soldValue := fmt.Sprintf("$%.2f sold median", product.SoldCompMedianPrice)
		if product.SoldCompGapPct > 0 {
			soldValue += fmt.Sprintf(" • %.0f%% below sold", product.SoldCompGapPct)
		}
		fields = append(fields, discordEmbedField{Name: "eBay Sold Comps", Value: soldValue, Inline: true})
	}

	var description string
	if product.Summary != "" {
		description = product.Summary
	}

	var thumbnail discordEmbedThumbnail
	if product.ImageURL != "" {
		thumbnail.URL = product.ImageURL
	}

	footerText := "Best Buy Marketplace"
	if product.AlertKind == bestbuy.AlertKindPriceDrop {
		footerText = "Best Buy Price Drop"
	} else if product.AlertKind == bestbuy.AlertKindComputeOutlier {
		footerText = "Best Buy Compute Outlier"
	} else if strings.HasPrefix(product.Source, "seller:") {
		footerText = "Best Buy New Listing"
	} else if product.Source == "openbox" || product.IsOpenBox {
		footerText = "Geek Squad Certified Open Box"
	}

	return discordEmbed{
		Title:       title,
		URL:         product.URL,
		Description: description,
		Color:       embedColor,
		Fields:      fields,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: footerText,
		},
	}
}

func formatBestBuyComputeIssueEmbed(issue bestbuy.ComputeIssue) discordEmbed {
	title := strings.TrimSpace(issue.Title)
	if title == "" {
		title = "Best Buy compute eBay verification issue"
	}
	productTitle := strings.TrimSpace(issue.Product.Name)
	if productTitle != "" {
		title = title + ": " + discordLimit(productTitle, 120)
	}

	reason := strings.TrimSpace(issue.Reason)
	if reason == "" {
		reason = "unknown"
	}
	details := strings.TrimSpace(issue.Details)
	if details == "" {
		details = strings.TrimSpace(issue.Verification.Error)
	}

	var desc strings.Builder
	desc.WriteString("The compute scanner found a would-be Best Buy alert, but it was not posted as a deal because eBay sold verification did not pass.\n\n")
	desc.WriteString("Reason: `")
	desc.WriteString(discordLimit(reason, 128))
	desc.WriteString("`")
	if details != "" {
		desc.WriteString("\n")
		desc.WriteString(discordLimit(details, 1200))
	}

	fields := []discordEmbedField{}
	price := bestBuyEffectivePrice(issue.Product)
	if price > 0 {
		fields = append(fields, discordEmbedField{Name: "Best Buy Price", Value: fmt.Sprintf("$%.2f", price), Inline: true})
	}
	if issue.Product.SellerName != "" {
		fields = append(fields, discordEmbedField{Name: "Seller", Value: discordLimit(issue.Product.SellerName, 1024), Inline: true})
	}
	if issue.Product.SKU != "" {
		fields = append(fields, discordEmbedField{Name: "SKU", Value: discordLimit(issue.Product.SKU, 1024), Inline: true})
	}
	if issue.Score.ComparableCount > 0 {
		value := fmt.Sprintf("%d comps", issue.Score.ComparableCount)
		if issue.Score.MedianPrice > 0 {
			value += fmt.Sprintf(" | $%.2f median", issue.Score.MedianPrice)
		}
		if issue.Score.GapPct > 0 {
			value += fmt.Sprintf(" | %.0f%% internal gap", issue.Score.GapPct)
		}
		fields = append(fields, discordEmbedField{Name: "Best Buy Internal Score", Value: discordLimit(value, 1024), Inline: false})
	}
	if issue.Verification.Query != "" || issue.Verification.Backend != "" || issue.Verification.Verdict != "" {
		value := []string{}
		if issue.Verification.Query != "" {
			value = append(value, "Query: `"+discordLimit(issue.Verification.Query, 240)+"`")
		}
		if issue.Verification.Backend != "" {
			value = append(value, "Backend: `"+discordLimit(issue.Verification.Backend, 80)+"`")
		}
		if issue.Verification.Verdict != "" {
			value = append(value, "Verdict: `"+discordLimit(issue.Verification.Verdict, 80)+"`")
		}
		if issue.Verification.ComparableCount > 0 {
			value = append(value, fmt.Sprintf("Matching sold comps: %d", issue.Verification.ComparableCount))
		}
		fields = append(fields, discordEmbedField{Name: "eBay Sold Check", Value: discordLimit(strings.Join(value, "\n"), 1024), Inline: false})
	}

	var thumbnail discordEmbedThumbnail
	if issue.Product.ImageURL != "" {
		thumbnail.URL = issue.Product.ImageURL
	}
	timestamp := issue.OccurredAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	return discordEmbed{
		Title:       title,
		URL:         issue.Product.URL,
		Description: desc.String(),
		Color:       colorWarmDeal,
		Timestamp:   timestamp.Format(time.RFC3339),
		Fields:      fields,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: "Best Buy Compute Verification Issue",
		},
	}
}

func formatBestBuyPriceDrop(product bestbuy.AnalyzedProduct) string {
	baseline := product.InitialEffectivePrice
	current := bestBuyEffectivePrice(product.Product)
	if baseline <= 0 {
		baseline = product.PreviousEffectivePrice
	}
	if current <= 0 {
		current = product.SalePrice
	}
	if current <= 0 {
		current = product.RegularPrice
	}

	amount := product.PriceDropAmount
	if amount <= 0 && baseline > current {
		amount = baseline - current
	}
	pct := product.PriceDropPct
	if pct <= 0 && baseline > 0 && amount > 0 {
		pct = amount / baseline * 100
	}
	if baseline > 0 && current > 0 && amount > 0 {
		return fmt.Sprintf("~~$%.2f~~ -> **$%.2f** ($%.2f / %.0f%% drop)", baseline, current, amount, pct)
	}
	if current > 0 {
		return fmt.Sprintf("**$%.2f**", current)
	}
	return "See Best Buy listing"
}

func bestBuyEffectivePrice(product bestbuy.Product) float64 {
	if product.SalePrice > 0 {
		return product.SalePrice
	}
	return product.RegularPrice
}
