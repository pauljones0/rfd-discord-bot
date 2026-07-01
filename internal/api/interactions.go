package api

import (
	"bytes"
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
	"github.com/pauljones0/rfd-discord-bot/internal/core"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/facebook"
	"github.com/pauljones0/rfd-discord-bot/internal/hardwareswap"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/genai"
)

// writeJSON encodes v as JSON to w and logs any encoding error.
func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("Failed to encode JSON response", "error", err)
	}
}

func optionString(options []interactionOption, name string) (string, bool) {
	for _, opt := range options {
		if opt.Name != name {
			continue
		}
		value, ok := opt.Value.(string)
		return value, ok
	}
	return "", false
}

func optionFloat(options []interactionOption, name string) (float64, bool) {
	for _, opt := range options {
		if opt.Name != name {
			continue
		}
		value, ok := opt.Value.(float64)
		return value, ok
	}
	return 0, false
}

func selectedChannel(req interactionRequest, options []interactionOption) (string, string) {
	channelID, _ := optionString(options, "channel")
	if channelID == "" || req.Data == nil || req.Data.Resolved == nil || req.Data.Resolved.Channels == nil {
		return channelID, ""
	}
	if ch, exists := req.Data.Resolved.Channels[channelID]; exists {
		return channelID, ch.Name
	}
	return channelID, ""
}

func requestUsername(req interactionRequest) string {
	if req.Member == nil || req.Member.User.Username == "" {
		return "Unknown"
	}
	return req.Member.User.Username
}

func storeContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

type channelFilterSetupSpec struct {
	SubscriptionType string
	Validate         func(string) bool
	InvalidMessage   string
	Save             func(Store, context.Context, models.Subscription) error
	SuccessMessage   func(channelID, filter string) string
}

var channelFilterSetupSpecs = map[string]channelFilterSetupSpec{
	"rfd": {
		SubscriptionType: "rfd",
		Validate:         dealtypes.IsRFD,
		InvalidMessage:   "Invalid RFD filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveSubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("✅ RFD deal notifications have been set up in <#%s> with filter **%s**!", channelID, dealTypeLabel(filter))
		},
	},
	"ebay": {
		SubscriptionType: "ebay",
		Validate:         dealtypes.IsEbay,
		InvalidMessage:   "Invalid eBay filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveSubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("✅ EBAY deal notifications have been set up in <#%s> with filter **%s**!", channelID, dealTypeLabel(filter))
		},
	},
	"bestbuy": {
		SubscriptionType: "bestbuy",
		Validate:         dealtypes.IsBestBuy,
		InvalidMessage:   "Invalid Best Buy filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveBestBuySubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("Best Buy alerts will be posted in <#%s> with filter **%s**.", channelID, dealTypeLabel(filter))
		},
	},
	"core": {
		SubscriptionType: "core",
		Validate:         dealtypes.IsCore,
		InvalidMessage:   "Invalid Core filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveSubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("✅ Core deal alerts will be posted in <#%s> with filter **%s**.", channelID, dealTypeLabel(filter))
		},
	},
	"oneverycorner": {
		SubscriptionType: dealtypes.SubscriptionOnEveryCorner,
		Validate:         dealtypes.IsOnEveryCorner,
		InvalidMessage:   "Invalid OnEveryCorner filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveSubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("✅ OnEveryCorner alerts will be posted in <#%s> with filter **%s**.", channelID, dealTypeLabel(filter))
		},
	},
	"crux": {
		SubscriptionType: dealtypes.SubscriptionCrux,
		Validate:         dealtypes.IsCrux,
		InvalidMessage:   "Invalid Crux filter type.",
		Save: func(store Store, ctx context.Context, sub models.Subscription) error {
			return store.SaveSubscription(ctx, sub)
		},
		SuccessMessage: func(channelID, filter string) string {
			return fmt.Sprintf("✅ Crux Investor alerts will be posted in <#%s> with filter **%s**.", channelID, dealTypeLabel(filter))
		},
	},
}

func (h *Handler) handleChannelFilterSetup(w http.ResponseWriter, req interactionRequest, options []interactionOption, kind string) {
	spec, ok := channelFilterSetupSpecs[kind]
	if !ok {
		h.respondPrivateMessage(w, "Unknown subscription type.")
		return
	}
	channelID, channelName := selectedChannel(req, options)
	filter, _ := optionString(options, "filter")
	if channelID == "" || filter == "" {
		h.respondPrivateMessage(w, "Please select a channel and filter type.")
		return
	}
	if !spec.Validate(filter) {
		h.respondPrivateMessage(w, spec.InvalidMessage)
		return
	}

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         filter,
		AddedBy:          requestUsername(req),
		AddedAt:          time.Now(),
		SubscriptionType: spec.SubscriptionType,
	}

	ctx, cancel := storeContext()
	defer cancel()
	if err := spec.Save(h.store, ctx, sub); err != nil {
		slog.Error("Failed to save subscription", "guild", req.GuildID, "type", spec.SubscriptionType, "error", err)
		h.respondPrivateMessage(w, "Failed to save subscription due to an internal error.")
		return
	}
	h.respondPrivateMessage(w, spec.SuccessMessage(channelID, filter))
}

