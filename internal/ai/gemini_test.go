package ai

import (
	"context"
	"fmt"
	"testing"
	"time"

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
		name              string
		initialState      *models.GeminiQuotaStatus
		storeError        error
		fallbackModels    []string
		expectedModel     string
		expectedDay       string
		expectedExhausted bool
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
		{
			name: "Exhausted state from today is preserved",
			initialState: &models.GeminiQuotaStatus{
				CurrentDay:   today,
				CurrentModel: "gemini-pro",
				AllExhausted: true,
				ExhaustedAt:  time.Now(), // recently exhausted
			},
			fallbackModels:    []string{"gemini-lite", "gemini-pro"},
			expectedModel:     "gemini-pro",
			expectedDay:       today,
			expectedExhausted: true,
		},
		{
			name: "Exhausted state from yesterday is reset on day rollover",
			initialState: &models.GeminiQuotaStatus{
				CurrentDay:   "2000-01-01",
				CurrentModel: "gemini-pro",
				AllExhausted: true,
			},
			fallbackModels:    []string{"gemini-lite", "gemini-pro"},
			expectedModel:     "gemini-lite",
			expectedDay:       today,
			expectedExhausted: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockQuotaStore{
				quota: tt.initialState,
				err:   tt.storeError,
			}

			client := &Client{
				store:           store,
				fallbackModels:  tt.fallbackModels,
				locations:       []string{"us-central1"},
				currentLocation: "us-central1",
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
			if client.allExhausted != tt.expectedExhausted {
				t.Errorf("expected allExhausted %v, got %v", tt.expectedExhausted, client.allExhausted)
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
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2", "tier-3"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-1",
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

	// Upgrade 3 — single region: should exhaust
	err = client.upgradeModelTier(context.Background())
	if err == nil {
		t.Fatalf("expected error when exhausting all tiers, got nil")
	}
	if client.currentModel != "tier-3" { // remains on last attempted tier
		t.Errorf("expected model tier-3, got %q", client.currentModel)
	}
	if !client.allExhausted {
		t.Error("expected allExhausted to be true after exhausting all tiers")
	}
	if !store.quota.AllExhausted {
		t.Error("expected AllExhausted to be persisted in store")
	}
	if !client.AllTiersExhausted() {
		t.Error("expected AllTiersExhausted() to return true")
	}
}

func TestUpgradeModelTierWithRegionFailover(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "tier-1",
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4", "europe-west1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-1",
	}

	// Exhaust tier-1 -> tier-2
	err := client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.currentModel != "tier-2" {
		t.Errorf("expected tier-2, got %q", client.currentModel)
	}
	if client.currentLocation != "us-central1" {
		t.Errorf("expected us-central1, got %q", client.currentLocation)
	}

	// Exhaust tier-2 in us-central1 -> should switch to us-east4, reset to tier-1
	err = client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected tier-1 after region switch, got %q", client.currentModel)
	}
	if client.currentLocation != "us-east4" {
		t.Errorf("expected us-east4, got %q", client.currentLocation)
	}

	// Exhaust tier-1 -> tier-2 in us-east4
	err = client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.currentModel != "tier-2" {
		t.Errorf("expected tier-2, got %q", client.currentModel)
	}

	// Exhaust tier-2 in us-east4 -> should switch to europe-west1, reset to tier-1
	err = client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected tier-1 after region switch, got %q", client.currentModel)
	}
	if client.currentLocation != "europe-west1" {
		t.Errorf("expected europe-west1, got %q", client.currentLocation)
	}

	// Exhaust tier-1 -> tier-2 in europe-west1
	err = client.upgradeModelTier(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exhaust tier-2 in europe-west1 -> no more regions, should truly exhaust
	err = client.upgradeModelTier(context.Background())
	if err == nil {
		t.Fatal("expected error when all regions exhausted")
	}
	if !client.allExhausted {
		t.Error("expected allExhausted=true")
	}
	if client.exhaustedAt.IsZero() {
		t.Error("expected exhaustedAt to be set")
	}
}

func TestHandleRateLimitError(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "tier-1",
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2", "tier-3"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-1",
	}

	ctx := context.Background()

	// First two calls should retry same model (transient rate limit)
	for i := 0; i < 2; i++ {
		shouldRetry, err := client.handleRateLimitError(ctx)
		if !shouldRetry {
			t.Fatalf("call %d: expected shouldRetry=true", i+1)
		}
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i+1, err)
		}
		if client.currentModel != "tier-1" {
			t.Errorf("call %d: expected model tier-1, got %q", i+1, client.currentModel)
		}
	}

	// Third call should escalate tier
	shouldRetry, err := client.handleRateLimitError(ctx)
	if !shouldRetry {
		t.Fatal("call 3: expected shouldRetry=true after tier upgrade")
	}
	if err != nil {
		t.Fatalf("call 3: unexpected error: %v", err)
	}
	if client.currentModel != "tier-2" {
		t.Errorf("call 3: expected model tier-2, got %q", client.currentModel)
	}
	if client.consecutive429s != 0 {
		t.Errorf("expected consecutive429s reset to 0, got %d", client.consecutive429s)
	}
}

