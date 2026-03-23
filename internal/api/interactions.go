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
	"github.com/pauljones0/rfd-discord-bot/internal/facebook"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// writeJSON encodes v as JSON to w and logs any encoding error.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("Failed to encode JSON response", "error", err)
	}
}

// Interaction constants
const (
	InteractionResponseTypePong                     = 1
	InteractionResponseTypeChannelMessageWithSource = 4
	InteractionResponseTypeUpdateMessage            = 7
	InteractionResponseTypeAutocompleteResult       = 8

	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
	InteractionTypeMessageComponent   = 3
	InteractionTypeAutocomplete       = 4
)

// Discord component type and style constants
const (
	ComponentTypeActionRow = 1
	ComponentTypeButton    = 2

	ButtonStylePrimary   = 1 // Blue
	ButtonStyleSecondary = 2 // Grey
	ButtonStyleDanger    = 4 // Red

	MessageFlagEphemeral = 64
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
	Name     string               `json:"name,omitempty"`
	Options  []interactionOption  `json:"options,omitempty"`
	CustomID string               `json:"custom_id,omitempty"` // For components
	Resolved *interactionResolved `json:"resolved,omitempty"`
}

type interactionResolved struct {
	Channels map[string]struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type int    `json:"type"`
	} `json:"channels"`
}