type removeAction struct {
	Kind      string
	ChannelID string
	Value     string
}

func parseRemoveAction(customID string) (removeAction, bool) {
	for _, candidate := range []struct {
		prefix string
		kind   string
	}{
		{prefix: "remove_sub::", kind: "sub"},
		{prefix: "remove_fb::", kind: "facebook"},
		{prefix: "remove_bb::", kind: "bestbuy"},
		{prefix: "remove_me::", kind: "memoryexpress"},
	} {
		if !strings.HasPrefix(customID, candidate.prefix) {
			continue
		}
		trimmed := strings.TrimPrefix(customID, candidate.prefix)
		parts := strings.SplitN(trimmed, "::", 2)
		if parts[0] == "" {
			return removeAction{}, false
		}
		value := ""
		if len(parts) > 1 {
			value = parts[1]
		}
		return removeAction{Kind: candidate.kind, ChannelID: parts[0], Value: value}, true
	}
	return removeAction{}, false
}

func writeUpdateMessage(w http.ResponseWriter, content string, components []discordComponent) {
	res := interactionResponse{
		Type: InteractionResponseTypeUpdateMessage,
		Data: &interactionResponseData{
			Content:    content,
			Components: &components,
		},
	}
	writeJSON(w, res)
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
	GetCoreSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error)
	GetRecentCoreRawNotifications(ctx context.Context, duration time.Duration) ([]models.CoreRawNotification, error)
	GetCoreRules(ctx context.Context) ([]models.CoreRule, error)
	SaveCoreRules(ctx context.Context, rules []models.CoreRule) error
	GetPendingCoreRules(ctx context.Context) ([]models.CoreRule, error)
	SavePendingCoreRules(ctx context.Context, rules []models.CoreRule) error
	DeletePendingCoreRules(ctx context.Context) error
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
	fallbackModels      []string
}

