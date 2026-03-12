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
	subscriptions []models.Subscription
	err           error
	removeErr     error
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

func TestHandleRemoveCommand(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "warm_hot_all"},
			{GuildID: "guild1", ChannelID: "chan2", DealType: "hot"},
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
	if comp1.CustomID != "remove_sub_chan1_warm_hot_all" {
		t.Errorf("expected CustomID 'remove_sub_chan1_warm_hot_all', got %s", comp1.CustomID)
	}
	if comp1.Label != "Delete warm_hot_all from #deals" {
		t.Errorf("expected Label 'Delete warm_hot_all from #deals', got %s", comp1.Label)
	}

	// Verify chan2 (no name) uses fallback
	comp2 := (*res.Data.Components)[1].Components[0]
	if comp2.Label != "Delete Channel (hot)" {
		t.Errorf("expected Label 'Delete Channel (hot)', got %s", comp2.Label)
	}
}

func TestHandleComponent_Remaining(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", ChannelName: "deals", DealType: "warm_hot_all"},
			{GuildID: "guild1", ChannelID: "chan2", ChannelName: "tech", DealType: "hot"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub_chan1_warm_hot_all",
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
		if label != "Delete hot from #tech" {
			t.Errorf("expected label 'Delete hot from #tech', got %s", label)
		}
	}
}

func TestHandleComponent_AllRemoved(t *testing.T) {
	store := &mockStore{
		subscriptions: []models.Subscription{
			{GuildID: "guild1", ChannelID: "chan1", DealType: "hot"},
		},
	}
	handler := &Handler{store: store}

	reqPayload := interactionRequest{
		GuildID: "guild1",
		Data: &interactionData{
			CustomID: "remove_sub_chan1_hot",
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