type interactionOption struct {
	Name    string              `json:"name"`
	Type    int                 `json:"type"`
	Value   interface{}         `json:"value,omitempty"`
	Options []interactionOption `json:"options,omitempty"` // for subcommands
	Focused bool                `json:"focused,omitempty"` // for autocomplete
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
	Content    string              `json:"content"`
	Flags      int                 `json:"flags,omitempty"`
	Components *[]discordComponent `json:"components,omitempty"`
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

// Autocomplete response types
type autocompleteResponse struct {
	Type int                      `json:"type"`
	Data autocompleteResponseData `json:"data"`
}

type autocompleteResponseData struct {
	Choices []autocompleteChoice `json:"choices"`
}

type autocompleteChoice struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Store abstracts the database operations needed by the API.
type Store interface {
	SaveSubscription(ctx context.Context, sub models.Subscription) error
	RemoveSubscription(ctx context.Context, guildID, channelID string) error
	GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	GetSubscription(ctx context.Context, guildID, channelID string) (*models.Subscription, error)
	SaveFacebookSubscription(ctx context.Context, sub models.Subscription) error
	RemoveFacebookSubscription(ctx context.Context, guildID, channelID, city string) error
	GetFacebookSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error
	RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error
	GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
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
		writeJSON(w, interactionResponse{Type: InteractionResponseTypePong})
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

	// Autocomplete
	if req.Type == InteractionTypeAutocomplete {
		h.handleAutocomplete(w, req)
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

	// Route both legacy /rfd-bot-setup and new /deals commands
	switch req.Data.Name {
	case "rfd-bot-setup":
		h.handleLegacyCommand(w, req)
	case "deals":
		h.handleDealsCommand(w, req)
	default:
		h.respondError(w, "Unknown command.")
	}
}

// handleLegacyCommand handles the old /rfd-bot-setup command for backward compatibility.
func (h *Handler) handleLegacyCommand(w http.ResponseWriter, req interactionRequest) {
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
}

// handleDealsCommand routes /deals subcommands to appropriate handlers.
func (h *Handler) handleDealsCommand(w http.ResponseWriter, req interactionRequest) {
	if req.GuildID == "" {
		h.respondPrivateMessage(w, "This command can only be used in a server.")
		return
	}

	if len(req.Data.Options) == 0 {
		h.respondPrivateMessage(w, "Please specify a subcommand.")
		return
	}

	subCommand := req.Data.Options[0]
	switch subCommand.Name {
	case "setup-rfd":
		h.handleSetupRFD(w, req, subCommand.Options)
	case "setup-ebay":
		h.handleSetupEbay(w, req, subCommand.Options)
	case "setup-facebook":
		h.handleSetupFacebook(w, req, subCommand.Options)
	case "setup-memoryexpress":
		h.handleSetupMemoryExpress(w, req, subCommand.Options)
	case "setup-bestbuy":
		h.handleSetupBestBuy(w, req, subCommand.Options)
	case "remove":
		h.handleDealsRemove(w, req, subCommand.Options)
	case "list":
		h.handleDealsList(w, req)
	default:
		h.respondPrivateMessage(w, "Unknown subcommand.")
	}
}

// handleSetupRFD handles /deals setup-rfd channel:<#channel> filter:<type>
func (h *Handler) handleSetupRFD(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID, channelName, filter string
	for _, opt := range options {
		switch opt.Name {
		case "channel":
			if val, ok := opt.Value.(string); ok {
				channelID = val
				if req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
			}
		case "filter":
			if val, ok := opt.Value.(string); ok {
				filter = val
			}
		}
	}

	if channelID == "" || filter == "" {
		h.respondPrivateMessage(w, "Please select a channel and filter type.")
		return
	}

	validFilters := map[string]bool{
		"rfd_all": true, "rfd_tech": true,
		"rfd_warm_hot": true, "rfd_warm_hot_tech": true,
		"rfd_hot": true, "rfd_hot_tech": true,
	}
	if !validFilters[filter] {
		h.respondPrivateMessage(w, "Invalid RFD filter type.")
		return
	}

	h.saveRFDEbaySubscription(w, req, channelID, channelName, filter, "rfd")
}

// handleSetupEbay handles /deals setup-ebay channel:<#channel> filter:<type>
func (h *Handler) handleSetupEbay(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID, channelName, filter string
	for _, opt := range options {
		switch opt.Name {
		case "channel":
			if val, ok := opt.Value.(string); ok {
				channelID = val
				if req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
			}
		case "filter":
			if val, ok := opt.Value.(string); ok {
				filter = val
			}
		}
	}

	if channelID == "" || filter == "" {
		h.respondPrivateMessage(w, "Please select a channel and filter type.")
		return
	}

	validFilters := map[string]bool{
		"ebay_warm_hot": true, "ebay_hot": true,
	}
	if !validFilters[filter] {
		h.respondPrivateMessage(w, "Invalid eBay filter type.")
		return
	}

	h.saveRFDEbaySubscription(w, req, channelID, channelName, filter, "ebay")
}

// saveRFDEbaySubscription saves an RFD or eBay subscription (shared logic).
func (h *Handler) saveRFDEbaySubscription(w http.ResponseWriter, req interactionRequest, channelID, channelName, dealType, subscriptionType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Check if a subscription already exists for this channel
	existing, err := h.store.GetSubscription(ctx, req.GuildID, channelID)
	if err != nil {
		slog.Error("Failed to check for existing subscription", "guild", req.GuildID, "channel", channelID, "error", err)
	}

	if existing != nil && existing.DealType != dealType {
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content: fmt.Sprintf("⚠️ <#%s> is already set up to receive **%s** deals. Do you want to overwrite it with **%s** deals?", channelID, existing.DealType, dealType),
				Flags:   MessageFlagEphemeral,
				Components: &[]discordComponent{
					{
						Type: ComponentTypeActionRow,
						Components: []discordComponent{
							{
								Type:     ComponentTypeButton,
								Style:    ButtonStylePrimary,
								Label:    "Confirm Update",
								CustomID: fmt.Sprintf("confirm_update::%s::%s::%s", channelID, dealType, channelName),
							},
							{
								Type:     ComponentTypeButton,
								Style:    ButtonStyleSecondary,
								Label:    "Cancel",
								CustomID: "confirm_cancel",
							},
						},
					},
				},
			},
		}
		writeJSON(w, res)
		return
	}

	username := "Unknown"
	if req.Member != nil {
		username = req.Member.User.Username
	}

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         dealType,
		AddedBy:          username,
		AddedAt:          time.Now(),
		SubscriptionType: subscriptionType,
	}

	if err := h.store.SaveSubscription(ctx, sub); err != nil {
		slog.Error("Failed to save subscription", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}

	label := strings.ToUpper(subscriptionType)
	h.respondPrivateMessage(w, fmt.Sprintf("✅ %s deal notifications have been set up in <#%s> with filter **%s**!", label, channelID, dealType))
}

