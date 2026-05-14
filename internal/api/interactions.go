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

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/facebook"
	"github.com/pauljones0/rfd-discord-bot/internal/hardwareswap"
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
	InteractionResponseTypeDeferredChannelMessage   = 5
	InteractionResponseTypeAutocompleteResult       = 8
	InteractionResponseTypeModal                    = 9

	InteractionTypePing               = 1
	InteractionTypeApplicationCommand = 2
	InteractionTypeMessageComponent   = 3
	InteractionTypeAutocomplete       = 4
	InteractionTypeModalSubmit        = 5
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
	Token   string             `json:"token,omitempty"` // Interaction token for deferred responses
	AppID   string             `json:"application_id,omitempty"`
}

type interactionData struct {
	Name       string               `json:"name,omitempty"`
	Options    []interactionOption  `json:"options,omitempty"`
	CustomID   string               `json:"custom_id,omitempty"` // For components and modals
	Resolved   *interactionResolved `json:"resolved,omitempty"`
	Components []interface{}        `json:"components,omitempty"` // For modal submit data
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
	RemoveSubscription(ctx context.Context, guildID, channelID, dealType string) error
	GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	GetSubscription(ctx context.Context, guildID, channelID string) (*models.Subscription, error)
	SaveFacebookSubscription(ctx context.Context, sub models.Subscription) error
	RemoveFacebookSubscription(ctx context.Context, guildID, channelID, city string) error
	GetFacebookSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error
	RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error
	GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	SaveBestBuySubscription(ctx context.Context, sub models.Subscription) error
	RemoveBestBuySubscription(ctx context.Context, guildID, channelID string) error
	GetBestBuySubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
}

// Handler holds the dependencies for the interaction endpoint.
type Handler struct {
	pubKey              ed25519.PublicKey
	store               Store
	hwStore             *hardwareswap.Store
	aiClient            *ai.Client
	discordToken        string
	discordAppID        string
	facebookEnabled     bool
	hardwareSwapEnabled bool
}