// NewHandler creates a new API interactions handler.
func NewHandler(cfg *config.Config, store Store, hwStore *hardwareswap.Store, aiClient *ai.Client) (*Handler, error) {
	if cfg.DiscordPublicKey == "" {
		if !cfg.AllowUnsignedDiscordInteractions {
			return nil, fmt.Errorf("DISCORD_PUBLIC_KEY is required unless ALLOW_UNSIGNED_DISCORD_INTERACTIONS=true")
		}
		return &Handler{
			store:               store,
			hwStore:             hwStore,
			aiClient:            aiClient,
			discordToken:        cfg.DiscordBotToken,
			discordAppID:        cfg.DiscordAppID,
			facebookEnabled:     cfg.FacebookEnabled,
			hardwareSwapEnabled: cfg.HardwareSwapEnabled,
			fallbackModels:      cfg.GeminiFallbackModels,
		}, nil
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
		fallbackModels:      cfg.GeminiFallbackModels,
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
	case "setup-core":
		h.handleSetupCore(w, req, subCommand.Options)
	case "setup-oneverycorner":
		h.handleSetupOnEveryCorner(w, req, subCommand.Options)
	case "setup-crux":
		h.handleSetupCrux(w, req, subCommand.Options)
	case "suggest-rules":
		h.handleCoreSuggestRules(w, req)
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
	h.handleChannelFilterSetup(w, req, options, "rfd")
}

// handleSetupEbay handles /deals setup-ebay channel:<#channel> filter:<type>
func (h *Handler) handleSetupEbay(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	h.handleChannelFilterSetup(w, req, options, "ebay")
}

// handleSetupFacebook handles /deals setup-facebook channel:<#channel> city:<city> [radius:<km>] [brands:<brands>]
func (h *Handler) handleSetupFacebook(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	channelID, channelName := selectedChannel(req, options)
	city, _ := optionString(options, "city")
	brands, _ := optionString(options, "brands")
	radiusKm := 500
	if radius, ok := optionFloat(options, "radius"); ok {
		radiusKm = int(radius)
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

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         "facebook_vehicles",
		AddedBy:          requestUsername(req),
		AddedAt:          time.Now(),
		SubscriptionType: "facebook",
		City:             city,
		RadiusKm:         radiusKm,
		FilterBrands:     filterBrands,
	}

	ctx, cancel := storeContext()
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
	channelID, channelName := selectedChannel(req, options)
	storeCode, _ := optionString(options, "store")
	filter, _ := optionString(options, "filter")

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

	sub := models.Subscription{
		GuildID:          req.GuildID,
		ChannelID:        channelID,
		ChannelName:      channelName,
		DealType:         filter,
		AddedBy:          requestUsername(req),
		AddedAt:          time.Now(),
		SubscriptionType: "memoryexpress",
		StoreCode:        storeCode,
	}

	ctx, cancel := storeContext()
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
	h.handleChannelFilterSetup(w, req, options, "bestbuy")
}

// handleSetupCore handles /deals setup-core channel:<#channel> filter:<type>
func (h *Handler) handleSetupCore(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	h.handleChannelFilterSetup(w, req, options, "core")
}

// handleSetupOnEveryCorner handles /deals setup-oneverycorner channel:<#channel> filter:<type>
func (h *Handler) handleSetupOnEveryCorner(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	h.handleChannelFilterSetup(w, req, options, "oneverycorner")
}

// handleSetupCrux handles /deals setup-crux channel:<#channel> filter:<type>
func (h *Handler) handleSetupCrux(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	h.handleChannelFilterSetup(w, req, options, "crux")
}

type subscriptionRemoveSpec struct {
	NoActiveMessage string
	Prompt          string
	List            func(*Handler, context.Context, string) ([]models.Subscription, error)
	BuildButtons    func([]models.Subscription) []discordComponent
}

func (h *Handler) subscriptionRemoveSpec(removeType string) (subscriptionRemoveSpec, bool) {
	specs := map[string]subscriptionRemoveSpec{
		"rfd": {
			NoActiveMessage: "No active **RFD** subscriptions found for this server.",
			Prompt:          "Here are the active **RFD** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.listDealSubscriptions(ctx, guildID, func(sub models.Subscription) bool { return sub.IsRFD() })
			},
			BuildButtons: buildRemoveButtons,
		},
		"ebay": {
			NoActiveMessage: "No active **EBAY** subscriptions found for this server.",
			Prompt:          "Here are the active **EBAY** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.listDealSubscriptions(ctx, guildID, func(sub models.Subscription) bool { return sub.IsEbay() })
			},
			BuildButtons: buildRemoveButtons,
		},
		"facebook": {
			NoActiveMessage: "No active **Facebook** subscriptions found for this server.",
			Prompt:          "Here are the active **Facebook** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.store.GetFacebookSubscriptionsByGuild(ctx, guildID)
			},
			BuildButtons: buildFacebookRemoveButtons,
		},
		"memoryexpress": {
			NoActiveMessage: "No active **Memory Express** subscriptions found for this server.",
			Prompt:          "Here are the active **Memory Express** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.store.GetMemExpressSubscriptionsByGuild(ctx, guildID)
			},
			BuildButtons: buildMemExpressRemoveButtons,
		},
		"bestbuy": {
			NoActiveMessage: "No active **Best Buy** subscriptions found for this server.",
			Prompt:          "Here are the active **Best Buy** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.store.GetBestBuySubscriptionsByGuild(ctx, guildID)
			},
			BuildButtons: buildBestBuyRemoveButtons,
		},
		"core": {
			NoActiveMessage: "No active **Core** subscriptions found for this server.",
			Prompt:          "Here are the active **Core** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.listDealSubscriptions(ctx, guildID, func(sub models.Subscription) bool { return sub.IsCore() })
			},
			BuildButtons: buildRemoveButtons,
		},
		"oneverycorner": {
			NoActiveMessage: "No active **OnEveryCorner** subscriptions found for this server.",
			Prompt:          "Here are the active **OnEveryCorner** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.listDealSubscriptions(ctx, guildID, func(sub models.Subscription) bool { return sub.IsOnEveryCorner() })
			},
			BuildButtons: buildRemoveButtons,
		},
		"crux": {
			NoActiveMessage: "No active **Crux Investor** subscriptions found for this server.",
			Prompt:          "Here are the active **Crux Investor** subscriptions. Click to remove:",
			List: func(h *Handler, ctx context.Context, guildID string) ([]models.Subscription, error) {
				return h.listDealSubscriptions(ctx, guildID, func(sub models.Subscription) bool { return sub.IsCrux() })
			},
			BuildButtons: buildRemoveButtons,
		},
	}
	spec, ok := specs[removeType]
	return spec, ok
}

func (h *Handler) listDealSubscriptions(ctx context.Context, guildID string, keep func(models.Subscription) bool) ([]models.Subscription, error) {
	subs, err := h.store.GetSubscriptionsByGuild(ctx, guildID)
	if err != nil {
		return nil, err
	}
	matching := make([]models.Subscription, 0, len(subs))
	for _, sub := range subs {
		if keep(sub) {
			matching = append(matching, sub)
		}
	}
	return matching, nil
}

func respondRemoveChoices(w http.ResponseWriter, prompt string, components []discordComponent) {
	res := interactionResponse{
		Type: InteractionResponseTypeChannelMessageWithSource,
		Data: &interactionResponseData{
			Content:    prompt,
			Flags:      MessageFlagEphemeral,
			Components: &components,
		},
	}
	writeJSON(w, res)
}

