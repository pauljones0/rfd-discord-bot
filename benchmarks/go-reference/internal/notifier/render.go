package notifier

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/pauljones0/rfd-discord-bot/internal/dealquality"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const (
	colorColdDeal = 2829617
	colorWarmDeal = 16098851
	colorHotDeal  = 16723320
)

type discordMessagePayload struct {
	Content         string                  `json:"content"`
	Embeds          []discordEmbed          `json:"embeds"`
	AllowedMentions *discordAllowedMentions `json:"allowed_mentions,omitempty"`
	Nonce           string                  `json:"nonce,omitempty"`
	EnforceNonce    bool                    `json:"enforce_nonce,omitempty"`
}

type discordAllowedMentions struct {
	Parse []string `json:"parse"`
}

type discordEmbedThumbnail struct {
	URL string `json:"url,omitempty"`
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
	Footer      discordEmbedFooter    `json:"footer,omitempty"`
}

type discordMessageResponse struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
}

func createDiscordPayload(deal models.DealInfo) discordMessagePayload {
	embed := formatDealToEmbed(deal)
	return discordMessagePayload{
		Content:         "", // clear any hidden message text
		Embeds:          []discordEmbed{embed},
		AllowedMentions: &discordAllowedMentions{Parse: []string{}},
	}
}

func formatDealToEmbed(deal models.DealInfo) discordEmbed {
	title := deal.Title
	if deal.CleanTitle != "" {
		title = deal.CleanTitle
	}

	titleURL := preferredDealURL(deal)

	discountBacked := dealquality.RFDWarmHotEligible(deal)
	if discountBacked && deal.HasBeenHot {
		title += " 🔥"
	}

	likes, comments, views, hasViews := deal.EngagementStats()
	liveWarm := discountBacked && dealquality.IsWarm(deal)
	liveHot := discountBacked && dealquality.IsHot(deal)
	embedColor := colorColdDeal

	if (discountBacked && deal.HasBeenHot) || liveHot {
		embedColor = colorHotDeal
	} else if (discountBacked && deal.HasBeenWarm) || liveWarm {
		embedColor = colorWarmDeal
	}

	var descriptionBuilder strings.Builder

	// Because processor.sortThreads() orders these by LikeCount desc, the links
	// here naturally print in order of most popular to least popular.
	for _, thread := range deal.Threads {
		descriptionBuilder.WriteString(fmt.Sprintf("[RFD](%s) ", thread.PostURL))
	}
	descriptionBuilder.WriteString("\n\n")

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
		Title:       boundedTitle(title),
		URL:         titleURL,
		Description: descriptionBuilder.String(),
		Timestamp:   timestampStr,
		Color:       embedColor,
		Thumbnail:   thumbnail,
		Footer: discordEmbedFooter{
			Text: footerText,
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

func formatEngagementLine(likeIcon string, likes, comments, views int, hasViews bool) string {
	if hasViews {
		return fmt.Sprintf("%s %d  💬 %d  👀 %d", likeIcon, likes, comments, views)
	}
	return fmt.Sprintf("%s %d  💬 %d", likeIcon, likes, comments)
}

// Discord limits embed titles to 256 characters. Count UTF-16 units
// conservatively, and truncate only at Unicode boundaries in the rendered copy.
func boundedTitle(title string) string {
	if len(utf16.Encode([]rune(title))) <= 256 {
		return title
	}
	remaining := 255 // Reserve one unit for an ellipsis.
	for offset, r := range title {
		units := 1
		if r > 0xffff {
			units = 2
		}
		if units > remaining {
			return title[:offset] + "…"
		}
		remaining -= units
	}
	return title
}
