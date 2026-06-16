package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
)

func stringChoices(choices []dealtypes.Choice) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(choices))
	for _, choice := range choices {
		out = append(out, map[string]interface{}{
			"name":  choice.Name,
			"value": choice.Value,
		})
	}
	return out
}

func main() {
	var guildIDsRaw string
	flag.StringVar(&guildIDsRaw, "guild-ids", os.Getenv("DISCORD_GUILD_IDS"), "comma-separated Discord guild IDs to register commands in immediately; empty registers global commands")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	if cfg.DiscordAppID == "" || cfg.DiscordBotToken == "" {
		log.Fatalf("DISCORD_APP_ID and DISCORD_BOT_TOKEN must be set")
	}

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
							"choices":     stringChoices(dealtypes.RFDChoices),
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
							"choices":     stringChoices(dealtypes.EbayChoices),
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
							"choices":     stringChoices(dealtypes.MemoryExpressChoices),
						},
					},
				},
				// setup-bestbuy subcommand
				{
					"name":        "setup-bestbuy",
					"description": "Subscribe this channel to Best Buy seller alerts, price drops, or compute outliers.",
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
							"description": "Which Best Buy alerts to publish.",
							"type":        3, // STRING
							"required":    true,
							"choices":     stringChoices(dealtypes.BestBuyChoices),
						},
					},
				},
				// setup-core subcommand
				{
					"name":        "setup-core",
					"description": "Subscribe this channel to Core lowest or p25 price deal alerts.",
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
							"description": "Which Core alerts to publish.",
							"type":        3, // STRING
							"required":    true,
							"choices":     stringChoices(dealtypes.CoreChoices),
						},
					},
				},
				// setup-oneverycorner subcommand
				{
					"name":        "setup-oneverycorner",
					"description": "Subscribe this channel to Bet365 corner and possible corner-goal alerts.",
					"type":        1, // SUB_COMMAND
					"options": []map[string]interface{}{
						{
							"name":          "channel",
							"description":   "The channel to publish alerts to.",
							"type":          7,           // CHANNEL
							"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
							"required":      true,
						},
						{
							"name":        "filter",
							"description": "Which OnEveryCorner alerts to publish.",
							"type":        3, // STRING
							"required":    true,
							"choices":     stringChoices(dealtypes.OnEveryCornerChoices),
						},
					},
				},
				// suggest-rules subcommand
				{
					"name":        "suggest-rules",
					"description": "Have Gemini analyze recent notifications and suggest product segmentation regexes.",
					"type":        1, // SUB_COMMAND
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
							"choices":     stringChoices(dealtypes.ActiveRemoveChoices(cfg.FacebookEnabled, cfg.HardwareSwapEnabled)),
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

	if !cfg.FacebookEnabled {
		if options, ok := payload[0]["options"].([]map[string]interface{}); ok {
			filtered := options[:0]
			for _, option := range options {
				if option["name"] == "setup-facebook" {
					continue
				}
				filtered = append(filtered, option)
			}
			payload[0]["options"] = filtered
		}
	}

	// HardwareSwap commands (top-level, not subcommands of /deals)
	if cfg.HardwareSwapEnabled {
		payload = append(payload, map[string]interface{}{
			"name":                       "hw-setup",
			"description":                "Configure HardwareSwap bot for this server (Admin Only).",
			"default_member_permissions": "32",
			"options": []map[string]interface{}{
				{
					"name":          "feed_channel",
					"description":   "Channel where deals will be posted.",
					"type":          7,
					"channel_types": []int{0, 5},
					"required":      true,
				},
				{
					"name":          "ping_channel",
					"description":   "Channel where users will be pinged for alert matches.",
					"type":          7,
					"channel_types": []int{0, 5},
					"required":      true,
				},
			},
		})
		payload = append(payload, map[string]interface{}{
			"name":        "hw-help",
			"description": "Learn how to use the HardwareSwap alert bot.",
		})
		payload = append(payload, map[string]interface{}{
			"name":        "hw-alert",
			"description": "Manage your HardwareSwap alerts.",
			"options": []map[string]interface{}{
				{
					"name":        "add",
					"description": "Add a new hardware alert.",
					"type":        1,
				},
				{
					"name":        "list",
					"description": "List and manage your active alerts.",
					"type":        1,
				},
			},
		})
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		log.Fatalf("Failed to marshal payload: %v", err)
	}

	for label, url := range registrationURLs(cfg.DiscordAppID, guildIDsRaw) {
		fmt.Printf("Registering Discord commands for %s...\n", label)
		if err := register(url, payloadBytes, cfg.DiscordBotToken); err != nil {
			log.Fatalf("Failed to register commands for %s: %v", label, err)
		}
		fmt.Printf("Successfully registered commands for %s.\n", label)
	}
}

func registrationURLs(appID, guildIDsRaw string) map[string]string {
	guildIDs := csv(guildIDsRaw)
	if len(guildIDs) == 0 {
		return map[string]string{
			"global": fmt.Sprintf("https://discord.com/api/v10/applications/%s/commands", appID),
		}
	}

	urls := make(map[string]string, len(guildIDs))
	for _, guildID := range guildIDs {
		urls["guild "+guildID] = fmt.Sprintf("https://discord.com/api/v10/applications/%s/guilds/%s/commands", appID, guildID)
	}
	return urls
}

func csv(raw string) []string {
	var out []string
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func register(url string, payloadBytes []byte, token string) error {
	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+token)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