func TestHandleRateLimitErrorResetOnSuccess(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "tier-1",
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-1",
		consecutive429s: 2, // about to escalate
	}

	// Simulate a successful call resetting the counter
	client.resetConsecutiveErrors()
	if client.consecutive429s != 0 {
		t.Errorf("expected consecutive429s reset to 0, got %d", client.consecutive429s)
	}
	if client.consecutive504s != 0 {
		t.Errorf("expected consecutive504s reset to 0, got %d", client.consecutive504s)
	}

	// Now a 429 should be treated as first occurrence (retry same model)
	shouldRetry, err := client.handleRateLimitError(context.Background())
	if !shouldRetry || err != nil {
		t.Fatalf("expected shouldRetry=true with no error, got retry=%v err=%v", shouldRetry, err)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected model tier-1, got %q", client.currentModel)
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
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2", "tier-3"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "old-removed-model",
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

func TestSwitchRegion(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4", "europe-west1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-2",
		consecutive429s: 5,
		consecutive504s: 3,
	}

	// Switch from us-central1 -> us-east4
	switched := client.switchRegion(context.Background())
	if !switched {
		t.Fatal("expected switchRegion to return true")
	}
	if client.currentLocation != "us-east4" {
		t.Errorf("expected us-east4, got %q", client.currentLocation)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected model reset to tier-1, got %q", client.currentModel)
	}
	if client.consecutive429s != 0 {
		t.Errorf("expected consecutive429s reset to 0, got %d", client.consecutive429s)
	}
	if client.consecutive504s != 0 {
		t.Errorf("expected consecutive504s reset to 0, got %d", client.consecutive504s)
	}

	// Switch from us-east4 -> europe-west1
	switched = client.switchRegion(context.Background())
	if !switched {
		t.Fatal("expected switchRegion to return true")
	}
	if client.currentLocation != "europe-west1" {
		t.Errorf("expected europe-west1, got %q", client.currentLocation)
	}

	// No more regions
	switched = client.switchRegion(context.Background())
	if switched {
		t.Fatal("expected switchRegion to return false when no more regions")
	}
	if client.currentLocation != "europe-west1" {
		t.Errorf("expected location unchanged at europe-west1, got %q", client.currentLocation)
	}
}

func TestSwitchRegionSingleLocation(t *testing.T) {
	client := &Client{
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		fallbackModels:  []string{"tier-1"},
	}

	switched := client.switchRegion(context.Background())
	if switched {
		t.Fatal("expected switchRegion to return false with single location")
	}
}

func TestCooldownRecovery(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:   today,
			CurrentModel: "tier-2",
			AllExhausted: true,
			ExhaustedAt:  time.Now().Add(-31 * time.Minute), // 31 min ago
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4"},
		currentLocation: "us-east4",
		currentDay:      today,
		currentModel:    "tier-2",
		allExhausted:    true,
		exhaustedAt:     time.Now().Add(-31 * time.Minute),
	}

	// AllTiersExhausted should return false because cooldown elapsed
	if client.AllTiersExhausted() {
		t.Error("expected AllTiersExhausted()=false after cooldown")
	}

	// checkDayRollover should reset everything
	model := client.checkDayRollover(context.Background())
	if model != "tier-1" {
		t.Errorf("expected model tier-1 after cooldown reset, got %q", model)
	}
	if client.currentLocation != "us-central1" {
		t.Errorf("expected location reset to us-central1, got %q", client.currentLocation)
	}
	if client.allExhausted {
		t.Error("expected allExhausted=false after cooldown reset")
	}
	if !client.exhaustedAt.IsZero() {
		t.Error("expected exhaustedAt to be zero after cooldown reset")
	}
}

func TestCooldownNotElapsed(t *testing.T) {
	today := getPacificDate()

	client := &Client{
		store:           &mockQuotaStore{},
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentDay:      today,
		currentModel:    "tier-2",
		allExhausted:    true,
		exhaustedAt:     time.Now().Add(-5 * time.Minute), // only 5 min ago
	}

	// Should still be exhausted
	if !client.AllTiersExhausted() {
		t.Error("expected AllTiersExhausted()=true before cooldown elapses")
	}
}

