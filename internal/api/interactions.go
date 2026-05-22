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

func splitDealSubscriptions(subs []models.Subscription) ([]models.Subscription, []models.Subscription) {
	var rfdSubs, ebaySubs []models.Subscription
	for _, sub := range subs {
		switch {
		case sub.IsEbay():
			ebaySubs = append(ebaySubs, sub)
		case sub.IsRFD():
			rfdSubs = append(rfdSubs, sub)
		}
	}
	return rfdSubs, ebaySubs
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
	rfdSubs, ebaySubs := splitDealSubscriptions(subs)

	fbSubs := h.facebookSubscriptionsForList(ctx, req.GuildID)
	meSubs := h.memExpressSubscriptionsForList(ctx, req.GuildID)
	bbSubs := h.bestBuySubscriptionsForList(ctx, req.GuildID)

	if !hasAnySubscription(rfdSubs, ebaySubs, fbSubs, meSubs, bbSubs) {
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

	h.respondPrivateMessage(w, msg.String())
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
