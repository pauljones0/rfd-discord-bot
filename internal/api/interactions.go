package api

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// Interaction constants
const (
	InteractionResponseTypePong                     = 1
	InteractionResponseTypeChannelMessageWithSource = 4
	InteractionResponseTypeUpdateMessage            = 7

	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
	InteractionTypeMessageComponent   = 3
)

// Simplified interaction payloads
type interactionRequest struct {
	Type    int                `json:"type"`
	Data    *interactionData   `json:"data,omitempty"`
	GuildID string             `json:"guild_id,omitempty"`
	Member  *interactionMember `json:"member,omitempty"`
	Message *discordMessage    `json:"message,omitempty"`
}

type interactionData struct {
	Name     string              `json:"name,omitempty"`
	Options  []interactionOption `json:"options,omitempty"`
	CustomID string              `json:"custom_id,omitempty"` // For components
}

type interactionOption struct {
	Name    string              `json:"name"`
	Type    int                 `json:"type"`
	Value   interface{}         `json:"value,omitempty"`
	Options []interactionOption `json:"options,omitempty"` // for subcommands
}

type interactionMember struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"user"`
	Permissions string `json:"permissions"`
}

type interactionResponse struct {
	Type int                      `json:"type"`
	Data *interactionResponseData `json:"data,omitempty"`
}

type interactionResponseData struct {
	Content    string             `json:"content"`
	Flags      int                `json:"flags,omitempty"`
	Components []discordComponent `json:"components,omitempty"`
}

type discordComponent struct {
	Type       int                `json:"type"`
	CustomID   string             `json:"custom_id,omitempty"`
	Style      int                `json:"style,omitempty"`
	Label      string             `json:"label,omitempty"`
	Components []discordComponent `json:"components,omitempty"`
}

type discordMessage struct {
	ID        string `json:"id"`
	ChannelID string `json:"channel_id"`
	Content   string `json:"content"`
}

// Store abstracts the database operations needed by the API.
type Store interface {
	SaveSubscription(ctx context.Context, sub models.Subscription) error
	RemoveSubscription(ctx context.Context, guildID, channelID string) error
	GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
}

// Handler holds the dependencies for the interaction endpoint.
type Handler struct {
	pubKey ed25519.PublicKey
	store  Store
}

// NewHandler creates a new API interactions handler.
func NewHandler(cfg *config.Config, store Store) (*Handler, error) {
	if cfg.DiscordPublicKey == "" {
		return &Handler{store: store}, nil // Run without verifier if missing key, useful for testing or disabled state
	}

	keyBytes, err := hex.DecodeString(cfg.DiscordPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode discord public key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid discord public key length")
	}

	return &Handler{
		pubKey: ed25519.PublicKey(keyBytes),
		store:  store,
	}, nil
}

// ServeHTTP handles incoming HTTP requests from Discord.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Verify signature
	if len(h.pubKey) == 0 {
		http.Error(w, "Server not configured to verify signatures", http.StatusInternalServerError)
		return
	}

	signature := r.Header.Get("X-Signature-Ed25519")
	timestamp := r.Header.Get("X-Signature-Timestamp")

	if signature == "" || timestamp == "" {
		http.Error(w, "Missing signature headers", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusInternalServerError)
		return
	}

	if !h.verifySignature(signature, timestamp, body) {
		http.Error(w, "Invalid request signature", http.StatusUnauthorized)
		return
	}

	var req interactionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		slog.Error("Failed to parse interaction JSON", "error", err)
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	// PING -> PONG
	if req.Type == InteractionTypePing {
		json.NewEncoder(w).Encode(interactionResponse{Type: InteractionResponseTypePong})
		return
	}

	// Slash Command
	if req.Type == InteractionTypeApplicationCommand {
		h.handleCommand(w, req)
		return
	}

	// Message Component
	if req.Type == InteractionTypeMessageComponent {
		h.handleComponent(w, req)
		return
	}

	http.Error(w, "Unknown interaction type", http.StatusBadRequest)
}

func (h *Handler) verifySignature(hexSignature, timestamp string, body []byte) bool {
	sig, err := hex.DecodeString(hexSignature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return false
	}
	msg := []byte(timestamp)
	msg = append(msg, body...)
	return ed25519.Verify(h.pubKey, msg, sig)
}

func (h *Handler) handleCommand(w http.ResponseWriter, req interactionRequest) {
	if req.Data == nil {
		h.respondError(w, "Missing command data.")
		return
	}

	if req.Data.Name == "rfd-bot-setup" {
		if req.GuildID == "" {
			h.respondPrivateMessage(w, "This command can only be used in a server.")
			return
		}

		// Look for the subcommand
		var subCommandName string
		var subCommandOptions []interactionOption
		if len(req.Data.Options) > 0 {
			subCommandName = req.Data.Options[0].Name
			subCommandOptions = req.Data.Options[0].Options
		}

		if subCommandName == "set" {
			h.handleSetCommand(w, req, subCommandOptions)
		} else if subCommandName == "remove" {
			h.handleRemoveCommand(w, req)
		} else {
			h.respondPrivateMessage(w, "Unknown subcommand. Usage: /rfd-bot-setup set <channel> OR /rfd-bot-setup remove")
		}
		return
	}

	h.respondError(w, "Unknown command.")
}

func (h *Handler) handleSetCommand(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID string
	var dealType string
	for _, opt := range options {
		if opt.Name == "channel" {
			if val, ok := opt.Value.(string); ok {
				channelID = val
			}
		} else if opt.Name == "type" {
			if val, ok := opt.Value.(string); ok {
				dealType = val
			}
		}
	}

	if channelID == "" || dealType == "" {
		h.respondPrivateMessage(w, "Please select a channel and deal type.")
		return
	}

	validTypes := map[string]bool{
		"all": true, "tech": true, "warm_hot_all": true,
		"warm_hot_tech": true, "hot_all": true, "hot_tech": true,
	}

	if !validTypes[dealType] {
		h.respondPrivateMessage(w, "Invalid deal type selected. Please use the autocomplete choices provided by the command.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	username := "Unknown"
	if req.Member != nil {
		username = req.Member.User.Username
	}

	sub := models.Subscription{
		GuildID:   req.GuildID,
		ChannelID: channelID,
		DealType:  dealType,
		AddedBy:   username,
		AddedAt:   time.Now(),
	}

	if err := h.store.SaveSubscription(ctx, sub); err != nil {
		slog.Error("Failed to save subscription", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}

	h.respondPrivateMessage(w, fmt.Sprintf("✅ RFD Bot has been successfully set up to post deals in <#%s>!", channelID))
}

func (h *Handler) handleRemoveCommand(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if req.GuildID == "" {
		h.respondPrivateMessage(w, "This command can only be used in a server.")
		return
	}

	subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get subscriptions for guild", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
		return
	}

	if len(subs) == 0 {
		h.respondPrivateMessage(w, "There are currently no active deal subscriptions for this server.")
		return
	}

	// Deduplicate subscriptions by channel ID
	seenChannels := make(map[string]bool)
	var uniqueSubs []models.Subscription
	for _, sub := range subs {
		if !seenChannels[sub.ChannelID] {
			seenChannels[sub.ChannelID] = true
			uniqueSubs = append(uniqueSubs, sub)
		}
	}

	var components []discordComponent
	for _, sub := range uniqueSubs {
		// Create an ActionRow for each subscription
		typeLabel := sub.DealType
		if typeLabel == "" {
			typeLabel = "all"
		}

		components = append(components, discordComponent{
			Type: 1, // Action Row
			Components: []discordComponent{
				{
					Type:     2, // Button
					Style:    4, // Danger (Red)
					Label:    fmt.Sprintf("Delete Channel (%s)", typeLabel),
					CustomID: fmt.Sprintf("remove_sub_%s_%s", sub.ChannelID, typeLabel),
				},
			},
		})
	}

	res := interactionResponse{
		Type: InteractionResponseTypeChannelMessageWithSource,
		Data: &interactionResponseData{
			Content:    "Here are the active deal channels for this server. Click the button below to remove them individually.",
			Flags:      64,
			Components: components,
		},
	}
	json.NewEncoder(w).Encode(res)
}

func (h *Handler) handleComponent(w http.ResponseWriter, req interactionRequest) {
	if req.Data == nil || req.Data.CustomID == "" {
		h.respondError(w, "Missing component data.")
		return
	}

	if req.GuildID == "" {
		h.respondError(w, "This action can only be used in a server.")
		return
	}

	var channelID string
	dealType := "all"
	if strings.HasPrefix(req.Data.CustomID, "remove_sub_") {
		trimmed := strings.TrimPrefix(req.Data.CustomID, "remove_sub_")
		parts := strings.SplitN(trimmed, "_", 2)
		channelID = parts[0]
		if len(parts) > 1 {
			dealType = parts[1]
		}
	} else {
		h.respondError(w, "Unknown button clicked.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.store.RemoveSubscription(ctx, req.GuildID, channelID); err != nil {
		slog.Error("Failed to remove subscription", "guild", req.GuildID, "channel", channelID, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}

	// Fetch remaining subscriptions to update the message
	subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get remaining subscriptions for guild", "guild", req.GuildID, "error", err)
		// Fallback to old behavior if we can't fetch remaining
		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>.", dealType, channelID),
				Components: []discordComponent{}, // Clear the buttons
			},
		}
		json.NewEncoder(w).Encode(res)
		return
	}

	if len(subs) == 0 {
		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    "🗑️ All channels removed, there are no active subscriptions for this server.",
				Components: []discordComponent{}, // Clear the buttons
			},
		}
		json.NewEncoder(w).Encode(res)
		return
	}

	// Deduplicate subscriptions by channel ID
	seenChannels := make(map[string]bool)
	var uniqueSubs []models.Subscription
	for _, sub := range subs {
		if !seenChannels[sub.ChannelID] {
			seenChannels[sub.ChannelID] = true
			uniqueSubs = append(uniqueSubs, sub)
		}
	}

	var components []discordComponent
	for _, sub := range uniqueSubs {
		typeLabel := sub.DealType
		if typeLabel == "" {
			typeLabel = "all"
		}

		components = append(components, discordComponent{
			Type: 1, // Action Row
			Components: []discordComponent{
				{
					Type:     2, // Button
					Style:    4, // Danger (Red)
					Label:    fmt.Sprintf("Delete Channel (%s)", typeLabel),
					CustomID: fmt.Sprintf("remove_sub_%s_%s", sub.ChannelID, typeLabel),
				},
			},
		})
	}

	res := interactionResponse{
		Type: InteractionResponseTypeUpdateMessage,
		Data: &interactionResponseData{
			Content:    fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>. Here are the remaining active deal channels:", dealType, channelID),
			Components: components,
		},
	}
	json.NewEncoder(w).Encode(res)
}

func (h *Handler) respondPrivateMessage(w http.ResponseWriter, msg string) {
	// Flags = 64 (Ephemeral - only the user sees it)
	res := interactionResponse{
		Type: InteractionResponseTypeChannelMessageWithSource,
		Data: &interactionResponseData{
			Content: msg,
			Flags:   64,
		},
	}
	json.NewEncoder(w).Encode(res)
}

func (h *Handler) respondError(w http.ResponseWriter, msg string) {
	h.respondPrivateMessage(w, "❌ Error: "+msg)
}
