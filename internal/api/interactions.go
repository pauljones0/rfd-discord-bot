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
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// Interaction constants
const (
	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2

	InteractionResponseTypePong                     = 1
	InteractionResponseTypeChannelMessageWithSource = 4
)

// Simplified interaction payloads
type interactionRequest struct {
	Type    int                `json:"type"`
	Data    *interactionData   `json:"data,omitempty"`
	GuildID string             `json:"guild_id,omitempty"`
	Member  *interactionMember `json:"member,omitempty"`
}

type interactionData struct {
	Name    string              `json:"name"`
	Options []interactionOption `json:"options,omitempty"`
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
	Content string `json:"content"`
	Flags   int    `json:"flags,omitempty"`
}

// Store abstracts the database operations needed by the API.
type Store interface {
	SaveSubscription(ctx context.Context, sub models.Subscription) error
	RemoveSubscription(ctx context.Context, guildID string) error
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

	// Verify signature if configured
	var body []byte
	var err error
	if len(h.pubKey) > 0 {
		signature := r.Header.Get("X-Signature-Ed25519")
		timestamp := r.Header.Get("X-Signature-Timestamp")

		if signature == "" || timestamp == "" {
			http.Error(w, "Missing signature headers", http.StatusUnauthorized)
			return
		}

		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusInternalServerError)
			return
		}

		if !h.verifySignature(signature, timestamp, body) {
			http.Error(w, "Invalid request signature", http.StatusUnauthorized)
			return
		}
	} else {
		// Just read it
		body, err = io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read body", http.StatusInternalServerError)
			return
		}
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
	for _, opt := range options {
		if opt.Name == "channel" {
			if val, ok := opt.Value.(string); ok {
				channelID = val
			}
			break
		}
	}

	if channelID == "" {
		h.respondPrivateMessage(w, "Please select a channel.")
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

	if err := h.store.RemoveSubscription(ctx, req.GuildID); err != nil {
		slog.Error("Failed to remove subscription", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}

	h.respondPrivateMessage(w, "🗑️ RFD Bot subscription has been removed from this server.")
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
