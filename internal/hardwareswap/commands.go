package hardwareswap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
)

// HandleCommand routes HardwareSwap slash commands.
// Returns a JSON-serializable response map, or nil if the command is not recognized.
func HandleCommand(ctx context.Context, w http.ResponseWriter, store *Store, commandName string, options []interface{}, guildID, userID string) map[string]interface{} {
	switch commandName {
	case "hw-setup":
		return handleSetup(ctx, store, options, guildID)
	case "hw-help":
		return handleHelp()
	case "hw-alert":
		return handleAlertGroup(ctx, w, store, options, guildID, userID)
	default:
		return nil
	}
}

func handleSetup(ctx context.Context, store *Store, options []interface{}, guildID string) map[string]interface{} {
	var feedChannelID, pingChannelID string
	for _, opt := range options {
		optMap, ok := opt.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := optMap["name"].(string)
		value, _ := optMap["value"].(string)
		switch name {
		case "feed_channel":
			feedChannelID = value
		case "ping_channel":
			pingChannelID = value
		}
	}

	if feedChannelID == "" || pingChannelID == "" {
		return ephemeralMessage("Both feed_channel and ping_channel are required.")
	}

	cfg := ServerConfig{
		FeedChannelID: feedChannelID,
		PingChannelID: pingChannelID,
	}
	if err := store.SaveServerConfig(ctx, guildID, cfg); err != nil {
		slog.Error("Failed to save HW server config", "processor", "hardwareswap", "error", err)
		return ephemeralMessage("Failed to save configuration.")
	}

	return ephemeralMessage(fmt.Sprintf(
		"Hardware Swap Bot configured!\n\nDeals posted to <#%s>.\nAlerts ping in <#%s>.\n\nUsers can now run `/hw-alert add` to get started!",
		feedChannelID, pingChannelID))
}

func handleHelp() map[string]interface{} {
	return map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			"flags": 64,
			"embeds": []interface{}{
				map[string]interface{}{
					"title":       "Hardware Swap Bot Help",
					"description": "Tracks r/CanadianHardwareSwap in real-time and pings you when matching deals appear.",
					"color":       0x00FF00,
					"fields": []interface{}{
						map[string]interface{}{
							"name":  "AI-Powered Alerts",
							"value": "Run `/hw-alert add` and select **'Help Me Write It'**. Describe what you want (e.g., *\"A 30-series GPU in Vancouver under $400\"*) and the AI handles the logic.",
						},
						map[string]interface{}{
							"name":  "Manual Querying",
							"value": "Select **'I'll Type It Myself'** to use Boolean logic like `(rtx AND 4090) NOT broken`.",
						},
						map[string]interface{}{
							"name":  "Management",
							"value": "Use `/hw-alert list` to view or delete your current subscriptions.",
						},
					},
				},
			},
		},
	}
}

func handleAlertGroup(ctx context.Context, w http.ResponseWriter, store *Store, options []interface{}, guildID, userID string) map[string]interface{} {
	if len(options) == 0 {
		return ephemeralMessage("No subcommand provided.")
	}

	firstOpt, ok := options[0].(map[string]interface{})
	if !ok {
		return ephemeralMessage("Invalid options.")
	}
	subCommand, _ := firstOpt["name"].(string)

	switch subCommand {
	case "add":
		return handleAlertAddStart()
	case "list":
		return handleAlertList(ctx, store, guildID, userID)
	default:
		return ephemeralMessage("Unknown subcommand.")
	}
}

func handleAlertAddStart() map[string]interface{} {
	return map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			"flags": 64,
			"embeds": []interface{}{
				map[string]interface{}{
					"title":       "Create a New Alert",
					"description": "How would you like to set up your alert?\n\n**Help Me Write It**: Describe what you're looking for in plain English, and the AI generates the match query.\n\n**I'll Type It Myself**: Type keywords directly (e.g., `rtx AND 4090`).",
					"color":       0x00B0F4,
				},
			},
			"components": []interface{}{
				map[string]interface{}{
					"type": 1,
					"components": []interface{}{
						map[string]interface{}{
							"type": 2, "style": 1,
							"label": "Help Me Write It", "custom_id": "hw_wizard_ai",
						},
						map[string]interface{}{
							"type": 2, "style": 2,
							"label": "I'll Type It Myself", "custom_id": "hw_wizard_manual",
						},
					},
				},
			},
		},
	}
}

func handleAlertList(ctx context.Context, store *Store, guildID, userID string) map[string]interface{} {
	alerts, err := store.GetUserAlerts(ctx, guildID, userID)
	if err != nil {
		slog.Error("Error fetching user alerts", "processor", "hardwareswap", "error", err)
		return ephemeralMessage("Failed to load alerts.")
	}

	if len(alerts) == 0 {
		return ephemeralMessage("You don't have any active alerts for this server.")
	}

	desc := ""
	var rows []interface{}
	for idx, a := range alerts {
		if idx >= 4 {
			desc += "\n*...and more.*"
			break
		}
		desc += fmt.Sprintf("**Alert #%d:** \"%s\"\n", idx+1, a.RawQuery)
		rows = append(rows, map[string]interface{}{
			"type": 1,
			"components": []interface{}{
				map[string]interface{}{
					"type": 2, "style": 2,
					"label":     fmt.Sprintf("Delete #%d", idx+1),
					"custom_id": "hw_delete_alert|" + a.ID,
				},
			},
		})
	}

	rows = append(rows, map[string]interface{}{
		"type": 1,
		"components": []interface{}{
			map[string]interface{}{
				"type": 2, "style": 4,
				"label": "Delete All", "custom_id": "hw_delete_all_alerts|",
			},
		},
	})

	return map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			"flags": 64,
			"embeds": []interface{}{
				map[string]interface{}{
					"title":       "Your Active Alerts",
					"description": desc,
					"color":       0x00B0F4,
				},
			},
			"components": rows,
		},
	}
}

func ephemeralMessage(content string) map[string]interface{} {
	return map[string]interface{}{
		"type": 4,
		"data": map[string]interface{}{
			"content": content,
			"flags":   64,
		},
	}
}