func splitDealSubscriptions(subs []models.Subscription) ([]models.Subscription, []models.Subscription, []models.Subscription, []models.Subscription, []models.Subscription) {
	var rfdSubs, ebaySubs, coreSubs, onEveryCornerSubs, cruxSubs []models.Subscription
	for _, sub := range subs {
		switch {
		case sub.IsEbay():
			ebaySubs = append(ebaySubs, sub)
		case sub.IsCore():
			coreSubs = append(coreSubs, sub)
		case sub.IsOnEveryCorner():
			onEveryCornerSubs = append(onEveryCornerSubs, sub)
		case sub.IsCrux():
			cruxSubs = append(cruxSubs, sub)
		case sub.IsRFD():
			rfdSubs = append(rfdSubs, sub)
		}
	}
	return rfdSubs, ebaySubs, coreSubs, onEveryCornerSubs, cruxSubs
}

func appendDealListSection(msg *strings.Builder, title string, subs []models.Subscription) {
	if len(subs) == 0 {
		return
	}
	msg.WriteString("**" + title + ":**\n")
	for _, sub := range subs {
		msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
	}
	msg.WriteString("\n")
}

func appendFacebookListSection(msg *strings.Builder, subs []models.Subscription) {
	if len(subs) == 0 {
		return
	}
	msg.WriteString("**Facebook Marketplace:**\n")
	for _, sub := range subs {
		brandInfo := ""
		if len(sub.FilterBrands) > 0 {
			brandInfo = fmt.Sprintf(" | brands: %s", strings.Join(sub.FilterBrands, ", "))
		}
		msg.WriteString(fmt.Sprintf("  • <#%s> — %s (radius: %d km%s)\n", sub.ChannelID, sub.City, sub.RadiusKm, brandInfo))
	}
	msg.WriteString("\n")
}

func appendMemExpressListSection(msg *strings.Builder, subs []models.Subscription) {
	if len(subs) == 0 {
		return
	}
	msg.WriteString("**Memory Express:**\n")
	for _, sub := range subs {
		storeName := memoryexpress.StoreName(sub.StoreCode)
		msg.WriteString(fmt.Sprintf("  • <#%s> — %s (%s)\n", sub.ChannelID, storeName, dealTypeLabel(sub.DealType)))
	}
	msg.WriteString("\n")
}

func appendBestBuyListSection(msg *strings.Builder, subs []models.Subscription) {
	if len(subs) == 0 {
		return
	}
	msg.WriteString("**Best Buy:**\n")
	for _, sub := range subs {
		msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
	}
	msg.WriteString("\n")
}

func appendCoreListSection(msg *strings.Builder, subs []models.Subscription) {
	if len(subs) == 0 {
		return
	}
	msg.WriteString("**Core:**\n")
	for _, sub := range subs {
		msg.WriteString(fmt.Sprintf("  • <#%s> — %s\n", sub.ChannelID, dealTypeLabel(sub.DealType)))
	}
	msg.WriteString("\n")
}

func hasAnySubscription(groups ...[]models.Subscription) bool {
	for _, group := range groups {
		if len(group) > 0 {
			return true
		}
	}
	return false
}

// handleDealsRemove handles /deals remove type:<rfd|ebay|facebook>
func (h *Handler) handleDealsRemove(w http.ResponseWriter, req interactionRequest, options []interactionOption) {
	removeType, _ := optionString(options, "type")
	if removeType == "" {
		h.respondPrivateMessage(w, "Please specify the subscription type to remove.")
		return
	}
	if !dealtypes.ValidSubscriptionType(removeType, h.facebookEnabled, h.hardwareSwapEnabled) {
		h.respondPrivateMessage(w, "Invalid or disabled subscription type.")
		return
	}

	spec, ok := h.subscriptionRemoveSpec(removeType)
	if !ok {
		h.respondPrivateMessage(w, "Invalid subscription type.")
		return
	}

	ctx, cancel := storeContext()
	defer cancel()

	subs, err := spec.List(h, ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get subscriptions", "guild", req.GuildID, "type", removeType, "error", err)
		h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
		return
	}
	if len(subs) == 0 {
		h.respondPrivateMessage(w, spec.NoActiveMessage)
		return
	}

	respondRemoveChoices(w, spec.Prompt, spec.BuildButtons(subs))
}

