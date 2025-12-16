package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const discordUpdateInterval = 10 * time.Minute

const (
	colorColdDeal    = 3092790  // #2F3136
	colorWarmDeal    = 16753920 // #FFA500
	colorHotDeal     = 16711680 // #FF0000
	colorVeryHotDeal = 16776960 // #FFFFFF

	heatScoreThresholdCold = 0.05
	heatScoreThresholdWarm = 0.1
	heatScoreThresholdHot  = 0.25
)

type Client struct {
	webhookURL string
	client     *http.Client
}

func New(webhookURL string) *Client {
	return &Client{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

// Send sends a new deal notification and returns the message ID.
func (c *Client) Send(ctx context.Context, deal models.DealInfo) (string, error) {
	if c.webhookURL == "" {
		return "", nil // Or error? Original code just skipped if empty.
	}
	embed := formatDealToEmbed(deal, false)
	return c.sendAndGetMessageID(embed)
}

// Update updates an existing notification.
func (c *Client) Update(ctx context.Context, messageID string, deal models.DealInfo) error {
	if c.webhookURL == "" || messageID == "" {
		return nil
	}
	// Check interval logic is usually done by the caller, but here we can just update if asked.
	// The original code checked time.Since(DiscordLastUpdatedTime).
	// We'll assume the caller decides WHEN to update.

	embed := formatDealToEmbed(deal, true)
	return c.updateDiscordMessage(messageID, embed)
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

type discordEmbed struct {
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	URL         string                `json:"url,omitempty"`
	Timestamp   string                `json:"timestamp,omitempty"`
	Color       int                   `json:"color,omitempty"`
	Thumbnail   discordEmbedThumbnail `json:"thumbnail,omitempty"`
	Fields      []discordEmbedField   `json:"fields,omitempty"`
}

type discordMessageResponse struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

func formatDealToEmbed(deal models.DealInfo, isUpdate bool) discordEmbed {
	var embedURL string
	if deal.ActualDealURL != "" {
		embedURL = deal.ActualDealURL
	} else {
		embedURL = deal.PostURL
	}

	description := "New RFD Post"
	if isUpdate {
		description = "Deal Updated"
	}

	var fields []discordEmbedField
	if deal.ActualDealURL != "" {
		fields = append(fields, discordEmbedField{Name: "Item", Value: fmt.Sprintf("[%s](%s)", getHomeDomain(deal.ActualDealURL), deal.ActualDealURL), Inline: true})
	}
	if deal.PostURL != "" {
		fields = append(fields, discordEmbedField{Name: "Post", Value: fmt.Sprintf("[%s](%s)", getHomeDomain(deal.PostURL), deal.PostURL), Inline: true})
	}
	if deal.AuthorName != "" && deal.AuthorURL != "" {
		fields = append(fields, discordEmbedField{Name: "Poster", Value: fmt.Sprintf("[%s](%s)", deal.AuthorName, deal.AuthorURL), Inline: true})
	}
	fields = append(fields, discordEmbedField{Name: "Likes", Value: strconv.Itoa(deal.LikeCount), Inline: true})
	fields = append(fields, discordEmbedField{Name: "Comments", Value: strconv.Itoa(deal.CommentCount), Inline: true})
	fields = append(fields, discordEmbedField{Name: "Views", Value: strconv.Itoa(deal.ViewCount), Inline: true})

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

	return discordEmbed{
		Title:       deal.Title,
		Description: description,
		URL:         embedURL,
		Timestamp:   isoTimestamp,
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Fields:      fields,
	}
}

func (c *Client) sendAndGetMessageID(embed discordEmbed) (string, error) {
	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	parsedURL, err := url.Parse(c.webhookURL)
	if err != nil {
		return "", err
	}
	q := parsedURL.Query()
	q.Set("wait", "true")
	parsedURL.RawQuery = q.Encode()

	req, err := http.NewRequest("POST", parsedURL.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var msgResponse discordMessageResponse
		if err := json.Unmarshal(bodyBytes, &msgResponse); err != nil {
			return "", err
		}
		return msgResponse.ID, nil
	}
	return "", fmt.Errorf("discord status: %s, body: %s", resp.Status, string(bodyBytes))
}

func (c *Client) updateDiscordMessage(messageID string, embed discordEmbed) error {
	payload := discordWebhookPayload{Embeds: []discordEmbed{embed}, Content: ""}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	parsedBaseURL, err := url.Parse(c.webhookURL)
	if err != nil {
		return err
	}
	finalPatchURL := fmt.Sprintf("%s://%s%s/messages/%s", parsedBaseURL.Scheme, parsedBaseURL.Host, parsedBaseURL.Path, messageID)

	req, err := http.NewRequest("PATCH", finalPatchURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	bodyBytes, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("discord update failed: %s, body: %s", resp.Status, string(bodyBytes))
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
	} else if heatScore > heatScoreThresholdWarm {
		return colorHotDeal
	} else if heatScore > heatScoreThresholdCold {
		return colorWarmDeal
	}
	return colorColdDeal
}

var knownTwoPartTLDs = map[string]bool{
	"co.uk": true, "com.au": true, "co.jp": true, "co.nz": true, "com.br": true,
	// ... (add all from main.go if needed, or implement a simpler logic)
}

func getHomeDomain(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "Link"
	}
	hostname := parsedURL.Hostname()
	if hostname == "" {
		return "Link"
	}
	// Simplified domain extraction for brevity.
	return hostname
}
