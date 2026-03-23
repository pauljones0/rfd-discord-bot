package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.DiscordAppID == "" || cfg.DiscordBotToken == "" {
		log.Fatalf("DISCORD_APP_ID and DISCORD_BOT_TOKEN must be set")
	}

	fmt.Println("Registering /deals global command...")

	url := fmt.Sprintf("https://discord.com/api/v10/applications/%s/commands", cfg.DiscordAppID)

	// Command definition
	// Restrict to admins: Manage Server permission is 0x20
	payload := []map[string]interface{}{
		{
			"name":                       "deals",
			"description":                "Manage deal notifications for this server.",
			"default_member_permissions": "32", // 0x20 Manage Server
			"options": []map[string]interface{}{
				// setup-rfd subcommand
				{
					"name":        "setup-rfd",
					"description": "Subscribe this channel to RFD deal notifications.",
					"type":        1, // SUB_COMMAND
					"options": []map[string]interface{}{
						{
							"name":          "channel",
							"description":   "The channel to publish deals to.",
							"type":          7,           // CHANNEL
							"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
							"required":      true,
						},
						{
							"name":        "filter",
							"description": "The type of RFD deals to publish.",
							"type":        3, // STRING
							"required":    true,
							"choices": []map[string]interface{}{
								{"name": "All deals", "value": "rfd_all"},
								{"name": "Tech only", "value": "rfd_tech"},
								{"name": "Warm + Hot (all)", "value": "rfd_warm_hot"},
								{"name": "Warm + Hot (tech)", "value": "rfd_warm_hot_tech"},
								{"name": "Hot only (all)", "value": "rfd_hot"},
								{"name": "Hot only (tech)", "value": "rfd_hot_tech"},
							},
						},
					},
				},
				// setup-ebay subcommand
				{
					"name":        "setup-ebay",
					"description": "Subscribe this channel to eBay deal notifications.",
					"type":        1, // SUB_COMMAND
					"options": []map[string]interface{}{
						{
							"name":          "channel",
							"description":   "The channel to publish deals to.",
							"type":          7,           // CHANNEL
							"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
							"required":      true,
						},
						{
							"name":        "filter",
							"description": "The type of eBay deals to publish.",
							"type":        3, // STRING
							"required":    true,
							"choices": []map[string]interface{}{
								{"name": "Warm + Hot deals", "value": "ebay_warm_hot"},
								{"name": "Hot deals only", "value": "ebay_hot"},
							},
						},
					},
				},
				// setup-facebook subcommand
				{
					"name":        "setup-facebook",
					"description": "Subscribe this channel to Facebook Marketplace car deal notifications.",
					"type":        1, // SUB_COMMAND
					"options": []map[string]interface{}{
						{
							"name":          "channel",
							"description":   "The channel to publish deals to.",
							"type":          7,           // CHANNEL
							"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
							"required":      true,
						},
						{
							"name":         "city",
							"description":  "The Canadian city to search (e.g. Toronto, Vancouver).",
							"type":         3, // STRING
							"required":     true,
							"autocomplete": true,
						},
						{
							"name":        "radius",
							"description": "Search radius in km (default: 500).",
							"type":        4, // INTEGER
							"required":    false,
						},
						{
							"name":        "brands",
							"description": "Comma-separated brand filter (e.g. honda,toyota).",
							"type":        3, // STRING
							"required":    false,
						},
					},
				},
				// setup-memoryexpress subcommand
			{
				"name":        "setup-memoryexpress",
				"description": "Subscribe this channel to Memory Express clearance deal notifications.",
				"type":        1, // SUB_COMMAND
				"options": []map[string]interface{}{
					{
						"name":          "channel",
						"description":   "The channel to publish deals to.",
						"type":          7,           // CHANNEL
						"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
						"required":      true,
					},
					{
						"name":         "store",
						"description":  "The Memory Express store location (e.g. Saskatoon North).",
						"type":         3, // STRING
						"required":     true,
						"autocomplete": true,
					},
					{
						"name":        "filter",
						"description": "The type of clearance deals to publish.",
						"type":        3, // STRING
						"required":    true,
						"choices": []map[string]interface{}{
							{"name": "Warm + Hot deals", "value": "me_warm_hot"},
							{"name": "Hot deals only", "value": "me_hot"},
						},
					},
				},
			},
			// remove subcommand
				{
					"name":        "remove",
					"description": "Remove a deal subscription from this server.",
					"type":        1, // SUB_COMMAND
					"options": []map[string]interface{}{
						{
							"name":        "type",
							"description": "The subscription type to remove.",
							"type":        3, // STRING
							"required":    true,
							"choices": []map[string]interface{}{
								{"name": "RFD", "value": "rfd"},
								{"name": "eBay", "value": "ebay"},
								{"name": "Facebook", "value": "facebook"},
								{"name": "Memory Express", "value": "memoryexpress"},
							},
						},
					},
				},
				// list subcommand
				{
					"name":        "list",
					"description": "Show all active deal subscriptions for this server.",
					"type":        1, // SUB_COMMAND
				},
			},
		},
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("Failed to marshal payload: %v", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+cfg.DiscordBotToken)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("Failed to read response body: %v", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("Successfully registered command.")
	} else {
		log.Fatalf("Failed to register command: HTTP %d\nBody: %s", resp.StatusCode, string(body))
	}
}