func TestHandle504Error(t *testing.T) {
	store := &mockQuotaStore{}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4"},
		currentLocation: "us-central1",
		currentModel:    "tier-1",
		consecutive504s: 0,
	}

	ctx := context.Background()

	// First 4 calls should not trigger region switch
	for i := 0; i < 4; i++ {
		switched := client.handle504Error(ctx)
		if switched {
			t.Fatalf("call %d: did not expect region switch yet", i+1)
		}
	}

	// 5th call should trigger region switch
	switched := client.handle504Error(ctx)
	if !switched {
		t.Fatal("expected region switch on 5th consecutive 504")
	}
	if client.currentLocation != "us-east4" {
		t.Errorf("expected us-east4, got %q", client.currentLocation)
	}
	if client.currentModel != "tier-1" {
		t.Errorf("expected model reset to tier-1, got %q", client.currentModel)
	}
	if client.consecutive504s != 0 {
		t.Errorf("expected consecutive504s reset to 0, got %d", client.consecutive504s)
	}
}

func TestHandle504ErrorNoMoreRegions(t *testing.T) {
	client := &Client{
		store:           &mockQuotaStore{},
		fallbackModels:  []string{"tier-1"},
		locations:       []string{"us-central1"},
		currentLocation: "us-central1",
		currentModel:    "tier-1",
		consecutive504s: 4,
	}

	// Should not switch (only one region)
	switched := client.handle504Error(context.Background())
	if switched {
		t.Fatal("expected no region switch with single location")
	}
	// Counter should still be reset
	if client.consecutive504s != 0 {
		t.Errorf("expected consecutive504s reset to 0, got %d", client.consecutive504s)
	}
}

func TestLocationPersistence(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:      today,
			CurrentModel:    "tier-2",
			CurrentLocation: "us-east4",
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4", "europe-west1"},
		currentLocation: "us-central1",
	}

	client.initQuotaState(context.Background())

	if client.currentLocation != "us-east4" {
		t.Errorf("expected location loaded from Firestore: us-east4, got %q", client.currentLocation)
	}
	if client.currentModel != "tier-2" {
		t.Errorf("expected model loaded from Firestore: tier-2, got %q", client.currentModel)
	}
}

func TestExtractJSONValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain JSON object",
			input: `{"clean_title": "Test", "is_warm": false, "is_lava_hot": false}`,
			want:  `{"clean_title": "Test", "is_warm": false, "is_lava_hot": false}`,
		},
		{
			name:  "JSON with trailing text",
			input: `{"clean_title": "Pioneer Subwoofer", "is_warm": false, "is_lava_hot": false}` + "\nThe eBay listing is overpriced.",
			want:  `{"clean_title": "Pioneer Subwoofer", "is_warm": false, "is_lava_hot": false}`,
		},
		{
			name:  "JSON array",
			input: `[{"item": "a"}, {"item": "b"}]`,
			want:  `[{"item": "a"}, {"item": "b"}]`,
		},
		{
			name:  "JSON array with trailing text",
			input: `[{"item": "a"}] some extra text`,
			want:  `[{"item": "a"}]`,
		},
		{
			name:  "JSON with escaped quotes",
			input: `{"title": "10\" Monitor", "ok": true}` + " trailing",
			want:  `{"title": "10\" Monitor", "ok": true}`,
		},
		{
			name:  "JSON with nested braces",
			input: `{"outer": {"inner": "val"}}` + " extra",
			want:  `{"outer": {"inner": "val"}}`,
		},
		{
			name:  "no JSON at all",
			input: `no json here`,
			want:  `no json here`,
		},
		{
			name:  "leading text before JSON",
			input: `Here is the result: {"ok": true}`,
			want:  `{"ok": true}`,
		},
		{
			name:  "braces inside strings should not confuse parser",
			input: `{"msg": "use {braces} carefully"}` + " done",
			want:  `{"msg": "use {braces} carefully"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSONValue(tt.input)
			if got != tt.want {
				t.Errorf("extractJSONValue(%q)\n  got:  %q\n  want: %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestStripCodeBlockWithTrailingText(t *testing.T) {
	input := "```json\n{\"ok\": true}\n```\nSome explanation."
	got := stripCodeBlock(input)
	want := `{"ok": true}`
	if got != want {
		t.Errorf("stripCodeBlock with trailing text after code fence:\n  got:  %q\n  want: %q", got, want)
	}
}

func TestLocationPersistenceInvalidLocation(t *testing.T) {
	today := getPacificDate()
	store := &mockQuotaStore{
		quota: &models.GeminiQuotaStatus{
			CurrentDay:      today,
			CurrentModel:    "tier-1",
			CurrentLocation: "asia-south1", // not in our locations list
		},
	}

	client := &Client{
		store:           store,
		fallbackModels:  []string{"tier-1", "tier-2"},
		locations:       []string{"us-central1", "us-east4"},
		currentLocation: "us-central1",
	}

	client.initQuotaState(context.Background())

	// Should keep the default location since the stored one is invalid
	if client.currentLocation != "us-central1" {
		t.Errorf("expected default location us-central1 for invalid stored location, got %q", client.currentLocation)
	}
}
