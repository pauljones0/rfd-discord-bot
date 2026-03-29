package hardwareswap

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"google.golang.org/genai"
)

// KeywordWizardResponse is the structured response for Boolean query compilation.
type KeywordWizardResponse struct {
	MustHave         []string `json:"must_have"`
	AnyOf            []string `json:"any_of"`
	MustNot          []string `json:"must_not"`
	TooBroad         bool     `json:"too_broad"`
	BroadReason      string   `json:"broad_reason,omitempty"`
	BroadSuggestions []string `json:"broad_suggestions,omitempty"`
	IsValid          bool     `json:"is_valid"`
	ErrorMessage     string   `json:"error_message,omitempty"`
}

// HandleModalSubmit handles the deferred response for modal submissions.
// Returns a deferred acknowledgement immediately, then processes asynchronously.
// The caller must write the deferred response to w before calling this.
func HandleModalSubmit(store *Store, aiClient *ai.Client, discordToken string, modalCustomID string, components []interface{}, appID, interactionToken, guildID, userID string) {
	if modalCustomID == "hw_modal_wizard_ai" {
		rawQuery := extractTextInputValue(components, 0, 0)
		sanitizedQuery := Sanitize(rawQuery)
		go processAIWizard(context.Background(), store, aiClient, discordToken, sanitizedQuery, appID, interactionToken, guildID, userID)
	} else if strings.HasPrefix(modalCustomID, "hw_modal_wizard_manual") {
		editCount := 0
		parts := strings.Split(modalCustomID, "|")
		if len(parts) > 1 {
			fmt.Sscanf(parts[1], "%d", &editCount)
		}
		title := extractTextInputValue(components, 0, 0)
		query := extractTextInputValue(components, 1, 0)
		sanitizedTitle := Sanitize(title)
		sanitizedQuery := Sanitize(query)
		go processManualWizard(context.Background(), store, aiClient, discordToken, sanitizedTitle, sanitizedQuery, editCount, appID, interactionToken, guildID, userID)
	}
}

func processAIWizard(ctx context.Context, store *Store, aiClient *ai.Client, discordToken, query, appID, interactionToken, guildID, userID string) {
	sysPrompt, _ := store.GetSystemPrompt(ctx, "wizard_prompt")
	if sysPrompt == "" {
		sysPrompt = DefaultWizardPrompt
	}

	prompt := sysPrompt + "\n\n" + fmt.Sprintf(WizardUserPromptTemplate, query)
	config := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	}

	text, _, _, err := aiClient.GenerateContentRaw(ctx, prompt, config)
	if err != nil {
		slog.Error("Gemini wizard failed", "processor", "hardwareswap", "error", err)
		sendFollowup(discordToken, appID, interactionToken, "Gemini failed to parse your request. Try wording it differently.")
		return
	}

	var wizard KeywordWizardResponse
	if err := json.Unmarshal([]byte(text), &wizard); err != nil {
		slog.Error("Failed to parse wizard response", "processor", "hardwareswap", "error", err)
		sendFollowup(discordToken, appID, interactionToken, "Failed to parse AI response. Please try again.")
		return
	}

	rule := AlertRule{
		UserID:   userID,
		ServerID: guildID,
		MustHave: wizard.MustHave,
		AnyOf:    wizard.AnyOf,
		MustNot:  wizard.MustNot,
		RawQuery: query,
	}

	if err := store.AddAlert(ctx, rule); err != nil {
		sendFollowup(discordToken, appID, interactionToken, "Failed to save alert.")
		return
	}

	alerts, _ := store.GetUserAlerts(ctx, guildID, userID)
	if len(alerts) == 0 {
		sendFollowup(discordToken, appID, interactionToken, "Failed to retrieve staged alert.")
		return
	}
	stagedAlertID := alerts[0].ID

	// Build confirmation embed
	fields := []interface{}{}
	if len(wizard.MustHave) > 0 {
		fields = append(fields, map[string]interface{}{
			"name": "Must Include", "value": "`" + strings.Join(wizard.MustHave, "`, `") + "`",
		})
	}
	if len(wizard.AnyOf) > 0 {
		fields = append(fields, map[string]interface{}{
			"name": "Match Any Of", "value": "`" + strings.Join(wizard.AnyOf, "`, `") + "`",
		})
	}
	if len(wizard.MustNot) > 0 {
		fields = append(fields, map[string]interface{}{
			"name": "Exclude", "value": "`" + strings.Join(wizard.MustNot, "`, `") + "`",
		})
	}

	color := 0x5865F2
	if wizard.TooBroad {
		color = 0xFEE75C
		suggestions := ""
		for _, s := range wizard.BroadSuggestions {
			suggestions += fmt.Sprintf("- %s\n", s)
		}
		fields = append(fields, map[string]interface{}{
			"name":  "Search is Too Broad",
			"value": fmt.Sprintf("> %s\n\n**Suggestions:**\n%s", wizard.BroadReason, suggestions),
		})
	}

	embed := map[string]interface{}{
		"title":       "Match Rule Created",
		"description": fmt.Sprintf("Converted your request into a search rule.\n\n**Intent:** *\"%s\"*", query),
		"color":       color,
		"fields":      fields,
	}

	components := []interface{}{
		map[string]interface{}{
			"type": 1,
			"components": []interface{}{
				map[string]interface{}{
					"type": 2, "style": 3,
					"label": "Looks Good! - Save", "custom_id": "hw_confirm_alert|" + stagedAlertID,
				},
				map[string]interface{}{
					"type": 2, "style": 4,
					"label": "Cancel", "custom_id": "hw_cancel_alert|" + stagedAlertID,
				},
			},
		},
	}

	sendFollowupEmbedWithComponents(discordToken, appID, interactionToken, embed, components)
}

