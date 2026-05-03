package hardwareswap

import (
	"fmt"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/reddit"
)

// CleanedPost is the structured AI response when parsing a Reddit post.
type CleanedPost struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Price       string `json:"price,omitempty"`
	Location    string `json:"location,omitempty"`
	Condition   string `json:"condition,omitempty"`
}

// BuildDealEmbed creates a Discord embed for a hardware swap deal.
func BuildDealEmbed(post reddit.Post, cleaned *CleanedPost) map[string]interface{} {
	fields := []map[string]interface{}{}

	if cleaned.Price != "" {
		fields = append(fields, map[string]interface{}{
			"name": "Price", "value": cleaned.Price, "inline": true,
		})
	}
	if cleaned.Condition != "" {
		fields = append(fields, map[string]interface{}{
			"name": "Condition", "value": cleaned.Condition, "inline": true,
		})
	}
	if cleaned.Location != "" {
		fields = append(fields, map[string]interface{}{
			"name": "Location", "value": cleaned.Location, "inline": true,
		})
	}

	embed := map[string]interface{}{
		"title":       cleaned.Title,
		"url":         "https://www.reddit.com" + post.Permalink,
		"description": cleaned.Description,
		"color":       getDealColor(post.Score, post.NumComments),
		"fields":      fields,
		"footer": map[string]interface{}{
			"text": fmt.Sprintf("r/%s | Score %d | %d comments", post.Subreddit, post.Score, post.NumComments),
		},
		"timestamp": time.Unix(int64(post.CreatedUtc), 0).Format(time.RFC3339),
	}

	if post.Thumbnail != "" && post.Thumbnail != "self" && post.Thumbnail != "default" {
		embed["thumbnail"] = map[string]interface{}{"url": post.Thumbnail}
	}

	return embed
}

// BuildClosedEmbed creates a greyed-out embed for sold/closed listings.
func BuildClosedEmbed(originalTitle, url, status string) map[string]interface{} {
	return map[string]interface{}{
		"title":       "~~" + originalTitle + "~~",
		"url":         url,
		"description": fmt.Sprintf("This listing has been marked as **%s** on Reddit.", status),
		"color":       0x2C2F33,
		"footer": map[string]interface{}{
			"text": "Deal Closed",
		},
	}
}

// BuildDealButtons creates the Open in Reddit button as a raw JSON component.
func BuildDealButtons(permalink string) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"type": 1, // ActionRow
			"components": []interface{}{
				map[string]interface{}{
					"type":  2, // Button
					"style": 5, // Link
					"label": "Open in Reddit",
					"url":   "https://www.reddit.com" + permalink,
				},
			},
		},
	}
}

func getDealColor(score, comments int) int {
	interactions := score + comments
	switch {
	case interactions >= 16:
		return 0xFF0000 // Red (hot)
	case interactions >= 6:
		return 0xFFA500 // Orange (warm)
	case interactions >= 3:
		return 0xFFFF00 // Yellow
	default:
		return 0x808080 // Grey
	}
}