// handleDealsList handles /deals list
func (h *Handler) handleDealsList(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := storeContext()
	defer cancel()

	subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get subscriptions", "guild", req.GuildID, "error", err)
		h.respondPrivateMessage(w, "Failed to retrieve subscriptions due to an internal error.")
		return
	}
	rfdSubs, ebaySubs, coreSubs, onEveryCornerSubs, cruxSubs := splitDealSubscriptions(subs)

	fbSubs := h.facebookSubscriptionsForList(ctx, req.GuildID)
	meSubs := h.memExpressSubscriptionsForList(ctx, req.GuildID)
	bbSubs := h.bestBuySubscriptionsForList(ctx, req.GuildID)

	if !hasAnySubscription(rfdSubs, ebaySubs, fbSubs, meSubs, bbSubs, coreSubs, onEveryCornerSubs, cruxSubs) {
		h.respondPrivateMessage(w, "No active deal subscriptions for this server.")
		return
	}

	var msg strings.Builder
	msg.WriteString("📋 **Active Deal Subscriptions**\n\n")
	appendDealListSection(&msg, "RFD", rfdSubs)
	appendDealListSection(&msg, "eBay", ebaySubs)
	appendFacebookListSection(&msg, fbSubs)
	appendMemExpressListSection(&msg, meSubs)
	appendBestBuyListSection(&msg, bbSubs)
	appendCoreListSection(&msg, coreSubs)
	appendDealListSection(&msg, "OnEveryCorner", onEveryCornerSubs)
	appendDealListSection(&msg, "Crux Investor", cruxSubs)

	h.respondPrivateMessage(w, msg.String())
}

func (h *Handler) coreSubscriptionsForList(ctx context.Context, guildID string) []models.Subscription {
	subs, err := h.store.GetCoreSubscriptionsByGuild(ctx, guildID)
	if err != nil {
		slog.Error("Failed to get Core subscriptions", "guild", guildID, "error", err)
		return nil
	}
	return subs
}

func (h *Handler) facebookSubscriptionsForList(ctx context.Context, guildID string) []models.Subscription {
	if !h.facebookEnabled {
		return nil
	}
	subs, err := h.store.GetFacebookSubscriptionsByGuild(ctx, guildID)
	if err != nil {
		slog.Error("Failed to get Facebook subscriptions", "guild", guildID, "error", err)
		return nil
	}
	return subs
}

func (h *Handler) memExpressSubscriptionsForList(ctx context.Context, guildID string) []models.Subscription {
	subs, err := h.store.GetMemExpressSubscriptionsByGuild(ctx, guildID)
	if err != nil {
		slog.Error("Failed to get Memory Express subscriptions", "guild", guildID, "error", err)
		return nil
	}
	return subs
}

func (h *Handler) bestBuySubscriptionsForList(ctx context.Context, guildID string) []models.Subscription {
	subs, err := h.store.GetBestBuySubscriptionsByGuild(ctx, guildID)
	if err != nil {
		slog.Error("Failed to get Best Buy subscriptions", "guild", guildID, "error", err)
		return nil
	}
	return subs
}

// handleAutocomplete handles Discord autocomplete interactions (type 4).
func (h *Handler) handleAutocomplete(w http.ResponseWriter, req interactionRequest) {
	if req.Data == nil || len(req.Data.Options) == 0 {
		writeAutocompleteChoices(w, nil)
		return
	}

	subCommand := req.Data.Options[0]
	switch {
	case subCommand.Name == "setup-facebook" && h.facebookEnabled:
		writeAutocompleteChoices(w, facebookCityChoices(focusedOptionString(subCommand.Options, "city")))
	case subCommand.Name == "setup-memoryexpress":
		writeAutocompleteChoices(w, memExpressStoreChoices(focusedOptionString(subCommand.Options, "store")))
	default:
		writeAutocompleteChoices(w, nil)
	}
}

func focusedOptionString(options []interactionOption, name string) string {
	for _, opt := range options {
		if opt.Name != name || !opt.Focused {
			continue
		}
		if value, ok := opt.Value.(string); ok {
			return value
		}
	}
	return ""
}

func facebookCityChoices(query string) []autocompleteChoice {
	cities := facebook.FilterCities(query)
	choices := make([]autocompleteChoice, 0, min(len(cities), 25))
	for _, city := range cities {
		choices = append(choices, autocompleteChoice{Name: city, Value: city})
		if len(choices) >= 25 {
			break
		}
	}
	return choices
}

func memExpressStoreChoices(query string) []autocompleteChoice {
	stores := memoryexpress.MatchingStores(query)
	choices := make([]autocompleteChoice, 0, min(len(stores), 25))
	for _, store := range stores {
		choices = append(choices, autocompleteChoice{Name: store.Name, Value: store.Code})
		if len(choices) >= 25 {
			break
		}
	}
	return choices
}

func writeAutocompleteChoices(w http.ResponseWriter, choices []autocompleteChoice) {
	if choices == nil {
		choices = []autocompleteChoice{}
	}
	writeJSON(w, autocompleteResponse{
		Type: InteractionResponseTypeAutocompleteResult,
		Data: autocompleteResponseData{Choices: choices},
	})
}

