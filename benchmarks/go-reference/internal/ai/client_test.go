package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"google.golang.org/genai"
)

func TestConfiguration(t *testing.T) {
	ctx := context.Background()
	if c, err := NewClient(ctx, nil, nil, nil); c != nil || err != nil {
		t.Fatalf("disabled: %v %v", c, err)
	}
	if _, err := NewClient(ctx, []string{"fixture"}, nil, nil); err == nil {
		t.Fatal("missing model accepted")
	}
}

func TestRestoreQuotaState(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      *models.GeminiQuotaStatus
		storeErr   error
		model, key string
		exhausted  bool
	}{
		{name: "new store", model: "first", key: "key0"},
		{name: "store unavailable", storeErr: errors.New("fixture unavailable"), model: "first", key: "key0"},
		{name: "persisted fallback", state: &models.GeminiQuotaStatus{CurrentDay: getPacificDate(), CurrentModel: "second", CurrentLocation: "key1"}, model: "second", key: "key1"},
		{name: "stale model", state: &models.GeminiQuotaStatus{CurrentDay: getPacificDate(), CurrentModel: "removed", CurrentLocation: "key1"}, model: "first", key: "key1"},
		{name: "stale key", state: &models.GeminiQuotaStatus{CurrentDay: getPacificDate(), CurrentModel: "second", CurrentLocation: "key99"}, model: "second", key: "key0"},
		{name: "previous day", state: &models.GeminiQuotaStatus{CurrentDay: "2000-01-01", CurrentModel: "second", CurrentLocation: "key1", AllExhausted: true}, model: "first", key: "key0"},
		{name: "active cooldown", state: &models.GeminiQuotaStatus{CurrentDay: getPacificDate(), CurrentModel: "second", CurrentLocation: "key1", AllExhausted: true, ExhaustedAt: time.Now()}, model: "second", key: "key1", exhausted: true},
		{name: "expired cooldown", state: &models.GeminiQuotaStatus{CurrentDay: getPacificDate(), CurrentModel: "second", CurrentLocation: "key1", AllExhausted: true, ExhaustedAt: time.Now().Add(-31 * time.Minute)}, model: "first", key: "key0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &mockQuotaStore{quota: test.state, err: test.storeErr}
			c, err := NewClient(context.Background(), []string{"one", "two"}, []string{"first", "second"}, store)
			if err != nil {
				t.Fatal(err)
			}
			if c.state.CurrentModel != test.model || c.state.CurrentLocation != test.key || c.state.AllExhausted != test.exhausted || c.state.CurrentDay != getPacificDate() {
				t.Fatalf("restored state: %+v", c.state)
			}
		})
	}
}

func TestFallbackOrderAndPersistence(t *testing.T) {
	ctx := context.Background()
	store := &mockQuotaStore{}
	c, err := NewClient(ctx, []string{"one", "two", "three"}, []string{"first", "second"}, store)
	if err != nil {
		t.Fatal(err)
	}
	var visited []string
	for {
		visited = append(visited, c.state.CurrentLocation+"/"+c.state.CurrentModel)
		if err = c.nextModel(ctx); err != nil {
			break
		}
		if !reflect.DeepEqual(*store.quota, c.state) {
			t.Fatal("fallback was not persisted")
		}
	}
	want := []string{"key0/first", "key0/second", "key1/first", "key1/second", "key2/first", "key2/second"}
	if !reflect.DeepEqual(visited, want) || !errors.Is(err, ErrQuotaCooldown) || !store.quota.AllExhausted || store.quota.ExhaustedAt.IsZero() {
		t.Fatalf("fallback sequence=%v error=%v stored=%+v", visited, err, store.quota)
	}
}

func TestRateLimitRequiresThreeConsecutiveResponses(t *testing.T) {
	ctx := context.Background()
	c, err := NewClient(ctx, []string{"fixture"}, []string{"first", "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	quota := genai.APIError{Code: 429, Message: "fixture"}
	for response := 1; response <= 3; response++ {
		err, delay := c.handleError(ctx, quota)
		if err == nil {
			t.Fatal("missing retry error")
		}
		if response < 3 && (c.state.CurrentModel != "first" || delay != 5*time.Second) {
			t.Fatal("premature fallback")
		}
	}
	if c.state.CurrentModel != "second" || c.rateLimits != 0 {
		t.Fatalf("fallback: %+v", c.state)
	}
}

func TestTimeoutFallbackAndSuccessReset(t *testing.T) {
	ctx := context.Background()
	c, err := NewClient(ctx, []string{"one", "two"}, []string{"first", "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		c.handleError(ctx, genai.APIError{Code: 504})
	}
	if c.keyIndex != 0 {
		t.Fatal("premature key fallback")
	}
	c.handleError(ctx, genai.APIError{Code: 504})
	if c.keyIndex != 1 || c.modelIndex != 0 || c.timeouts != 0 {
		t.Fatalf("fallback: %+v", c.state)
	}
	for i := 0; i < 5; i++ {
		c.handleError(ctx, genai.APIError{Code: 504})
	}
	if c.state.AllExhausted {
		t.Fatal("temporary timeout became quota exhaustion")
	}
	fixture := newCooldownTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, cleanTitleFixture) })
	fixture.rateLimits, fixture.timeouts = 2, 4
	if _, err := fixture.CleanTitles(ctx, []models.TitleRequest{{Index: 0, Title: "Monitor"}}); err != nil {
		t.Fatal(err)
	}
	if fixture.rateLimits != 0 || fixture.timeouts != 0 {
		t.Fatal("success retained consecutive failures")
	}
}