// NewHandler creates a new API interactions handler.
func NewHandler(cfg *config.Config, store Store, hwStore *hardwareswap.Store, aiClient *ai.Client) (*Handler, error) {
	if cfg.DiscordPublicKey == "" {
		return &Handler{
			store:               store,
			hwStore:             hwStore,
			aiClient:            aiClient,
			discordToken:        cfg.DiscordBotToken,
			discordAppID:        cfg.DiscordAppID,
			facebookEnabled:     cfg.FacebookEnabled,
			hardwareSwapEnabled: cfg.HardwareSwapEnabled,
		}, nil // Run without verifier if missing key, useful for testing or disabled state
	}

	keyBytes, err := hex.DecodeString(cfg.DiscordPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to decode discord public key: %w", err)
	}
	if len(keyBytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid discord public key length")
	}

	return &Handler{
		pubKey:              ed25519.PublicKey(keyBytes),
		store:               store,
		hwStore:             hwStore,
		aiClient:            aiClient,
		discordToken:        cfg.DiscordBotToken,
		discordAppID:        cfg.DiscordAppID,
		facebookEnabled:     cfg.FacebookEnabled,
		hardwareSwapEnabled: cfg.HardwareSwapEnabled,
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

	// Modal Submit
	if req.Type == InteractionTypeModalSubmit {
		h.handleModalSubmit(w, req)
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

	subcommand := ""
	if len(req.Data.Options) > 0 {
		subcommand = req.Data.Options[0].Name
	}
	slog.Info("Discord application command received", "command", req.Data.Name, "subcommand", subcommand, "guild", req.GuildID)

	switch req.Data.Name {
	case "deals":
		h.handleDealsCommand(w, req)
	case "hw-setup", "hw-help", "hw-alert":
		if !h.hardwareSwapEnabled {
			h.respondError(w, "HardwareSwap features are disabled.")
			return
		}
		h.handleHWCommand(w, req)
	default:
		h.respondError(w, "Unknown command.")
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
		if !h.facebookEnabled {
			h.respondPrivateMessage(w, "Facebook Marketplace features are currently disabled.")
			return
		}
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

	if !dealtypes.IsRFD(filter) {
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

	if !dealtypes.IsEbay(filter) {
		h.respondPrivateMessage(w, "Invalid eBay filter type.")
		return
	}

	h.saveRFDEbaySubscription(w, req, channelID, channelName, filter, "ebay")
}

// saveRFDEbaySubscription saves an RFD or eBay subscription (shared logic).
func (h *Handler) saveRFDEbaySubscription(w http.ResponseWriter, req interactionRequest, channelID, channelName, dealType, subscriptionType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

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
	h.respondPrivateMessage(w, fmt.Sprintf("✅ %s deal notifications have been set up in <#%s> with filter **%s**!", label, channelID, dealTypeLabel(dealType)))
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

	if !dealtypes.IsMemoryExpress(filter) {
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

	if !dealtypes.IsBestBuy(filter) {
		h.respondPrivateMessage(w, "Invalid Best Buy filter type.")
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
		SubscriptionType: "bestbuy",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.store.SaveBestBuySubscription(ctx, sub); err != nil {
		slog.Error("Failed to save Best Buy subscription", "guild", req.GuildID, "channel", channelID, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}

	h.respondPrivateMessage(w, fmt.Sprintf("Best Buy alerts will be posted in <#%s> with filter **%s**.", channelID, dealTypeLabel(filter)))
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
	if !dealtypes.ValidSubscriptionType(removeType, h.facebookEnabled, h.hardwareSwapEnabled) {
		h.respondPrivateMessage(w, "Invalid or disabled subscription type.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	switch removeType {
	case "rfd", "ebay":
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
		if !h.facebookEnabled {
			h.respondPrivateMessage(w, "Facebook Marketplace features are currently disabled.")
			return
		}
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

	case "bestbuy":
		bbSubs, err := h.store.GetBestBuySubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			slog.Error("Failed to get Best Buy subscriptions", "guild", req.GuildID, "error", err)
			h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
			return
		}

		if len(bbSubs) == 0 {
			h.respondPrivateMessage(w, "No active **Best Buy** subscriptions found for this server.")
			return
		}

		components := buildBestBuyRemoveButtons(bbSubs)
		res := interactionResponse{
			Type: InteractionResponseTypeChannelMessageWithSource,
			Data: &interactionResponseData{
				Content:    "Here are the active **Best Buy** subscriptions. Click to remove:",
				Flags:      MessageFlagEphemeral,
				Components: &components,
			},
		}
		writeJSON(w, res)

	default:
		h.respondPrivateMessage(w, "Invalid subscription type.")
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

	var fbSubs []models.Subscription
	if h.facebookEnabled {
		var err error
		fbSubs, err = h.store.GetFacebookSubscriptionsByGuild(ctx, req.GuildID)
		if err != nil {
			slog.Error("Failed to get Facebook subscriptions", "guild", req.GuildID, "error", err)
		}
	}

	meSubs, err := h.store.GetMemExpressSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get Memory Express subscriptions", "guild", req.GuildID, "error", err)
	}

	bbSubs, err := h.store.GetBestBuySubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get Best Buy subscriptions", "guild", req.GuildID, "error", err)
	}

	if len(subs) == 0 && len(fbSubs) == 0 && len(meSubs) == 0 && len(bbSubs) == 0 {
		h.respondPrivateMessage(w, "No active deal subscriptions for this server.")
		return
	}

	var msg strings.Builder
	msg.WriteString("📋 **Active Deal Subscriptions**\n\n")

	// Group RFD/eBay subs
	var rfdSubs, ebaySubs []models.Subscription
	for _, sub := range subs {
		if sub.IsEbay() {
			ebaySubs = append(ebaySubs, sub)
		} else if sub.IsRFD() {
			rfdSubs = append(rfdSubs, sub)
		}
	}

	if len(rfdSubs) > 0 {
		msg.WriteString("**RFD:**\n")
		for _, sub := range rfdSubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
		}
		msg.WriteString("\n")
	}

	if len(ebaySubs) > 0 {
		msg.WriteString("**eBay:**\n")
		for _, sub := range ebaySubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
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
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s (%s)\n", sub.ChannelID, storeName, dealTypeLabel(sub.DealType)))
		}
		msg.WriteString("\n")
	}

	if len(bbSubs) > 0 {
		msg.WriteString("**Best Buy:**\n")
		for _, sub := range bbSubs {
			msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
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
	if subCommand.Name == "setup-facebook" && h.facebookEnabled {
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

	if strings.HasPrefix(req.Data.CustomID, "hw_") {
		if !h.hardwareSwapEnabled {
			h.respondError(w, "HardwareSwap features are disabled.")
			return
		}
		h.handleHWComponent(w, req)
		return
	}

	if strings.HasPrefix(req.Data.CustomID, "remove_sub::") {
		trimmed := strings.TrimPrefix(req.Data.CustomID, "remove_sub::")
		parts := strings.SplitN(trimmed, "::", 2)
		channelID = parts[0]
		if len(parts) > 1 {
			dealType = parts[1]
		}
	} else if strings.HasPrefix(req.Data.CustomID, "remove_fb::") {
		if !h.facebookEnabled {
			h.respondError(w, "Facebook Marketplace features are disabled.")
			return
		}
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
	} else if strings.HasPrefix(req.Data.CustomID, "remove_bb::") {
		// Best Buy subscription removal: remove_bb::{channelID}
		bbChannelID := strings.TrimPrefix(req.Data.CustomID, "remove_bb::")

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		if err := h.store.RemoveBestBuySubscription(ctx, req.GuildID, bbChannelID); err != nil {
			slog.Error("Failed to remove Best Buy subscription", "guild", req.GuildID, "channel", bbChannelID, "error", err)
			h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
			return
		}

		res := interactionResponse{
			Type: InteractionResponseTypeUpdateMessage,
			Data: &interactionResponseData{
				Content:    fmt.Sprintf("🗑️ Best Buy subscription has been removed from <#%s>.", bbChannelID),
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
	} else {
		h.respondError(w, "Unknown button clicked.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := h.store.RemoveSubscription(ctx, req.GuildID, channelID, dealType); err != nil {
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
				Content:    fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>.", dealTypeLabel(dealType), channelID),
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
			Content:    fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>. Here are the remaining active deal channels:", dealTypeLabel(dealType), channelID),
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

// buildRemoveButtons returns a danger button for each subscription.
func buildRemoveButtons(subs []models.Subscription) []discordComponent {
	var components []discordComponent
	for _, sub := range subs {
		typeLabel := dealTypeLabel(sub.DealType)

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
					CustomID: fmt.Sprintf("remove_sub::%s::%s", sub.ChannelID, sub.DealType),
				},
			},
		})
	}
	return components
}

func dealTypeLabel(dealType string) string {
	return dealtypes.Label(dealType)
}

// buildFacebookRemoveButtons creates remove buttons for Facebook subscriptions.
func buildMemExpressRemoveButtons(subs []models.Subscription) []discordComponent {
	var components []discordComponent
	for _, sub := range subs {
		storeName := memoryexpress.StoreName(sub.StoreCode)
		filterLabel := dealTypeLabel(sub.DealType)
		label := fmt.Sprintf("Remove %s (%s) from #%s", storeName, filterLabel, sub.ChannelName)
		if sub.ChannelName == "" {
			label = fmt.Sprintf("Remove %s (%s)", storeName, filterLabel)
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

func buildBestBuyRemoveButtons(subs []models.Subscription) []discordComponent {
	var components []discordComponent
	for _, sub := range subs {
		filterLabel := dealTypeLabel(sub.DealType)
		label := fmt.Sprintf("Remove Best Buy (%s) from #%s", filterLabel, sub.ChannelName)
		if sub.ChannelName == "" {
			label = fmt.Sprintf("Remove Best Buy (%s)", filterLabel)
		}

		components = append(components, discordComponent{
			Type: ComponentTypeActionRow,
			Components: []discordComponent{
				{
					Type:     ComponentTypeButton,
					Style:    ButtonStyleDanger,
					Label:    label,
					CustomID: fmt.Sprintf("remove_bb::%s", sub.ChannelID),
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

// handleHWCommand routes hw-setup, hw-help, hw-alert commands to the hardwareswap package.
func (h *Handler) handleHWCommand(w http.ResponseWriter, req interactionRequest) {
	if h.hwStore == nil {
		h.respondPrivateMessage(w, "HardwareSwap features are not configured on this bot.")
		return
	}

	if req.GuildID == "" {
		h.respondPrivateMessage(w, "This command can only be used in a server.")
		return
	}

	userID := ""
	if req.Member != nil {
		userID = req.Member.User.ID
	}

	// Convert typed options to []interface{} for the hardwareswap package
	options := convertOptionsToGeneric(req.Data.Options)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := hardwareswap.HandleCommand(ctx, w, h.hwStore, req.Data.Name, options, req.GuildID, userID)
	if result == nil {
		h.respondError(w, "Unknown HardwareSwap command.")
		return
	}

	writeJSON(w, result)
}

// handleHWComponent routes hw_ prefixed component interactions to the hardwareswap package.
func (h *Handler) handleHWComponent(w http.ResponseWriter, req interactionRequest) {
	if h.hwStore == nil {
		h.respondPrivateMessage(w, "HardwareSwap features are not configured on this bot.")
		return
	}

	userID := ""
	if req.Member != nil {
		userID = req.Member.User.ID
	}

	// Collect message embeds as []interface{} from the raw message
	var messageEmbeds []interface{}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	result := hardwareswap.HandleComponent(ctx, h.hwStore, h.aiClient, h.discordToken, req.Data.CustomID, req.GuildID, userID, messageEmbeds)
	if result == nil {
		h.respondError(w, "Unknown HardwareSwap component.")
		return
	}

	writeJSON(w, result)
}

// handleModalSubmit routes modal submissions.
func (h *Handler) handleModalSubmit(w http.ResponseWriter, req interactionRequest) {
	if req.Data == nil || req.Data.CustomID == "" {
		h.respondError(w, "Missing modal data.")
		return
	}

	if strings.HasPrefix(req.Data.CustomID, "hw_modal_") {
		if !h.hardwareSwapEnabled {
			h.respondError(w, "HardwareSwap features are disabled.")
			return
		}
		h.handleHWModalSubmit(w, req)
		return
	}

	h.respondError(w, "Unknown modal submission.")
}

// handleHWModalSubmit handles HardwareSwap modal submissions with a deferred response.
func (h *Handler) handleHWModalSubmit(w http.ResponseWriter, req interactionRequest) {
	if h.hwStore == nil {
		h.respondPrivateMessage(w, "HardwareSwap features are not configured on this bot.")
		return
	}

	userID := ""
	if req.Member != nil {
		userID = req.Member.User.ID
	}

	// Write deferred response immediately (type 5 — DeferredChannelMessageWithSource)
	writeJSON(w, map[string]interface{}{
		"type": InteractionResponseTypeDeferredChannelMessage,
		"data": map[string]interface{}{
			"flags": MessageFlagEphemeral,
		},
	})

	// Process asynchronously — HandleModalSubmit sends follow-up messages via the Discord API
	hardwareswap.HandleModalSubmit(
		h.hwStore,
		h.aiClient,
		h.discordToken,
		req.Data.CustomID,
		req.Data.Components,
		h.discordAppID,
		req.Token,
		req.GuildID,
		userID,
	)
}

// convertOptionsToGeneric converts typed interactionOption slices to []interface{}
// for use by the hardwareswap package which works with raw JSON maps.
func convertOptionsToGeneric(options []interactionOption) []interface{} {
	result := make([]interface{}, len(options))
	for i, opt := range options {
		m := map[string]interface{}{
			"name": opt.Name,
			"type": opt.Type,
		}
		if opt.Value != nil {
			m["value"] = opt.Value
		}
		if len(opt.Options) > 0 {
			m["options"] = convertOptionsToGeneric(opt.Options)
		}
		result[i] = m
	}
	return result
}