func (h *Handler) handleRemoveCommand(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := storeContext()
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

	customID := req.Data.CustomID
	if strings.HasPrefix(customID, "hw_") {
		if !h.hardwareSwapEnabled {
			h.respondError(w, "HardwareSwap features are disabled.")
			return
		}
		h.handleHWComponent(w, req)
		return
	}

	if customID == "approve_core_rules" {
		h.handleApproveCoreRules(w, req)
		return
	}
	if customID == "reject_core_rules" {
		h.handleRejectCoreRules(w, req)
		return
	}

	action, ok := parseRemoveAction(customID)
	if !ok {
		h.respondError(w, "Unknown button clicked.")
		return
	}
	h.handleRemoveComponent(w, req, action)
}

func (h *Handler) handleRemoveComponent(w http.ResponseWriter, req interactionRequest, action removeAction) {
	ctx, cancel := storeContext()
	defer cancel()

	switch action.Kind {
	case "facebook":
		h.handleFacebookRemoveComponent(w, ctx, req, action)
	case "bestbuy":
		h.handleBestBuyRemoveComponent(w, ctx, req, action)
	case "memoryexpress":
		h.handleMemExpressRemoveComponent(w, ctx, req, action)
	default:
		h.handleDealRemoveComponent(w, ctx, req, action)
	}
}

