package hardwareswap

import (
	"context"
	"log/slog"
	"strings"
)

// HandleComponent routes HardwareSwap button/select interactions.
// Returns a JSON-serializable response map.
func HandleComponent(ctx context.Context, store *Store, aiClient interface{}, discordToken, customID, guildID, userID string, messageEmbeds []interface{}) map[string]interface{} {
	parts := strings.Split(customID, "|")
	action := parts[0]

	switch action {
	case "hw_wizard_ai":
		return showAIWizardModal()

	case "hw_wizard_manual":
		return showManualModal("")

	case "hw_confirm_alert":
		flow := "wizard"
		if len(parts) > 2 && parts[2] == "Manual" {
			flow = "manual"
		}
		_ = store.SaveAnalytics(ctx, AnalyticsRecord{
			FlowType: flow,
			Outcome:  "Accepted_" + flow,
		})
		return updateMessage("Alert Saved Successfully!")

	case "hw_cancel_alert":
		if len(parts) > 1 {
			_ = store.DeleteAlert(ctx, parts[1])
		}
		flow := "wizard"
		if len(parts) > 2 && parts[2] == "Manual" {
			flow = "manual"
		}
		_ = store.SaveAnalytics(ctx, AnalyticsRecord{
			FlowType: flow,
			Outcome:  "Cancelled_" + flow,
		})
		return updateMessage("Alert Cancelled.")

	case "hw_cancel_alert_creation":
		_ = store.SaveAnalytics(ctx, AnalyticsRecord{
			FlowType: "manual",
			Outcome:  "Cancelled_Manual_Syntax_Error",
		})
		return updateMessage("Alert Creation Cancelled.")

	case "hw_edit_alert":
		editCount := "1"
		if len(parts) > 2 {
			editCount = parts[2]
		}
		return showManualModal(editCount)

	case "hw_delete_alert":
		if len(parts) > 1 {
			if err := store.DeleteAlert(ctx, parts[1]); err != nil {
				slog.Error("Failed to delete alert", "processor", "hardwareswap", "error", err)
			}
		}
		return map[string]interface{}{
			"type": 7,
			"data": map[string]interface{}{
				"content":    "Alert removed.",
				"embeds":     messageEmbeds,
				"components": []interface{}{},
			},
		}

	case "hw_delete_all_alerts":
		if err := store.DeleteAllUserAlerts(ctx, guildID, userID); err != nil {
			slog.Error("Failed to delete all alerts", "processor", "hardwareswap", "error", err)
		}
		return updateMessage("All your alerts on this server have been deleted.")

	default:
		return nil
	}
}

func showAIWizardModal() map[string]interface{} {
	return map[string]interface{}{
		"type": 9, // Modal
		"data": map[string]interface{}{
			"custom_id": "hw_modal_wizard_ai",
			"title":     "Setup a Hardware Alert",
			"components": []interface{}{
				map[string]interface{}{
					"type": 1,
					"components": []interface{}{
						map[string]interface{}{
							"type": 4, "custom_id": "text_query",
							"label": "What are you looking for?", "style": 2,
							"placeholder": "e.g. A used 3080 series GPU in Toronto under $500",
							"required": true, "max_length": 300,
						},
					},
				},
			},
		},
	}
}

func showManualModal(editCount string) map[string]interface{} {
	customID := "hw_modal_wizard_manual"
	if editCount != "" {
		customID += "|" + editCount
	}
	return map[string]interface{}{
		"type": 9,
		"data": map[string]interface{}{
			"custom_id": customID,
			"title":     "Manual Alert Entry",
			"components": []interface{}{
				map[string]interface{}{
					"type": 1,
					"components": []interface{}{
						map[string]interface{}{
							"type": 4, "custom_id": "text_title",
							"label": "Name your alert (e.g., Cheap 4090)", "style": 1,
							"required": true, "max_length": 50,
						},
					},
				},
				map[string]interface{}{
					"type": 1,
					"components": []interface{}{
						map[string]interface{}{
							"type": 4, "custom_id": "text_query",
							"label": "Query Syntax", "style": 2,
							"placeholder": "(rtx AND 4090) NOT (broken)",
							"required": true, "max_length": 150,
						},
					},
				},
			},
		},
	}
}

func updateMessage(content string) map[string]interface{} {
	return map[string]interface{}{
		"type": 7,
		"data": map[string]interface{}{
			"content":    content,
			"embeds":     []interface{}{},
			"components": []interface{}{},
		},
	}
}

