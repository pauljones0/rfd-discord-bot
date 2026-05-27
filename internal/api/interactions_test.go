package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// mockStore implements the Store interface for testing
type mockStore struct {
	subscriptions         []models.Subscription
	facebookSubscriptions []models.Subscription
	err                   error
	removeErr             error
}

func (m *mockStore) SaveSubscription(ctx context.Context, sub models.Subscription) error {
	if m.err != nil {
		return m.err
	}
	m.subscriptions = append(m.subscriptions, sub)
	return nil
}

func (m *mockStore) RemoveSubscription(ctx context.Context, guildID, channelID, dealType string) error {
	if m.removeErr != nil {
		return m.removeErr
	}

	var remaining []models.Subscription
	for _, sub := range m.subscriptions {
		if sub.GuildID == guildID && sub.ChannelID == channelID && (dealType == "" || sub.DealType == dealType) {
			continue
		}
		remaining = append(remaining, sub)
	}
	m.subscriptions = remaining
	return nil
}

func (m *mockStore) GetSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	var match []models.Subscription
	for _, sub := range m.subscriptions {
		if sub.GuildID == guildID {
			match = append(match, sub)
		}
	}
	return match, nil
}

func (m *mockStore) GetSubscription(ctx context.Context, guildID, channelID string) (*models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	for _, sub := range m.subscriptions {
		if sub.GuildID == guildID && sub.ChannelID == channelID {
			return &sub, nil
		}
	}
	return nil, nil
}

func (m *mockStore) SaveFacebookSubscription(ctx context.Context, sub models.Subscription) error {
	if m.err != nil {
		return m.err
	}
	m.facebookSubscriptions = append(m.facebookSubscriptions, sub)
	return nil
}

func (m *mockStore) RemoveFacebookSubscription(ctx context.Context, guildID, channelID, city string) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	var remaining []models.Subscription
	for _, sub := range m.facebookSubscriptions {
		if sub.GuildID == guildID && sub.ChannelID == channelID && sub.City == city {
			continue
		}
		remaining = append(remaining, sub)
	}
	m.facebookSubscriptions = remaining
	return nil
}

func (m *mockStore) GetFacebookSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	var match []models.Subscription
	for _, sub := range m.facebookSubscriptions {
		if sub.GuildID == guildID {
			match = append(match, sub)
		}
	}
	return match, nil
}

func (m *mockStore) SaveMemExpressSubscription(ctx context.Context, sub models.Subscription) error {
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockStore) RemoveMemExpressSubscription(ctx context.Context, guildID, channelID, storeCode string) error {
	if m.err != nil {
		return m.err
	}
	return nil
}

func (m *mockStore) GetMemExpressSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}

func (m *mockStore) SaveBestBuySubscription(ctx context.Context, sub models.Subscription) error {
	if m.err != nil {
		return m.err
	}
	m.subscriptions = append(m.subscriptions, sub)
	return nil
}

func (m *mockStore) RemoveBestBuySubscription(ctx context.Context, guildID, channelID string) error {
	if m.removeErr != nil {
		return m.removeErr
	}
	return nil
}

func (m *mockStore) GetBestBuySubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}

func (m *mockStore) GetCoreSubscriptionsByGuild(ctx context.Context, guildID string) ([]models.Subscription, error) {
	if m.err != nil {
		return nil, m.err
	}
	// Return subscriptions of type core
	var out []models.Subscription
	for _, sub := range m.subscriptions {
		if sub.IsCore() && sub.GuildID == guildID {
			out = append(out, sub)
		}
	}
	return out, nil
}

func (m *mockStore) GetRecentCoreRawNotifications(ctx context.Context, duration time.Duration) ([]models.CoreRawNotification, error) {
	return nil, nil
}

func (m *mockStore) GetCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	return nil, nil
}

func (m *mockStore) SaveCoreRules(ctx context.Context, rules []models.CoreRule) error {
	return nil
}