// handleSetupFacebook handles /deals setup-facebook channel:<#channel> city:<city> [radius:<km>] [brands:<brands>]
func (h *Handler) handleSetupFacebook(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID, channelName, city, brands string
	radiusKm := 500

	for _, opt := range options {
		switch opt.Name {
		case "channel":
			if val, ok := opt.Value.(string); ok {
				channelID = val
				if req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
			}
		case "city":
			if val, ok := opt.Value.(string); ok {
				city = val
			}
		case "radius":
			// JSON numbers come as float64
			if val, ok := opt.Value.(float64); ok {
				radiusKm = int(val)
			}
		case "brands":
			if val, ok := opt.Value.(string); ok {
				brands = val
			}
		}
	}

	if channelID == "" || city == "" {
		h.respondPrivateMessage(w, "Please select a channel and city.")
		return
	}

	// Validate that the city exists in our list
	if _, ok := facebook.CityLocationIDs[city]; !ok {
		h.respondPrivateMessage(w, fmt.Sprintf("Unknown city **%s**. Please use the autocomplete suggestions.", city))
		return
	}

	if radiusKm <= 0 {
		radiusKm = 500
	}

	var filterBrands []string
	if brands != "" {
		for _, b := range strings.Split(brands, ",") {
			b = strings.TrimSpace(b)
			if b != "" {
				filterBrands = append(filterBrands, strings.ToLower(b))
			}
		}
	}

	username := "Unknown"
	if req.Member != nil {
		username = req.Member.User.Username
	}

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         "facebook_vehicles",
		AddedBy:          username,
		AddedAt:          time.Now(),
		SubscriptionType: "facebook",
		City:             city,
		RadiusKm:         radiusKm,
		FilterBrands:     filterBrands,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.store.SaveFacebookSubscription(ctx, sub); err != nil {
		slog.Error("Failed to save Facebook subscription", "guild", req.GuildID, "city", city, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}

	msg := fmt.Sprintf("✅ Facebook Marketplace car deals for **%s** (radius: %d km) will be posted in <#%s>!", city, radiusKm, channelID)
	if len(filterBrands) > 0 {
		msg += fmt.Sprintf("\nBrand filter: %s", strings.Join(filterBrands, ", "))
	}
	h.respondPrivateMessage(w, msg)
}

// handleSetupMemoryExpress handles /deals setup-memoryexpress channel:<#channel> store:<store> filter:<type>
func (h *Handler) handleSetupMemoryExpress(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID, channelName, storeCode, filter string
	for _, opt := range options {
		switch opt.Name {
		case "channel":
			if val, ok := opt.Value.(string); ok {
				channelID = val
				if req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
			}
		case "store":
			if val, ok := opt.Value.(string); ok {
				storeCode = val
			}
		case "filter":
			if val, ok := opt.Value.(string); ok {
				filter = val
			}
		}
	}

	if channelID == "" || storeCode == "" || filter == "" {
		h.respondPrivateMessage(w, "Please select a channel, store, and filter type.")
		return
	}

	if !memoryexpress.ValidStoreCode(storeCode) {
		h.respondPrivateMessage(w, fmt.Sprintf("Unknown store **%s**. Please use the autocomplete suggestions.", storeCode))
		return
	}

	validFilters := map[string]bool{
		"me_warm_hot": true, "me_hot": true,
	}
	if !validFilters[filter] {
		h.respondPrivateMessage(w, "Invalid Memory Express filter type.")
		return
	}

	username := "Unknown"
	if req.Member != nil {
		username = req.Member.User.Username
	}

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         filter,
		AddedBy:          username,
		AddedAt:          time.Now(),
		SubscriptionType: "memoryexpress",
		StoreCode:        storeCode,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.store.SaveMemExpressSubscription(ctx, sub); err != nil {
		slog.Error("Failed to save Memory Express subscription", "guild", req.GuildID, "store", storeCode, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}

	storeName := memoryexpress.StoreName(storeCode)
	h.respondPrivateMessage(w, fmt.Sprintf("✅ Memory Express clearance deals for **%s** will be posted in <#%s> with filter **%s**!", storeName, channelID, filter))
}

// handleSetupBestBuy handles /deals setup-bestbuy channel:<#channel> filter:<type>
func (h *Handler) handleSetupBestBuy(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID, channelName, filter string
	for _, opt := range options {
		switch opt.Name {
		case "channel":
			if val, ok := opt.Value.(string); ok {
				channelID = val
				if req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
			}
		case "filter":
			if val, ok := opt.Value.(string); ok {
				filter = val
			}
		}
	}

	if channelID == "" || filter == "" {
		h.respondPrivateMessage(w, "Please select a channel and filter type.")
		return
	}

	validFilters := map[string]bool{
		"bb_warm_hot": true, "bb_hot": true,
	}
	if !validFilters[filter] {
		h.respondPrivateMessage(w, "Invalid Best Buy filter type.")
		return
	}

	h.saveRFDEbaySubscription(w, req, channelID, channelName, filter, "bestbuy")
}

// handleDealsRemove handles /deals remove type:<rfd|ebay|facebook>
func (h *Handler) handleDealsRemove(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var removeType string
	for _, opt := range options {
		if opt.Name == "type" {
			if val, ok := opt.Value.(string); ok {
				removeType = val
			}
		}
	}

	if removeType == "" {
		h.respondPrivateMessage(w, "Please specify the subscription type to remove.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch removeType {
	case "rfd", "ebay", "bestbuy":
		// Get all subscriptions and filter by type
		subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			slog.Error("Failed to get subscriptions", "guild", req.GuildID, "error", err)
			h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
			return
		}

		var matching []models.Subscription
		for _, sub := range subs {
			if removeType == "rfd" && sub.IsRFD() {
				matching = append(matching, sub)
			} else if removeType == "ebay" && sub.IsEbay() {
				matching = append(matching, sub)
			} else if removeType == "bestbuy" && sub.IsBestBuy() {
				matching = append(matching, sub)
			}
		}

		if len(matching) == 0 {
			h.respondPrivateMessage(w, fmt.Sprintf("No active **%s** subscriptions found for this server.", strings.ToUpper(removeType)))
			return
		}

		components := buildRemoveButtons(matching)
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("Here are the active **%s** subscriptions. Click to remove:", strings.ToUpper(removeType)),
				Flags:      MessageFlagEphemeral,
				Components: &components,
			},
		}
		writeJSON(w, res)

	case "facebook":
		fbSubs, err := h.store.GetFacebookSubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			slog.Error("Failed to get Facebook subscriptions", "guild", req.GuildID, "error", err)
			h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
			return
		}

		if len(fbSubs) == 0 {
			h.respondPrivateMessage(w, "No active **Facebook** subscriptions found for this server.")
			return
		}

		components := buildFacebookRemoveButtons(fbSubs)
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content:    "Here are the active **Facebook** subscriptions. Click to remove:",
				Flags:      MessageFlagEphemeral,
				Components: &components,
			},
		}
		writeJSON(w, res)

	case "memoryexpress":
		meSubs, err := h.store.GetMemExpressSubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			slog.Error("Failed to get Memory Express subscriptions", "guild", req.GuildID, "error", err)
			h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
			return
		}

		if len(meSubs) == 0 {
			h.respondPrivateMessage(w, "No active **Memory Express** subscriptions found for this server.")
			return
		}

		components := buildMemExpressRemoveButtons(meSubs)
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content:    "Here are the active **Memory Express** subscriptions. Click to remove:",
				Flags:      MessageFlagEphemeral,
				Components: &components,
			},
		}
		writeJSON(w, res)

	default:
		h.respondPrivateMessage(w, "Invalid subscription type. Choose rfd, ebay, facebook, memoryexpress, or bestbuy.")
	}
}

