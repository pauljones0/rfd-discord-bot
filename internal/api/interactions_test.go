package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

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

func (m *mockStore) RemoveSubscription(ctx context.Context, guildID, channelID string) error {
	if m.removeErr != nil {
		return m.removeErr
	}

	var remaining []models.Subscription
	for _, sub := range m.subscriptions {
		if sub.GuildID == guildID && sub.ChannelID == channelID {
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

func TestHandleRemoveCommand(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "warm_hot_all"},
			{GuildID: "guild1", ChannelID: "chan2", DealType: "hot_all"},
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
	if comp1.CustomID != "remove_sub::chan1::warm_hot_all" {
		t.Errorf("expected CustomID 'remove_sub::chan1::warm_hot_all', got %s", comp1.CustomID)
	}
	if comp1.Label != "Delete warm_hot_all from #deals" {
		t.Errorf("expected Label 'Delete warm_hot_all from #deals', got %s", comp1.Label)
	}

	// Verify chan2 (no name) uses fallback
	comp2 := (*res.Data.Components)[1].Components[0]
	if comp2.Label != "Delete Channel (hot_all)" {
		t.Errorf("expected Label 'Delete Channel (hot)', got %s", comp2.Label)
	}
}

func TestHandleComponent_Remaining(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "warm_hot_all"},
			{GuildID: "guild1", ChannelID: "chan2", ChannelName: "tech", DealType: "hot_all"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub::chan1::warm_hot_all",
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

	expectedPrefix := "🗑️ RFD Bot warm_hot_all has been removed from <#chan1>"
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
		if label != "Delete hot_all from #tech" {
			t.Errorf("expected label 'Delete hot from #tech', got %s", label)
		}
	}
}

func TestHandleComponent_AllRemoved(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", DealType: "hot_all"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub::chan1::hot_all",
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

func TestHandleSetCommand_Confirmation(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", DealType: "rfd_all", ChannelName: "deals"},
		},
	}
	handler := &Handler{store: store}

	options := []interactionOption{
		{Name: "channel", Value: "chan1"},
		{Name: "type", Value: "rfd_tech"},
	}
	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			Name:    "rfd-bot-setup",
			Options: []interactionOption{{Name: "set", Options: options}},
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
	handler.handleSetCommand(w, reqPayload, options)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if !strings.Contains(res.Data.Content, "already set up") {
		t.Errorf("expected confirmation message, got: %s", res.Data.Content)
	}

	if res.Data.Components == nil || len(*res.Data.Components) != 1 {
		t.Fatalf("expected 1 component (Action Row), got %v", res.Data.Components)
	}

	comps := (*res.Data.Components)[0].Components
	if len(comps) != 2 {
		t.Fatalf("expected 2 buttons, got %d", len(comps))
	}

	if comps[0].Label != "Confirm Update" || !strings.HasPrefix(comps[0].CustomID, "confirm_update::") {
		t.Errorf("Expected Confirm Update button, got %+v", comps[0])
	}
}

func TestHandleComponent_ConfirmUpdate(t *testing.T) {
	store := &mockStore{}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Member: &interactionMember{
			User: struct {
				ID       string `json:"id"`
				Username string `json:"username"`
			}{ID: "user1", Username: "tester"},
		},
		Data: &interactionData{
			CustomID: "confirm_update::chan1::rfd_tech::deals",
		},
	}

	w := httptest.NewRecorder()
	handler.handleComponent(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if !strings.Contains(res.Data.Content, "successfully updated") {
		t.Errorf("expected success message, got: %s", res.Data.Content)
	}

	if len(store.subscriptions) != 1 {
		t.Fatalf("expected 1 subscription saved, got %d", len(store.subscriptions))
	}

	if store.subscriptions[0].DealType != "rfd_tech" || store.subscriptions[0].ChannelName != "deals" {
		t.Errorf("expected subscription tech/deals, got %+v", store.subscriptions[0])
	}
}

func TestHandleComponent_CancelUpdate(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", DealType: "rfd_all"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "confirm_cancel",
		},
	}

	w := httptest.NewRecorder()
	handler.handleComponent(w, reqPayload)

	var res interactionResponse
	if err := json.NewDecoder(w.Body).Decode(&res); err != nil {
		t.Fatalf("failed decoding response: %v", err)
	}

	if !strings.Contains(res.Data.Content, "cancelled") {
		t.Errorf("expected cancel message, got: %s", res.Data.Content)
	}

	if store.subscriptions[0].DealType != "rfd_all" {
		t.Errorf("expected subscription to remain 'rfd_all', got %s", store.subscriptions[0].DealType)
	}
}
