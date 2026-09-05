package ai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/genai"
)

const cleanTitleFixture = `{"candidates":[{"content":{"parts":[{"text":"[{\"index\":0,\"clean_title\":\"Monitor for $100\"}]"}]}}]}`

func newCooldownTestClient(t *testing.T, store QuotaStore, handler http.HandlerFunc) *Client {
	t.Helper()
	ctx := context.Background()
	client, err := NewClient(ctx, "", nil, []string{"fixture-key"}, []string{"fixture-model"}, store)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	fixtureClient, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:     "fixture-key",
		Backend:    genai.BackendGeminiAPI,
		HTTPClient: server.Client(),
		HTTPOptions: genai.HTTPOptions{
			BaseURL: server.URL,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	client.clients["key0"] = fixtureClient
	return client
}

func TestGenerationHonorsPersistedCooldownAfterRestart(t *testing.T) {
	store := &mockQuotaStore{quota: &models.GeminiQuotaStatus{
		CurrentDay:      getPacificDate(),
		CurrentModel:    "fixture-model",
		CurrentLocation: "key0",
		AllExhausted:    true,
		ExhaustedAt:     time.Now(),
	}}
	var calls atomic.Int32
	for restart := 0; restart < 2; restart++ {
		client := newCooldownTestClient(t, store, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(cleanTitleFixture))
		})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, titleErr := client.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}})
		_, _, _, rawErr := client.GenerateContentRaw(ctx, "fixture", nil)
		_, _, _, overrideErr := client.GenerateContentWithModel(ctx, "fixture-model", "fixture", nil)
		cancel()
		for operation, err := range map[string]error{"titles": titleErr, "raw": rawErr, "override": overrideErr} {
			if !errors.Is(err, ErrQuotaCooldown) {
				t.Errorf("restart %d %s error = %v, want quota cooldown", restart, operation, err)
			}
		}
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("API calls during persisted cooldown = %d, want 0", got)
	}
}

func TestTitleCleanupRecoversFromCooldownAndDayRollover(t *testing.T) {
	for _, test := range []struct {
		name string
		day  string
		at   time.Time
	}{
		{"expired persisted cooldown", getPacificDate(), time.Now().Add(-31 * time.Minute)},
		{"new quota day", "2000-01-01", time.Now()},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &mockQuotaStore{quota: &models.GeminiQuotaStatus{
				CurrentDay:      test.day,
				CurrentModel:    "fixture-model",
				CurrentLocation: "key0",
				AllExhausted:    true,
				ExhaustedAt:     test.at,
			}}
			var calls atomic.Int32
			client := newCooldownTestClient(t, store, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(cleanTitleFixture))
			})
			results, err := client.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}})
			if err != nil || results[0] != "Monitor for $100" {
				t.Fatalf("recovered title cleanup = %v, %v", results, err)
			}
			if calls.Load() != 1 || store.quota.AllExhausted || !store.quota.ExhaustedAt.IsZero() || store.quota.CurrentDay != getPacificDate() {
				t.Fatalf("unexpected recovered state: calls=%d quota=%+v", calls.Load(), store.quota)
			}
		})
	}
}

func TestTitleCleanupHonorsInMemoryCooldownWithoutStore(t *testing.T) {
	var calls atomic.Int32
	client := newCooldownTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cleanTitleFixture))
	})
	client.mu.Lock()
	client.allExhausted = true
	client.exhaustedAt = time.Now()
	client.mu.Unlock()
	_, err := client.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	if !errors.Is(err, ErrQuotaCooldown) || calls.Load() != 0 {
		t.Fatalf("active cooldown: error=%v API calls=%d", err, calls.Load())
	}
	client.mu.Lock()
	client.exhaustedAt = time.Now().Add(-31 * time.Minute)
	client.mu.Unlock()
	results, err := client.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	if err != nil || results[0] != "Monitor for $100" || calls.Load() != 1 {
		t.Fatalf("recovered cleanup: results=%v error=%v API calls=%d", results, err, calls.Load())
	}
}

func TestTitleCleanupStopsImmediatelyAfterTerminalExhaustion(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"quota", http.StatusTooManyRequests, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Fixture quota exhausted"}}`},
		{"unsupported feature", http.StatusBadRequest, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Feature is not supported"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &mockQuotaStore{}
			var calls atomic.Int32
			client := newCooldownTestClient(t, store, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			// The next quota response exhausts the final available key/model.
			client.consecutive429s = 2
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_, err := client.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}})
			if !errors.Is(err, ErrQuotaCooldown) {
				t.Fatalf("error = %v, want terminal quota cooldown", err)
			}
			if calls.Load() != 1 || !store.quota.AllExhausted || store.quota.ExhaustedAt.IsZero() {
				t.Fatalf("terminal state: calls=%d quota=%+v", calls.Load(), store.quota)
			}
		})
	}
}

func TestTitleCleanupRechecksCooldownBeforeRetry(t *testing.T) {
	var calls atomic.Int32
	var client *Client
	client = newCooldownTestClient(t, &mockQuotaStore{}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		// Another caller exhausted the shared quota while this request was in flight.
		client.mu.Lock()
		client.allExhausted = true
		client.exhaustedAt = time.Now()
		client.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"Fixture unavailable"}}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := client.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	if !errors.Is(err, ErrQuotaCooldown) || calls.Load() != 1 {
		t.Fatalf("retry after shared exhaustion: error=%v API calls=%d", err, calls.Load())
	}
}

func TestTitleCleanupDoesNotRetryPermanentAuthenticationFailure(t *testing.T) {
	var calls atomic.Int32
	client := newCooldownTestClient(t, &mockQuotaStore{}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":401,"status":"UNAUTHENTICATED","message":"Fixture authentication rejected"}}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := client.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatalf("permanent authentication failure: error=%v API calls=%d", err, calls.Load())
	}
}

func TestTitleCleanupRetriesTransientServiceFailure(t *testing.T) {
	var calls atomic.Int32
	client := newCooldownTestClient(t, &mockQuotaStore{}, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":503,"status":"UNAVAILABLE","message":"Fixture unavailable"}}`))
			return
		}
		_, _ = w.Write([]byte(cleanTitleFixture))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	results, err := client.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	if err != nil || results[0] != "Monitor for $100" || calls.Load() != 2 {
		t.Fatalf("transient retry: results=%v error=%v API calls=%d", results, err, calls.Load())
	}
}

func TestTitleRepairKeepsSuccessfulTitlesWhenQuotaBecomesExhausted(t *testing.T) {
	var calls atomic.Int32
	var client *Client
	client = newCooldownTestClient(t, &mockQuotaStore{}, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		client.mu.Lock()
		client.allExhausted = true
		client.exhaustedAt = time.Now()
		client.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(cleanTitleFixture))
	})
	results, err := client.CleanTitles(context.Background(), []models.TitleRequest{
		{Index: 0, Title: "Monitor"},
		{Index: 1, Title: "Mouse"},
	})
	if err != nil || len(results) != 1 || results[0] != "Monitor for $100" || calls.Load() != 1 {
		t.Fatalf("repair paused by quota: results=%v error=%v API calls=%d", results, err, calls.Load())
	}
}