// handleDealsList handles /deals list
func (h *Handler) handleDealsList(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get subscriptions", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
		return
	}

	fbSubs, err := h.store.GetFacebookSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get Facebook subscriptions", "guild", req.GuildID, "error", err)
	}

	meSubs, err := h.store.GetMemExpressSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get Memory Express subscriptions", "guild", req.GuildID, "error", err)
	}

	if len(subs) == 0 && len(fbSubs) == 0 && len(meSubs) == 0 {
		h.respondPrivateMessage(w, "No active deal subscriptions for this server.")
		return
	}

	var msg strings.Builder
	msg.WriteString("📋 **Active Deal Subscriptions**\n\n")

	// Group RFD/eBay subs
	var rfdSubs, ebaySubs, bbSubs []models.Subscription
	for _, sub := range subs {
		if sub.IsEbay() {
			ebaySubs = append(ebaySubs, sub)
		} else if sub.IsBestBuy() {
			bbSubs = append(bbSubs, sub)
		} else if sub.IsRFD() {
			rfdSubs = append(rfdSubs, sub)
		}
	}

	if len(rfdSubs) > 0 {
		msg.WriteString("**RFD:**\n")
		for _, sub := range rfdSubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, sub.DealType))
		}
		msg.WriteString("\n")
	}

	if len(ebaySubs) > 0 {
		msg.WriteString("**eBay:**\n")
		for _, sub := range ebaySubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, sub.DealType))
		}
		msg.WriteString("\n")
	}

	if len(fbSubs) > 0 {
		msg.WriteString("**Facebook Marketplace:**\n")
		for _, sub := range fbSubs {
			brandInfo := ""
			if len(sub.FilterBrands) > 0 {
				brandInfo = fmt.Sprintf(" | brands: %s", strings.Join(sub.FilterBrands, ", "))
			}
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s (radius: %d km%s)\n", sub.ChannelID, sub.City, sub.RadiusKm, brandInfo))
		}
		msg.WriteString("\n")
	}

	if len(meSubs) > 0 {
		msg.WriteString("**Memory Express:**\n")
		for _, sub := range meSubs {
			storeName := memoryexpress.StoreName(sub.StoreCode)
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s (%s)\n", sub.ChannelID, storeName, sub.DealType))
		}
		msg.WriteString("\n")
	}

	if len(bbSubs) > 0 {
		msg.WriteString("**Best Buy:**\n")
		for _, sub := range bbSubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, sub.DealType))
		}
	}

	h.respondPrivateMessage(w, msg.String())
}