func (m *mockStore) GetPendingCoreRules(ctx context.Context) ([]models.CoreRule, error) {
	return nil, nil
}

func (m *mockStore) SavePendingCoreRules(ctx context.Context, rules []models.CoreRule) error {
	return nil
}

func (m *mockStore) DeletePendingCoreRules(ctx context.Context) error {
	return nil
}

func TestHandleRemoveCommand(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "rfd_warm_hot"},
			{GuildID: "guild1", ChannelID: "chan2", DealType: "rfd_hot"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
	}

	w := httptest.NewRecorder()
	handler.handleRemoveCommand(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if res.Type != InteractionResponseTypeChannelMessageWithSource {
		t.Errorf("expected response type %d, got %d", InteractionResponseTypeChannelMessageWithSource, res.Type)
	}

	if res.Data.Components == nil || len(*res.Data.Components) != 2 {
		compLen := 0
		if res.Data.Components != nil {
			compLen = len(*res.Data.Components)
		}
		t.Fatalf("expected 2 components, got %d", compLen)
	}

	// Verify the CustomID format has dealType
	comp1 := (*res.Data.Components)[0].Components[0]
	if comp1.CustomID != "remove_sub::chan1::rfd_warm_hot" {
		t.Errorf("expected CustomID 'remove_sub::chan1::rfd_warm_hot', got %s", comp1.CustomID)
	}
	if comp1.Label != "Delete RFD warm + hot deals from #deals" {
		t.Errorf("expected Label 'Delete RFD warm + hot deals from #deals', got %s", comp1.Label)
	}

	// Verify chan2 (no name) uses fallback
	comp2 := (*res.Data.Components)[1].Components[0]
	if comp2.Label != "Delete Channel (RFD hot deals)" {
		t.Errorf("expected Label 'Delete Channel (RFD hot deals)', got %s", comp2.Label)
	}
}

func TestHandleComponent_Remaining(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "rfd_warm_hot"},
			{GuildID: "guild1", ChannelID: "chan2", ChannelName: "tech", DealType: "rfd_hot"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub::chan1::rfd_warm_hot",
		},
	}

	w := httptest.NewRecorder()
	handler.handleComponent(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if res.Type != InteractionResponseTypeUpdateMessage {
		t.Errorf("expected response type %d, got %d", InteractionResponseTypeUpdateMessage, res.Type)
	}

	expectedPrefix := "🗑️ RFD Bot RFD warm + hot deals has been removed from <#chan1>"
	if !strings.HasPrefix(res.Data.Content, expectedPrefix) {
		t.Errorf("expected message to start with %q, but got %q", expectedPrefix, res.Data.Content)
	}

	if res.Data.Components == nil || len(*res.Data.Components) != 1 {
		compLen := 0
		if res.Data.Components != nil {
			compLen = len(*res.Data.Components)
		}
		t.Errorf("expected 1 remaining component button for chan2, got %d", compLen)
	} else {
		label := (*res.Data.Components)[0].Components[0].Label
		if label != "Delete RFD hot deals from #tech" {
			t.Errorf("expected label 'Delete RFD hot deals from #tech', got %s", label)
		}
	}
}

func TestHandleComponent_AllRemoved(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", DealType: "rfd_hot"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub::chan1::rfd_hot",
		},
	}

	w := httptest.NewRecorder()
	handler.handleComponent(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if res.Type != InteractionResponseTypeUpdateMessage {
		t.Errorf("expected response type %d, got %d", InteractionResponseTypeUpdateMessage, res.Type)
	}

	expectedContent := "🗑️ All channels removed, there are no active subscriptions for this server."
	if res.Data.Content != expectedContent {
		t.Errorf("expected message to be %q, but got %q", expectedContent, res.Data.Content)
	}

	if res.Data.Components == nil || len(*res.Data.Components) != 0 {
		compLen := 0
		if res.Data.Components != nil {
			compLen = len(*res.Data.Components)
		}
		t.Errorf("expected 0 components (cleared buttons), got %d", compLen)
	}
}

