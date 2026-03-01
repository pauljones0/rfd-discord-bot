package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"

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

	fmt.Println("Registering /rfd-bot-setup global command...")

	url := fmt.Sprintf("https://discord.com/api/v10/applications/%s/commands", cfg.DiscordAppID)

	// Command definition
	// Restrict to admins: Manage Server permission is 0x20
	payload := map[string]interface{}{
		"name":                       "rfd-bot-setup",
		"description":                "Configure the RFD bot for this server.",
		"default_member_permissions": "32", // 0x20 Manage Server
		"options": []map[string]interface{}{
			{
				"name":        "set",
				"description": "Set the channel for the bot to publish deals to.",
				"type":        1, // SUB_COMMAND
				"options": []map[string]interface{}{
					{
						"name":          "channel",
						"description":   "The channel to publish deals to.",
						"type":          7,           // CHANNEL
						"channel_types": []int{0, 5}, // GUILD_TEXT, GUILD_ANNOUNCEMENT
						"required":      true,
					},
				},
			},
			{
				"name":        "remove",
				"description": "Remove the bot subscription from this server.",
				"type":        1, // SUB_COMMAND
			},
		},
	}

	payloadBytes, err := json.Marshal([]interface{}{payload})
	if err != nil {
		log.Fatalf("Failed to marshal payload: %v", err)
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		log.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+cfg.DiscordBotToken)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		fmt.Println("Successfully registered command.")
	} else {
		log.Fatalf("Failed to register command: HTTP %d\nBody: %s", resp.StatusCode, string(body))
	}
}