// handleAutocomplete handles Discord autocomplete interactions (type 4).
func (h *Handler) handleAutocomplete(w http.ResponseWriter, req interactionRequest) {
	if req.Data == nil || len(req.Data.Options) == 0 {
		writeJSON(w, autocompleteResponse{
			Type: InteractionResponseTypeAutocompleteResult,
			Data: autocompleteResponseData{Choices: []autocompleteChoice{}},
		})
		return
	}

	// Find the focused option within the subcommand
	subCommand := req.Data.Options[0]
	if subCommand.Name == "setup-facebook" {
		var query string
		for _, opt := range subCommand.Options {
			if opt.Name == "city" && opt.Focused {
				if val, ok := opt.Value.(string); ok {
					query = val
				}
			}
		}

		cities := facebook.FilterCities(query)
		var choices []autocompleteChoice
		for _, city := range cities {
			choices = append(choices, autocompleteChoice{Name: city, Value: city})
			if len(choices) >= 25 { // Discord max autocomplete choices
				break
			}
		}

		writeJSON(w, autocompleteResponse{
			Type: InteractionResponseTypeAutocompleteResult,
			Data: autocompleteResponseData{Choices: choices},
		})
		return
	}

	if subCommand.Name == "setup-memoryexpress" {
		var query string
		for _, opt := range subCommand.Options {
			if opt.Name == "store" && opt.Focused {
				if val, ok := opt.Value.(string); ok {
					query = val
				}
			}
		}

		stores := memoryexpress.MatchingStores(query)
		var choices []autocompleteChoice
		for _, store := range stores {
			choices = append(choices, autocompleteChoice{Name: store.Name, Value: store.Code})
			if len(choices) >= 25 {
				break
			}
		}

		writeJSON(w, autocompleteResponse{
			Type: InteractionResponseTypeAutocompleteResult,
			Data: autocompleteResponseData{Choices: choices},
		})
		return
	}

	// Fallback: empty choices
	writeJSON(w, autocompleteResponse{
		Type: InteractionResponseTypeAutocompleteResult,
		Data: autocompleteResponseData{Choices: []autocompleteChoice{}},
	})
}