func TestBuildRemoveButtons_AllowsSameChannelDifferentFeeds(t *testing.T) {
	subs := []models.Subscription{
		{ChannelID: "chan1", ChannelName: "deals", DealType: "rfd_warm_hot", SubscriptionType: "rfd"},
		{ChannelID: "chan1", ChannelName: "deals", DealType: "ebay_ca_price_drop", SubscriptionType: "ebay"},
	}

	components := buildRemoveButtons(subs)
	if len(components) != 2 {
		t.Fatalf("expected 2 buttons for same channel with different feeds, got %d", len(components))
	}
	if components[1].Components[0].Label != "Delete eBay Canada price drops from #deals" {
		t.Fatalf("unexpected eBay button label: %q", components[1].Components[0].Label)
	}
	if components[1].Components[0].CustomID != "remove_sub::chan1::ebay_ca_price_drop" {
		t.Fatalf("unexpected eBay button custom id: %q", components[1].Components[0].CustomID)
	}
}

func TestHandleSetupEbay_AllowsCanadaAndUSSameChannel(t *testing.T) {
	store := &mockStore{}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			Resolved: &interactionResolved{
				Channels: map[string]struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Type int    `json:"type"`
				}{
					"chan1": {ID: "chan1", Name: "deals"},
				},
			},
		},
	}

	w := httptest.NewRecorder()
	handler.handleSetupEbay(w, reqPayload, []interactionOption{
		{Name: "channel", Value: "chan1"},
		{Name: "filter", Value: "ebay_ca_price_drop"},
	})
	if !strings.Contains(w.Body.String(), "eBay Canada price drops") {
		t.Fatalf("expected Canada setup response, got %q", w.Body.String())
	}

	w = httptest.NewRecorder()
	handler.handleSetupEbay(w, reqPayload, []interactionOption{
		{Name: "channel", Value: "chan1"},
		{Name: "filter", Value: "ebay_us_price_drop"},
	})
	if !strings.Contains(w.Body.String(), "eBay US price drops") {
		t.Fatalf("expected US setup response, got %q", w.Body.String())
	}

	if len(store.subscriptions) != 2 {
		t.Fatalf("expected 2 eBay subscriptions in the same channel, got %d", len(store.subscriptions))
	}
}

func TestHandleCommand_RejectsLegacyRfdBotSetup(t *testing.T) {
	handler := &Handler{store: &mockStore{}}
	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data:    &interactionData{Name: "rfd-bot-setup"},
	}

	w := httptest.NewRecorder()
	handler.handleCommand(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if !strings.Contains(res.Data.Content, "Unknown command") {
		t.Fatalf("expected unknown command response, got %q", res.Data.Content)
	}
}

func TestHandleDealsCommandRejectsDisabledFacebookSetup(t *testing.T) {
	handler := &Handler{store: &mockStore{}}
	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{Options: []interactionOption{{
			Name: "setup-facebook",
		}}},
	}

	w := httptest.NewRecorder()
	handler.handleDealsCommand(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if !strings.Contains(res.Data.Content, "Facebook Marketplace features are currently disabled") {
		t.Fatalf("expected disabled Facebook response, got %q", res.Data.Content)
	}
}

func TestHandleCommandRejectsDisabledHardwareSwap(t *testing.T) {
	handler := &Handler{store: &mockStore{}}
	reqPayload := interactionRequest{Data: &interactionData{Name: "hw-help"}}

	w := httptest.NewRecorder()
	handler.handleCommand(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}
	if !strings.Contains(res.Data.Content, "HardwareSwap features are disabled") {
		t.Fatalf("expected disabled HardwareSwap response, got %q", res.Data.Content)
	}
}

func TestNewHandlerRequiresDiscordPublicKeyByDefault(t *testing.T) {
	_, err := NewHandler(&config.Config{}, &mockStore{}, nil, nil)
	if err == nil {
		t.Fatal("NewHandler() error = nil, want missing public key error")
	}
	if !strings.Contains(err.Error(), "DISCORD_PUBLIC_KEY") {
		t.Fatalf("NewHandler() error = %q, want DISCORD_PUBLIC_KEY context", err)
	}
}