func TestUnsupportedModelFallsBack(t *testing.T) {
	c, err := NewClient(context.Background(), []string{"fixture"}, []string{"first", "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	err, delay := c.handleError(context.Background(), genai.APIError{Code: 400, Message: "Feature is not supported"})
	if err == nil || delay != 0 || c.state.CurrentModel != "second" {
		t.Fatalf("unsupported model fallback: %v %v %+v", err, delay, c.state)
	}
}

func TestUsageCountsNetworkRequestsAndResponseFailures(t *testing.T) {
	for _, test := range []struct {
		name, first string
		status      int
		wantParse   int
	}{
		{"transient service", `{"error":{"code":503,"message":"unavailable"}}`, 503, 0},
		{"invalid JSON", `{"candidates":[{"content":{"parts":[{"text":"bad JSON"}]}}]}`, 200, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			c := newCooldownTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(test.status)
					fmt.Fprint(w, test.first)
					return
				}
				fmt.Fprint(w, `{"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":4},"candidates":[{"content":{"parts":[{"text":"[{\"index\":0,\"clean_title\":\"Monitor\"}]"}]}}]}`)
			})
			if _, err := c.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}}); err != nil {
				t.Fatal(err)
			}
			r, in, out, parse, retries := c.DrainUsage()
			if r != 2 || in != 12 || out != 4 || parse != test.wantParse || retries != 1 {
				t.Fatalf("usage=%d %d %d %d %d", r, in, out, parse, retries)
			}
			r, in, out, parse, retries = c.DrainUsage()
			if r+in+out+parse+retries != 0 {
				t.Fatal("usage drained twice")
			}
			c.mu.Lock()
			c.state.AllExhausted = true
			c.state.ExhaustedAt = time.Now()
			c.mu.Unlock()
			c.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}})
			r, in, out, parse, retries = c.DrainUsage()
			if r+in+out+parse+retries != 0 {
				t.Fatal("cooldown counted as network activity")
			}
		})
	}
}

func TestPartialRepairOnlyRequestsMissingIndexes(t *testing.T) {
	calls := 0
	c := newCooldownTestClient(t, nil, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"[{\"index\":10,\"clean_title\":\"First\"},{\"index\":99,\"clean_title\":\"unrequested\"},{\"index\":20,\"clean_title\":\" \"}]"}]}}]}`)
			return
		}
		var request struct {
			Contents []struct{ Parts []struct{ Text string } }
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		prompt := request.Contents[0].Parts[0].Text
		if strings.Contains(prompt, "10. Title:") || !strings.Contains(prompt, "20. Title:") {
			t.Errorf("wrong repair prompt: %s", prompt)
		}
		fmt.Fprint(w, `{"candidates":[{"content":{"parts":[{"text":"[{\"index\":20,\"clean_title\":\"Second\"}]"}]}}]}`)
	})
	got, err := c.CleanTitles(context.Background(), []models.TitleRequest{{Index: 10, Title: "First"}, {Index: 20, Title: "Second"}})
	if err != nil || !reflect.DeepEqual(got, map[int]string{10: "First", 20: "Second"}) || calls != 2 {
		t.Fatalf("repair=%v error=%v calls=%d", got, err, calls)
	}
	requests, _, _, parse, retries := c.DrainUsage()
	if requests != 2 || parse != 0 || retries != 0 {
		t.Fatalf("repair usage=%d %d %d", requests, parse, retries)
	}
}

func TestHTTPFallbackUsesConfiguredModelAndKeyOrder(t *testing.T) {
	var visited []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visited = append(visited, r.Header.Get("X-Goog-Api-Key")+" "+r.URL.Path)
		if len(visited) < 3 {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"code":400,"message":"Feature is not supported"}}`)
			return
		}
		fmt.Fprint(w, cleanTitleFixture)
	}))
	defer server.Close()
	keys := []string{"fixture-one", "fixture-two"}
	c, err := NewClient(context.Background(), keys, []string{"first", "second"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, key := range keys {
		c.clients[i], err = genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey: key, Backend: genai.BackendGeminiAPI, HTTPClient: server.Client(),
			HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	results, err := c.CleanTitles(context.Background(), []models.TitleRequest{{Index: 0, Title: "Monitor"}})
	want := []string{
		"fixture-one /v1beta/models/first:generateContent",
		"fixture-one /v1beta/models/second:generateContent",
		"fixture-two /v1beta/models/first:generateContent",
	}
	if err != nil || results[0] != "Monitor for $100" || !reflect.DeepEqual(visited, want) {
		t.Fatalf("fallback requests=%v results=%v error=%v", visited, results, err)
	}
}

func TestParseTitlesIgnoresThoughtsAndHandlesSplitJSON(t *testing.T) {
	response := &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{Content: &genai.Content{Parts: []*genai.Part{
		{Thought: true, Text: `[{"index":0,"clean_title":"Do not display internal thought"}]`},
		nil,
		{Text: `[{"index":0,`},
		{Text: `"clean_title":"Monitor"}]`},
	}}}}}
	results, err := parseTitles(response)
	if err != nil || len(results) != 1 || results[0].CleanTitle != "Monitor" {
		t.Fatalf("parsed titles=%v error=%v", results, err)
	}
	for _, response := range []*genai.GenerateContentResponse{nil, {}, {Candidates: []*genai.Candidate{nil}}} {
		if _, err := parseTitles(response); err == nil {
			t.Fatal("empty response accepted")
		}
	}
}