// handleSetCommand handles the legacy /rfd-bot-setup set subcommand.
func (h *Handler) handleSetCommand(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	var channelID string
	var channelName string
	var dealType string
	for _, opt := range options {
		if opt.Name == "channel" {
			if val, ok := opt.Value.(string); ok {
				channelID = val
				// Try to get channel name from resolved data
				if req.Data != nil && req.Data.Resolved != nil && req.Data.Resolved.Channels != nil {
					if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
						channelName = ch.Name
					}
				}
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
		// RFD types
		"rfd_all": true, "rfd_tech": true,
		"rfd_warm_hot": true, "rfd_warm_hot_tech": true,
		"rfd_hot": true, "rfd_hot_tech": true,
		// eBay types
		"ebay_warm_hot": true, "ebay_hot": true,
		// Cross-source types
		"warm_hot_all": true, "hot_all": true,
	}

	if !validTypes[dealType] {
		h.respondPrivateMessage(w, "Invalid deal type selected. Please use the autocomplete choices provided by the command.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Check if a subscription already exists for this channel
	existing, err := h.store.GetSubscription(ctx, req.GuildID, channelID)
	if err != nil {
		slog.Error("Failed to check for existing subscription", "guild", req.GuildID, "channel", channelID, "error", err)
		// Proceed anyway, worst case we overwrite without warning
	}

	if existing != nil && existing.DealType != dealType {
		// Found an existing subscription with a different type, ask for confirmation
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content: fmt.Sprintf("⚠️ <#%s> is already set up to receive **%s** deals. Do you want to overwrite it with **%s** deals?", channelID, existing.DealType, dealType),
				Flags:   MessageFlagEphemeral,
				Components: &[]discordComponent{
					{
						Type: ComponentTypeActionRow,
						Components: []discordComponent{
							{
								Type:     ComponentTypeButton,
								Style:    ButtonStylePrimary,
								Label:    "Confirm Update",
								CustomID: fmt.Sprintf("confirm_update::%s::%s::%s", channelID, dealType, channelName),
							},
							{
								Type:     ComponentTypeButton,
								Style:    ButtonStyleSecondary,
								Label:    "Cancel",
								CustomID: "confirm_cancel",
							},
						},
					},
				},
			},
		}
		writeJSON(w, res)
		return
	}

	username := "Unknown"
	if req.Member != nil {
		username = req.Member.User.Username
	}

	sub := models.Subscription{
		GuildID:     req.GuildID,
		ChannelID:   channelID,
		ChannelName: channelName,
		DealType:    dealType,
		AddedBy:     username,
		AddedAt:     time.Now(),
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

	components := buildRemoveButtons(subs)

	res := interactionResponse{
		Type: InteractionResponseTypeChannelMessageWithSource,
		Data: &interactionResponseData{
			Content:    "Here are the active deal channels for this server. Click the button below to remove them individually.",
			Flags:      MessageFlagEphemeral,
			Components: &components,
		},
	}
	writeJSON(w, res)
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
	channelName := ""

	if strings.HasPrefix(req.Data.CustomID, "remove_sub::") {
		trimmed := strings.TrimPrefix(req.Data.CustomID, "remove_sub::")
		parts := strings.SplitN(trimmed, "::", 2)
		channelID = parts[0]
		if len(parts) > 1 {
			dealType = parts[1]
		}
	} else if strings.HasPrefix(req.Data.CustomID, "remove_fb::") {
		// Facebook subscription removal: remove_fb::{channelID}::{city}
		trimmed := strings.TrimPrefix(req.Data.CustomID, "remove_fb::")
		parts := strings.SplitN(trimmed, "::", 2)
		if len(parts) < 2 {
			h.respondError(w, "Invalid Facebook removal data.")
			return
		}
		fbChannelID := parts[0]
		fbCity := parts[1]

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := h.store.RemoveFacebookSubscription(ctx, req.GuildID, fbChannelID, fbCity); err != nil {
			slog.Error("Failed to remove Facebook subscription", "guild", req.GuildID, "channel", fbChannelID, "city", fbCity, "error", err)
			h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
			return
		}

		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("🗑️ Facebook Marketplace subscription for **%s** has been removed from <#%s>.", fbCity, fbChannelID),
				Components: &[]discordComponent{},
			},
		}
		writeJSON(w, res)
		return
	} else if strings.HasPrefix(req.Data.CustomID, "remove_me::") {
		// Memory Express subscription removal: remove_me::{channelID}::{storeCode}
		trimmed := strings.TrimPrefix(req.Data.CustomID, "remove_me::")
		parts := strings.SplitN(trimmed, "::", 2)
		if len(parts) < 2 {
			h.respondError(w, "Invalid Memory Express removal data.")
			return
		}
		meChannelID := parts[0]
		meStoreCode := parts[1]

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := h.store.RemoveMemExpressSubscription(ctx, req.GuildID, meChannelID, meStoreCode); err != nil {
			slog.Error("Failed to remove Memory Express subscription", "guild", req.GuildID, "channel", meChannelID, "store", meStoreCode, "error", err)
			h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
			return
		}

		storeName := memoryexpress.StoreName(meStoreCode)
		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("🗑️ Memory Express subscription for **%s** has been removed from <#%s>.", storeName, meChannelID),
				Components: &[]discordComponent{},
			},
		}
		writeJSON(w, res)
		return
	} else if strings.HasPrefix(req.Data.CustomID, "confirm_update::") {
		trimmed := strings.TrimPrefix(req.Data.CustomID, "confirm_update::")
		parts := strings.SplitN(trimmed, "::", 3)
		if len(parts) < 2 {
			h.respondError(w, "Invalid confirmation data.")
			return
		}
		channelID = parts[0]
		dealType = parts[1]
		if len(parts) > 2 {
			channelName = parts[2]
		}

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		username := "Unknown"
		if req.Member != nil {
			username = req.Member.User.Username
		}

		sub := models.Subscription{
			GuildID:     req.GuildID,
			ChannelID:   channelID,
			ChannelName: channelName,
			DealType:    dealType,
			AddedBy:     username,
			AddedAt:     time.Now(),
		}

		if err := h.store.SaveSubscription(ctx, sub); err != nil {
			slog.Error("Failed to update subscription", "guild", req.GuildID, "channel", channelID, "error", err)
			h.respondPrivateMessage(w, "Failed to update subscription due to an internal error.")
			return
		}

		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("✅ RFD Bot has been successfully updated to post **%s** deals in <#%s>!", dealType, channelID),
				Components: &[]discordComponent{}, // Clear buttons
			},
		}
		writeJSON(w, res)
		return
	} else if req.Data.CustomID == "confirm_cancel" {
		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    "❌ Update cancelled. No changes were made.",
				Components: &[]discordComponent{}, // Clear buttons
			},
		}
		writeJSON(w, res)
		return
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
				Components: &[]discordComponent{}, // Clear the buttons
			},
		}
		writeJSON(w, res)
		return
	}

	if len(subs) == 0 {
		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    "🗑️ All channels removed, there are no active subscriptions for this server.",
				Components: &[]discordComponent{}, // Clear the buttons
			},
		}
		writeJSON(w, res)
		return
	}

	components := buildRemoveButtons(subs)

	res := interactionResponse{
		Type: InteractionResponseTypeUpdateMessage,
		Data: &interactionResponseData{
			Content:    fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>. Here are the remaining active deal channels:", dealType, channelID),
			Components: &components,
		},
	}
	writeJSON(w, res)
}