func (h *Handler) handleFacebookRemoveComponent(w http.ResponseWriter, ctx context.Context, req interactionRequest, action removeAction) {
	if !h.facebookEnabled {
		h.respondError(w, "Facebook Marketplace features are disabled.")
		return
	}
	if action.Value == "" {
		h.respondError(w, "Invalid Facebook removal data.")
		return
	}
	if err := h.store.RemoveFacebookSubscription(ctx, req.GuildID, action.ChannelID, action.Value); err != nil {
		slog.Error("Failed to remove Facebook subscription", "guild", req.GuildID, "channel", action.ChannelID, "city", action.Value, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}
	writeUpdateMessage(w, fmt.Sprintf("🗑️ Facebook Marketplace subscription for **%s** has been removed from <#%s>.", action.Value, action.ChannelID), []discordComponent{})
}

func (h *Handler) handleBestBuyRemoveComponent(w http.ResponseWriter, ctx context.Context, req interactionRequest, action removeAction) {
	if err := h.store.RemoveBestBuySubscription(ctx, req.GuildID, action.ChannelID); err != nil {
		slog.Error("Failed to remove Best Buy subscription", "guild", req.GuildID, "channel", action.ChannelID, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}
	writeUpdateMessage(w, fmt.Sprintf("🗑️ Best Buy subscription has been removed from <#%s>.", action.ChannelID), []discordComponent{})
}

func (h *Handler) handleMemExpressRemoveComponent(w http.ResponseWriter, ctx context.Context, req interactionRequest, action removeAction) {
	if action.Value == "" {
		h.respondError(w, "Invalid Memory Express removal data.")
		return
	}
	if err := h.store.RemoveMemExpressSubscription(ctx, req.GuildID, action.ChannelID, action.Value); err != nil {
		slog.Error("Failed to remove Memory Express subscription", "guild", req.GuildID, "channel", action.ChannelID, "store", action.Value, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}
	storeName := memoryexpress.StoreName(action.Value)
	writeUpdateMessage(w, fmt.Sprintf("🗑️ Memory Express subscription for **%s** has been removed from <#%s>.", storeName, action.ChannelID), []discordComponent{})
}

func (h *Handler) handleDealRemoveComponent(w http.ResponseWriter, ctx context.Context, req interactionRequest, action removeAction) {
	dealType := action.Value
	if dealType == "" {
		dealType = "all"
	}
	if err := h.store.RemoveSubscription(ctx, req.GuildID, action.ChannelID, dealType); err != nil {
		slog.Error("Failed to remove subscription", "guild", req.GuildID, "channel", action.ChannelID, "error", err)
		h.respondPrivateMessage(w, "Failed to remove subscription due to an internal error.")
		return
	}

	subs, err := h.store.GetSubscriptionsByGuild(ctx, req.GuildID)
	if err != nil {
		slog.Error("Failed to get remaining subscriptions for guild", "guild", req.GuildID, "error", err)
		writeUpdateMessage(w, fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>.", dealTypeLabel(dealType), action.ChannelID), []discordComponent{})
		return
	}
	if len(subs) == 0 {
		writeUpdateMessage(w, "🗑️ All channels removed, there are no active subscriptions for this server.", []discordComponent{})
		return
	}

	components := buildRemoveButtons(subs)
	writeUpdateMessage(w, fmt.Sprintf("🗑️ RFD Bot %s has been removed from <#%s>. Here are the remaining active deal channels:", dealTypeLabel(dealType), action.ChannelID), components)
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

	ctx, cancel := storeContext()
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

	ctx, cancel := storeContext()
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

// handleCoreSuggestRules handles /deals suggest-rules channel command.
func (h *Handler) handleCoreSuggestRules(w http.ResponseWriter, req interactionRequest) {
	if h.aiClient == nil {
		h.respondPrivateMessage(w, "AI client is not configured on this bot.")
		return
	}

	// 1. Write deferred response immediately (type 5)
	writeJSON(w, map[string]interface{}{
		"type": InteractionResponseTypeDeferredChannelMessage,
		"data": map[string]interface{}{
			"flags": MessageFlagEphemeral,
		},
	})

	// 2. Process asynchronously
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		// Get recent raw notifications
		notifs, err := h.store.GetRecentCoreRawNotifications(ctx, 7*24*time.Hour)
		if err != nil {
			slog.Error("Failed to fetch raw notifications for rule suggestion", "error", err)
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "❌ Failed to fetch raw notifications from the database.",
			})
			return
		}

		if len(notifs) == 0 {
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "ℹ️ No raw notifications have been recorded in the last 7 days. Please ingest some deals first.",
			})
			return
		}

		// Get existing rules
		existingRules, err := h.store.GetCoreRules(ctx)
		if err != nil {
			slog.Error("Failed to fetch existing core rules", "error", err)
		}

		// Format notifications for Gemini
		var sampleText strings.Builder
		for i, n := range notifs {
			if i >= 100 { // Limit to 100 recent notifications to keep prompt compact and precise
				break
			}
			sampleText.WriteString(fmt.Sprintf("- Title: %q | Message: %q\n", n.Title, n.Message))
		}

		// Format existing rules for Gemini
		var rulesJson []byte
		if len(existingRules) > 0 {
			rulesJson, _ = json.Marshal(existingRules)
		} else {
			rulesJson = []byte("[]")
		}

		// Prepare prompt
		var prompt strings.Builder
		prompt.WriteString("You are an expert regex rules architect for a deal alert bot.\n")
		prompt.WriteString("Your task is to analyze raw Discord notifications and suggest search-and-replace regular expressions to clean up product names for better grouping (segmentation).\n\n")
		prompt.WriteString("Goals:\n")
		prompt.WriteString("1. Group identical products together by stripping redundant retailer names, fluff words (e.g. 'sale', 'limited stock', 'deal of the day', 'lava hot', 'ymmv'), emojis, leading/trailing dashes, or extra spaces.\n")
		prompt.WriteString("2. CRITICAL: Keep distinct sub-products SEPARATE. Do NOT strip memory configurations (e.g., '16g', '8g', '16gb', '8gb', '12gb', '24gb', '32gb', '1tb') or specific edition names. For example, '5060ti 8g' and '5060ti 16g' are different products; do not strip their memory sizes. However, you should normalize '8g' or '8g ' to '8gb' and '16g' to '16gb' consistently using regex patterns.\n\n")
		prompt.WriteString("Here are the existing active rules we are already applying:\n")
		prompt.WriteString(string(rulesJson) + "\n\n")
		prompt.WriteString("Here are recent raw notifications (newest first):\n")
		prompt.WriteString(sampleText.String() + "\n\n")
		prompt.WriteString("Respond with a JSON array containing proposed rules. Each rule should be an object with \"pattern\" (regexp compilation pattern, case-insensitive) and \"replace\" (replacement string). Output ONLY the JSON array inside a standard JSON block (no markdown blocks or extra explanation). Example response format:\n")
		prompt.WriteString("[{\"pattern\": \"(?i)\\\\s*-\\\\s*deal of the day\", \"replace\": \"\"}, {\"pattern\": \"(?i)\\\\b(\\\\d+)g\\\\b\", \"replace\": \"$1gb\"}]\n")

		// Call Gemini with Pro model
		model := "gemini-2.5-pro"
		for _, m := range h.fallbackModels {
			if strings.Contains(m, "pro") {
				model = m
				break
			}
		}

		config := &genai.GenerateContentConfig{
			Temperature: genai.Ptr[float32](0.1),
		}
		respText, _, _, err := h.aiClient.GenerateContentWithModel(ctx, model, prompt.String(), config)
		if err != nil {
			slog.Error("Gemini call for rule suggestion failed", "error", err)
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "❌ AI text generation failed. Please try again later.",
			})
			return
		}

		// Parse rules
		cleanedJson := stripMarkdownCodeFences(respText)
		var suggestedRules []models.CoreRule
		if err := json.Unmarshal([]byte(cleanedJson), &suggestedRules); err != nil {
			slog.Error("Failed to parse suggested rules JSON", "raw", respText, "error", err)
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "❌ Failed to parse suggested rules returned by the AI. Raw response:\n```\n" + respText + "\n```",
			})
			return
		}
		if err := core.ValidateRules(suggestedRules); err != nil {
			slog.Error("Suggested core rules contain invalid regex patterns", "error", err)
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "❌ Suggested rules included invalid regex patterns:\n```\n" + err.Error() + "\n```",
			})
			return
		}

		// Store pending rules
		if err := h.store.SavePendingCoreRules(ctx, suggestedRules); err != nil {
			slog.Error("Failed to save pending core rules", "error", err)
			_ = h.sendDiscordFollowup(req.Token, map[string]any{
				"content": "❌ Failed to save pending rules in the database.",
			})
			return
		}

		// Dry run comparison (sample a few changes)
		var diffText strings.Builder
		diffText.WriteString("**Proposed Regex Rules:**\n")
		for _, r := range suggestedRules {
			diffText.WriteString(fmt.Sprintf("- Pattern: `%s` ➡️ Replace: `%q`\n", r.Pattern, r.Replace))
		}
		diffText.WriteString("\n**Sample Dry-Run Normalization Changes:**\n")

		changedCount := 0
		for _, n := range notifs {
			productName, _, _, _, isDeal := core.ParseNotificationText(nil, n.Message)
			if !isDeal {
				continue
			}

			// Apply old active rules
			oldNorm := core.NormalizeProductName(productName, existingRules, "")
			// Apply new suggested rules
			newNorm := core.NormalizeProductName(productName, suggestedRules, "")

			if oldNorm != newNorm && changedCount < 5 {
				diffText.WriteString(fmt.Sprintf("• Original: `%s`\n  Before: `%s`\n  After:  `%s`\n\n", productName, oldNorm, newNorm))
				changedCount++
			}
		}

		if changedCount == 0 {
			diffText.WriteString("*(No changes detected on the sampled notifications. Rules might be cleanups that didn't match the recent sample, or redundant.)*\n")
		}

		// Format embed & buttons
		embed := map[string]any{
			"title":       "🤖 Proposed Core Product Segmentation Rules",
			"description": diffText.String(),
			"color":       0x5865F2, // Discord Blurple
			"timestamp":   time.Now().Format(time.RFC3339),
		}

		components := []map[string]any{
			{
				"type": ComponentTypeActionRow,
				"components": []map[string]any{
					{
						"type":      ComponentTypeButton,
						"style":     ButtonStylePrimary,
						"label":     "Approve & Apply",
						"custom_id": "approve_core_rules",
					},
					{
						"type":      ComponentTypeButton,
						"style":     ButtonStyleDanger,
						"label":     "Reject & Discard",
						"custom_id": "reject_core_rules",
					},
				},
			},
		}

		err = h.sendDiscordFollowup(req.Token, map[string]any{
			"embeds":     []any{embed},
			"components": components,
		})
		if err != nil {
			slog.Error("Failed to send rule proposal embed to Discord", "error", err)
		}
	}()
}

