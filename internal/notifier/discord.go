package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
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
	embedColor := colorColdDeal

	if deal.HasBeenHot || deal.IsLavaHot {
		embedColor = colorHotDeal
	} else if deal.HasBeenWarm || deal.IsWarm {
		embedColor = colorWarmDeal
	}

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
	descriptionBuilder.WriteString(fmt.Sprintf("%s %d  💬 %d  👀 %d", likeIcon, likes, comments, views))

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

// doRequest handles the shared retry/rate-limit/backoff loop for Discord API calls.
// It returns the response body on success.
func (c *Client) doRequest(ctx context.Context, method, targetURL string, payload discordWebhookPayload) ([]byte, error) {
	start := time.Now()
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

// IsWarm determines if a deal is considered warm.
func (c *Client) IsWarm(deal models.DealInfo) bool {
	return deal.IsWarm
}

// IsHot determines if a deal is considered hot.
func (c *Client) IsHot(deal models.DealInfo) bool {
	return deal.IsLavaHot
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

// --- eBay Deal Notifications ---

// SendEbayDeal sends a new eBay deal notification to all subscribed channels.
// Returns a map of ChannelID -> MessageID.
func (c *Client) SendEbayDeal(ctx context.Context, item ebay.EbayItem, subs []models.Subscription) (map[string]string, error) {
	if c.botToken == "" {
		return nil, nil
	}

	payload := createEbayPayload(item)
	results := make(map[string]string)

	for _, sub := range subs {
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
	// Title
	title := item.Title
	if item.CleanTitle != "" {
		title = item.CleanTitle
	}

	if item.IsLavaHot {
		title += " 🔥"
	}

	// Color
	embedColor := colorWarmDeal // Only warm/hot items get stored, so minimum is warm
	if item.IsLavaHot {
		embedColor = colorHotDeal
	}

	// Description
	var descBuilder strings.Builder

	// Seller info
	if item.Seller != "" {
		descBuilder.WriteString(fmt.Sprintf("**Seller:** [%s](https://www.ebay.ca/usr/%s)\n", item.Seller, item.Seller))
	}

	// Condition
	if item.Condition != "" {
		descBuilder.WriteString(fmt.Sprintf("**Condition:** %s\n", item.Condition))
	}

	// Price
	if item.Price != "" {
		currency := item.Currency
		if currency == "" {
			currency = "CAD"
		}
		descBuilder.WriteString(fmt.Sprintf("\n💰 **%s %s**", currency, item.Price))
	}

	// Thumbnail
	var thumbnail discordEmbedThumbnail
	if item.ImageURL != "" {
		thumbnail.URL = item.ImageURL
	}

	return discordEmbed{
		Title:       title,
		URL:         item.ItemURL,
		Description: descBuilder.String(),
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: "🛒 eBay Canada",
		},
	}
}

// SendMemExpressDeal sends a Memory Express clearance deal to subscribed Discord channels.
func (c *Client) SendMemExpressDeal(ctx context.Context, product memoryexpress.AnalyzedProduct, subs []models.Subscription) error {
	if c.botToken == "" {
		return nil
	}

	embed := formatMemExpressEmbed(product)
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
			slog.Error("Failed to send Memory Express deal to channel",
				"processor", "memoryexpress",
				"channel", sub.ChannelID,
				"title", product.CleanTitle,
				"error", err,
			)
		} else {
			slog.Info("Memory Express deal sent",
				"processor", "memoryexpress",
				"channel", sub.ChannelID,
				"title", product.CleanTitle,
			)
		}
	}

	return nil
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

	var fields []discordEmbedField

	// Price field with strikethrough original
	finalPrice := product.SalePrice
	if finalPrice == 0 {
		finalPrice = product.ClearancePrice
	}
	priceVal := fmt.Sprintf("~~$%.2f~~ → **$%.2f**", product.RegularPrice, finalPrice)
	if product.DiscountPct > 0 {
		priceVal += fmt.Sprintf(" (%.0f%% off)", product.DiscountPct)
	}
	fields = append(fields, discordEmbedField{Name: "Price", Value: priceVal})

	// Category
	if product.Category != "" {
		fields = append(fields, discordEmbedField{Name: "Category", Value: product.Category, Inline: true})
	}

	// Store
	fields = append(fields, discordEmbedField{Name: "Store", Value: product.StoreName, Inline: true})

	// Stock
	if product.Stock > 0 {
		fields = append(fields, discordEmbedField{Name: "Stock", Value: fmt.Sprintf("%d", product.Stock), Inline: true})
	}

	var description string
	if product.Summary != "" {
		description = product.Summary
	}

	var thumbnail discordEmbedThumbnail
	if product.ImageURL != "" {
		thumbnail.URL = product.ImageURL
	}

	return discordEmbed{
		Title:       title,
		URL:         product.URL,
		Description: description,
		Color:       embedColor,
		Fields:      fields,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: "Memory Express Clearance • In-store pickup only",
		},
	}
}

// SendBestBuyDeal sends a Best Buy deal notification to all eligible subscriptions.
func (c *Client) SendBestBuyDeal(ctx context.Context, product bestbuy.AnalyzedProduct, subs []models.Subscription) error {
	if c.botToken == "" {
		return nil
	}

	embed := formatBestBuyEmbed(product)
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
			slog.Error("Failed to send Best Buy deal to channel",
				"processor", "bestbuy",
				"channel", sub.ChannelID,
				"title", product.CleanTitle,
				"error", err,
			)
		} else {
			slog.Info("Best Buy deal sent",
				"processor", "bestbuy",
				"channel", sub.ChannelID,
				"title", product.CleanTitle,
			)
		}
	}

	return nil
}

func formatBestBuyEmbed(product bestbuy.AnalyzedProduct) discordEmbed {
	title := product.CleanTitle
	if title == "" {
		title = product.Name
	}
	if product.IsLavaHot {
		title += " 🔥"
	}

	embedColor := colorWarmDeal
	if product.IsLavaHot {
		embedColor = colorHotDeal
	}

	var fields []discordEmbedField

	// Price field with strikethrough original
	if product.SalePrice > 0 && product.SalePrice < product.RegularPrice {
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

	var description string
	if product.Summary != "" {
		description = product.Summary
	}

	var thumbnail discordEmbedThumbnail
	if product.ImageURL != "" {
		thumbnail.URL = product.ImageURL
	}

	footerText := "Best Buy Marketplace"
	if product.Source == "openbox" || product.IsOpenBox {
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