func (h *Handler) respondPrivateMessage(w http.ResponseWriter, msg string) {
	res := interactionResponse{
		Type: InteractionResponseTypeChannelMessageWithSource,
		Data: &interactionResponseData{
			Content: msg,
			Flags:   MessageFlagEphemeral,
		},
	}
	writeJSON(w, res)
}

func (h *Handler) respondError(w http.ResponseWriter, msg string) {
	h.respondPrivateMessage(w, "❌ Error: "+msg)
}

// buildRemoveButtons deduplicates subscriptions by channel and returns
// a slice of Discord action-row components with a danger button for each.
func buildRemoveButtons(subs []models.Subscription) []discordComponent {
	seenChannels := make(map[string]bool)
	var components []discordComponent
	for _, sub := range subs {
		if seenChannels[sub.ChannelID] {
			continue
		}
		seenChannels[sub.ChannelID] = true

		typeLabel := sub.DealType
		if typeLabel == "" {
			typeLabel = "all"
		}

		label := fmt.Sprintf("Delete Channel (%s)", typeLabel)
		if sub.ChannelName != "" {
			label = fmt.Sprintf("Delete %s from #%s", typeLabel, sub.ChannelName)
		}

		components = append(components, discordComponent{
			Type: ComponentTypeActionRow,
			Components: []discordComponent{
				{
					Type:     ComponentTypeButton,
					Style:    ButtonStyleDanger,
					Label:    label,
					CustomID: fmt.Sprintf("remove_sub::%s::%s", sub.ChannelID, typeLabel),
				},
			},
		})
	}
	return components
}