func processManualWizard(ctx context.Context, store *Store, aiClient *ai.Client, discordToken, title, query string, editCount int, appID, interactionToken, guildID, userID string) {
	if editCount >= 3 {
		sendFollowup(discordToken, appID, interactionToken, "Alert creation cancelled due to multiple invalid query attempts. Please start over.")
		return
	}

	sysPrompt, _ := store.GetSystemPrompt(ctx, "manual_prompt")
	if sysPrompt == "" {
		sysPrompt = DefaultManualPrompt
	}

	prompt := sysPrompt + "\n\n" + fmt.Sprintf(ManualUserPromptTemplate, query)
	config := &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
	}

	text, _, _, err := aiClient.GenerateContentRaw(ctx, prompt, config)
	if err != nil {
		sendFollowup(discordToken, appID, interactionToken, "Gemini failed to validate your request. Please try again later.")
		return
	}

	var wizard KeywordWizardResponse
	if err := json.Unmarshal([]byte(text), &wizard); err != nil {
		sendFollowup(discordToken, appID, interactionToken, "Failed to parse AI response.")
		return
	}

	if !wizard.IsValid {
		_ = store.SaveAnalytics(ctx, AnalyticsRecord{
			OriginalUserPrompt: query,
			Outcome:            "Rejected_Syntax_Error",
			EditCount:          editCount,
		})

		embed := map[string]interface{}{
			"title":       "Invalid Query Syntax",
			"description": fmt.Sprintf("**Query Syntax Error:**\n`%s`\n\n**Reason:** %s", query, wizard.ErrorMessage),
			"color":       0xFF0000,
		}
		components := []interface{}{
			map[string]interface{}{
				"type": 1,
				"components": []interface{}{
					map[string]interface{}{
						"type": 2, "style": 1,
						"label": "Edit Query", "custom_id": fmt.Sprintf("hw_edit_alert||%d", editCount+1),
					},
					map[string]interface{}{
						"type": 2, "style": 4,
						"label": "Cancel", "custom_id": "hw_cancel_alert_creation|",
					},
				},
			},
		}
		sendFollowupEmbedWithComponents(discordToken, appID, interactionToken, embed, components)
		return
	}

	// Valid query -- stage and confirm
	desc := fmt.Sprintf("**Title:** *%s*\n**Raw Query:** `%s`\n\n**Parsed As:**\n", title, query)
	if len(wizard.MustHave) > 0 {
		desc += fmt.Sprintf("- **ALL of:** `%s`\n", strings.Join(wizard.MustHave, "`, `"))
	}
	if len(wizard.AnyOf) > 0 {
		desc += fmt.Sprintf("- **AT LEAST ONE of:** `%s`\n", strings.Join(wizard.AnyOf, "`, `"))
	}
	if len(wizard.MustNot) > 0 {
		desc += fmt.Sprintf("- **NONE of:** `%s`\n", strings.Join(wizard.MustNot, "`, `"))
	}

	rule := AlertRule{
		UserID:   userID,
		ServerID: guildID,
		MustHave: wizard.MustHave,
		AnyOf:    wizard.AnyOf,
		MustNot:  wizard.MustNot,
		RawQuery: title,
	}

	if err := store.AddAlert(ctx, rule); err != nil {
		sendFollowup(discordToken, appID, interactionToken, "Failed to save alert.")
		return
	}

	alerts, _ := store.GetUserAlerts(ctx, guildID, userID)
	if len(alerts) == 0 {
		sendFollowup(discordToken, appID, interactionToken, "System error while saving alert.")
		return
	}
	stagedAlertID := alerts[0].ID

	embed := map[string]interface{}{
		"title":       "Check Your Manual Query",
		"description": desc,
		"color":       0x00FF00,
	}
	components := []interface{}{
		map[string]interface{}{
			"type": 1,
			"components": []interface{}{
				map[string]interface{}{
					"type": 2, "style": 3,
					"label": "Save Alert", "custom_id": "hw_confirm_alert|" + stagedAlertID + "|Manual",
				},
				map[string]interface{}{
					"type": 2, "style": 4,
					"label": "Cancel", "custom_id": "hw_cancel_alert|" + stagedAlertID + "|Manual",
				},
			},
		},
	}
	sendFollowupEmbedWithComponents(discordToken, appID, interactionToken, embed, components)
}

// extractTextInputValue extracts a text input value from modal components.
// rowIdx is the action row index, compIdx is the component index within the row.
func extractTextInputValue(components []interface{}, rowIdx, compIdx int) string {
	if rowIdx >= len(components) {
		return ""
	}
	row, ok := components[rowIdx].(map[string]interface{})
	if !ok {
		return ""
	}
	rowComponents, ok := row["components"].([]interface{})
	if !ok {
		return ""
	}
	if compIdx >= len(rowComponents) {
		return ""
	}
	comp, ok := rowComponents[compIdx].(map[string]interface{})
	if !ok {
		return ""
	}
	value, _ := comp["value"].(string)
	return value
}

// --- Discord followup helpers ---

func sendFollowup(token, appID, interactionToken, content string) {
	payload := map[string]interface{}{
		"content": content,
		"flags":   64,
	}
	endpoint := fmt.Sprintf("/webhooks/%s/%s", appID, interactionToken)
	_ = discordPost(token, endpoint, payload)
}

func sendFollowupEmbedWithComponents(token, appID, interactionToken string, embed map[string]interface{}, components []interface{}) {
	payload := map[string]interface{}{
		"embeds":     []interface{}{embed},
		"components": components,
		"flags":      64,
	}
	endpoint := fmt.Sprintf("/webhooks/%s/%s", appID, interactionToken)
	_ = discordPost(token, endpoint, payload)
}