// handleApproveCoreRules applies pending rules to active configuration.
func (h *Handler) handleApproveCoreRules(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := storeContext()
	defer cancel()

	// 1. Retrieve pending rules
	pending, err := h.store.GetPendingCoreRules(ctx)
	if err != nil {
		slog.Error("Failed to fetch pending core rules", "error", err)
		h.respondPrivateMessage(w, "❌ Failed to retrieve pending rules from the database.")
		return
	}

	if len(pending) == 0 {
		h.respondPrivateMessage(w, "ℹ️ No pending rules found to approve.")
		return
	}
	if err := core.ValidateRules(pending); err != nil {
		slog.Error("Pending core rules contain invalid regex patterns", "error", err)
		h.respondPrivateMessage(w, "❌ Pending rules include invalid regex patterns and were not applied.")
		return
	}

	// 2. Save to active rules
	if err := h.store.SaveCoreRules(ctx, pending); err != nil {
		slog.Error("Failed to save approved core rules", "error", err)
		h.respondPrivateMessage(w, "❌ Failed to save approved rules.")
		return
	}

	// 3. Delete pending rules
	if err := h.store.DeletePendingCoreRules(ctx); err != nil {
		slog.Error("Failed to delete pending core rules after approval", "error", err)
	}

	// 4. Update the interaction response message to show success
	writeUpdateMessage(w, "✅ **Product segmentation rules approved and applied successfully!**", []discordComponent{})
}

// handleRejectCoreRules discards proposed rules.
func (h *Handler) handleRejectCoreRules(w http.ResponseWriter, req interactionRequest) {
	ctx, cancel := storeContext()
	defer cancel()

	// Delete pending rules
	if err := h.store.DeletePendingCoreRules(ctx); err != nil {
		slog.Error("Failed to delete pending core rules on rejection", "error", err)
	}

	// Update the interaction response message to show rejection
	writeUpdateMessage(w, "❌ **Proposed rules discarded.**", []discordComponent{})
}

// sendDiscordFollowup patches the deferred response in Discord.
func (h *Handler) sendDiscordFollowup(token string, body any) error {
	url := fmt.Sprintf("https://discord.com/api/v10/webhooks/%s/%s/messages/@original", h.discordAppID, token)
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PATCH", url, bytes.NewBuffer(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+h.discordToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("discord API error %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func stripMarkdownCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimSuffix(s, "```")
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start != -1 && end != -1 && end > start {
		return s[start : end+1]
	}
	return s
}