// buildFacebookRemoveButtons creates remove buttons for Facebook subscriptions.
func buildMemExpressRemoveButtons(subs []models.Subscription) []discordComponent {
	var components []discordComponent
	for _, sub := range subs {
		storeName := memoryexpress.StoreName(sub.StoreCode)
		label := fmt.Sprintf("Remove %s from #%s", storeName, sub.ChannelName)
		if sub.ChannelName == "" {
			label = fmt.Sprintf("Remove %s", storeName)
		}

		components = append(components, discordComponent{
			Type: ComponentTypeActionRow,
			Components: []discordComponent{
				{
					Type:     ComponentTypeButton,
					Style:    ButtonStyleDanger,
					Label:    label,
					CustomID: fmt.Sprintf("remove_me::%s::%s", sub.ChannelID, sub.StoreCode),
				},
			},
		})
	}
	return components
}

func buildFacebookRemoveButtons(subs []models.Subscription) []discordComponent {
	var components []discordComponent
	for _, sub := range subs {
		label := fmt.Sprintf("Remove %s from #%s", sub.City, sub.ChannelName)
		if sub.ChannelName == "" {
			label = fmt.Sprintf("Remove %s", sub.City)
		}

		components = append(components, discordComponent{
			Type: ComponentTypeActionRow,
			Components: []discordComponent{
				{
					Type:     ComponentTypeButton,
					Style:    ButtonStyleDanger,
					Label:    label,
					CustomID: fmt.Sprintf("remove_fb::%s::%s", sub.ChannelID, sub.City),
				},
			},
		})
	}
	return components
}
