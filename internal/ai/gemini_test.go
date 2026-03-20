package ai

import (
	"context"
	"fmt"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type mockQuotaStore struct {
	quota *models.GeminiQuotaStatus
	err   error
}

func (m *mockQuotaStore) GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error) {
	return m.quota, m.err
}

func (m *mockQuotaStore) UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error {
	m.quota = &quota
	return m.err
}

func TestCheckDayRollover(t *testing.T) {
	today := getPacificDate()

	tests := []struct {
		name           string
		initialState   *models.GeminiQuotaStatus
		storeError     error
		fallbackModels []string
		expectedModel  string
		expectedDay    string
	}{
		{
			name:           "No existing state, initiates to cheapest",
			initialState:   nil,
			fallbackModels: []string{"gemini-lite", "gemini-pro"},
			expectedModel:  "gemini-lite",
			expectedDay:    today,
		},
		{
			name: "Existing state from today, keeps current model",
			initialState: &models.GeminiQuotaStatus{
				CurrentDay:   today,
				CurrentModel: "gemini-pro",
			},
			fallbackModels: []string{"gemini-lite", "gemini-pro"},
			expectedModel:  "gemini-pro",
			expectedDay:    today,
		},
		{
			name: "Existing state from yesterday, rolls over to cheapest",
			initialState: &models.GeminiQuotaStatus{
				CurrentDay:   "2000-01-01",
				CurrentModel: "gemini-pro",
			},
			fallbackModels: []string{"gemini-lite", "gemini-pro"},
			expectedModel:  "gemini-lite",
			expectedDay:    today,
		},
		{
			name:           "Store error assumes cheapest",
			initialState:   nil,
			storeError:     fmt.Errorf("db error"),
			fallbackModels: []string{"gemini-lite", "gemini-pro"},
			expectedModel:  "gemini-lite",
			expectedDay:    today,
		},
		{
			name: "Stale model not in fallback list resets to cheapest",
			initialState: &models.GeminiQuotaStatus{
				CurrentDay:   today,
				CurrentModel: "gemini-3.1-flash-lite-preview",
			},
			fallbackModels: []string{"gemini-lite", "gemini-pro"},
			expectedModel:  "gemini-lite",
			expectedDay:    today,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockQuotaStore{
				quota: tt.initialState,
				err:   tt.storeError,
			}

			client := &Client{
				store:          store,
				fallbackModels: tt.fallbackModels,
			}

			// Simulated NewClient startup
			client.initQuotaState(context.Background())

			// Check first rollover call directly
			model := client.checkDayRollover(context.Background())
			if model != tt.expectedModel {
				t.Errorf("expected model %q, got %q", tt.expectedModel, model)
			}
			if client.currentDay != tt.expectedDay {
				t.Errorf("expected day %q, got %q", tt.expectedDay, client.currentDay)
			}
		})
	}
}

func TestUpgradeModelTier(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "tier-1",
		},
	}

	client := &Client{
		store:          store,
		fallbackModels: []string{"tier-1", "tier-2", "tier-3"},
		currentDay:     today,
		currentModel:   "tier-1",
	}

	// Upgrade 1
	err := client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error upgrading tier 1->2: %v", err)
	}
	if client.currentModel != "tier-2" {
		t.Errorf("expected model tier-2, got %q", client.currentModel)
	}
	if store.quota.CurrentModel != "tier-2" {
		t.Errorf("expected store model tier-2, got %q", store.quota.CurrentModel)
	}

	// Upgrade 2
	err = client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error upgrading tier 2->3: %v", err)
	}
	if client.currentModel != "tier-3" {
		t.Errorf("expected model tier-3, got %q", client.currentModel)
	}

	// Upgrade 3 (Exhausted)
	err = client.upgradeModelTier(context.Background())
	if err == nil {
		t.Fatalf("expected error when exhausting all tiers, got nil")
	}
	if client.currentModel != "tier-3" { // remains on last attempted tier
		t.Errorf("expected model tier-3, got %q", client.currentModel)
	}
}

func TestUpgradeModelTierStaleModel(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "old-removed-model",
		},
	}

	client := &Client{
		store:          store,
		fallbackModels: []string{"tier-1", "tier-2", "tier-3"},
		currentDay:     today,
		currentModel:   "old-removed-model",
	}

	// Should reset to tier-1 instead of failing
	err := client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected model tier-1, got %q", client.currentModel)
	}
}