func TestNewHandlerAllowsUnsignedWhenExplicitlyEnabled(t *testing.T) {
	handler, err := NewHandler(&config.Config{AllowUnsignedDiscordInteractions: true}, &mockStore{}, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler() returned unexpected error: %v", err)
	}
	if handler == nil {
		t.Fatal("NewHandler() returned nil handler")
	}
}

func TestNewHandlerRejectsInvalidPublicKey(t *testing.T) {
	_, err := NewHandler(&config.Config{DiscordPublicKey: "not-hex"}, &mockStore{}, nil, nil)
	if err == nil {
		t.Fatal("NewHandler() error = nil, want invalid public key error")
	}
}

func TestNewHandlerAcceptsValidPublicKey(t *testing.T) {
	key := strings.Repeat("00", 32)
	handler, err := NewHandler(&config.Config{DiscordPublicKey: key}, &mockStore{}, nil, nil)
	if err != nil {
		t.Fatalf("NewHandler() returned unexpected error: %v", err)
	}
	if handler == nil || len(handler.pubKey) != 32 {
		t.Fatalf("NewHandler() pubKey length = %d, want 32", len(handler.pubKey))
	}
}

func TestHandleChannelFilterSetup_SavesRFDSubscription(t *testing.T) {
	store := &mockStore{}
	handler := &Handler{store: store}
	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{Resolved: &interactionResolved{Channels: map[string]struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Type int    `json:"type"`
		}{"chan1": {ID: "chan1", Name: "deals"}}}},
	}

	w := httptest.NewRecorder()
	handler.handleSetupRFD(w, reqPayload, []interactionOption{{Name: "channel", Value: "chan1"}, {Name: "filter", Value: "rfd_warm_hot"}})

	if len(store.subscriptions) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(store.subscriptions))
	}
	sub := store.subscriptions[0]
	if sub.SubscriptionType != "rfd" || sub.ChannelName != "deals" || sub.DealType != "rfd_warm_hot" {
		t.Fatalf("unexpected subscription: %#v", sub)
	}
}

func TestHandleChannelFilterSetup_SavesBestBuySubscription(t *testing.T) {
	store := &mockStore{}
	handler := &Handler{store: store}
	reqPayload := interactionRequest{GuildID: "guild1", Data: &interactionData{}}

	w := httptest.NewRecorder()
	handler.handleSetupBestBuy(w, reqPayload, []interactionOption{{Name: "channel", Value: "chan1"}, {Name: "filter", Value: "bb_compute"}})

	if len(store.subscriptions) != 1 {
		t.Fatalf("subscriptions = %d, want 1", len(store.subscriptions))
	}
	sub := store.subscriptions[0]
	if sub.SubscriptionType != "bestbuy" || sub.ChannelID != "chan1" || sub.DealType != "bb_compute" {
		t.Fatalf("unexpected subscription: %#v", sub)
	}
}

func TestParseRemoveAction(t *testing.T) {
	tests := []struct {
		customID string
		want     removeAction
		ok       bool
	}{
		{"remove_sub::chan1::rfd_hot", removeAction{Kind: "sub", ChannelID: "chan1", Value: "rfd_hot"}, true},
		{"remove_fb::chan2::Saskatoon", removeAction{Kind: "facebook", ChannelID: "chan2", Value: "Saskatoon"}, true},
		{"remove_bb::chan3", removeAction{Kind: "bestbuy", ChannelID: "chan3"}, true},
		{"remove_me::chan4::SKST", removeAction{Kind: "memoryexpress", ChannelID: "chan4", Value: "SKST"}, true},
		{"unknown::chan", removeAction{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.customID, func(t *testing.T) {
			got, ok := parseRemoveAction(tt.customID)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("parseRemoveAction() = %#v, %v; want %#v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
