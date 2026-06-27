package oneverycorner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTotalCornerAPIScheduleUsesLeaguePagesAndLookaheadFilter(t *testing.T) {
	var requestedPages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "test-token" {
			t.Fatalf("token query missing")
		}
		if r.URL.Path != "/league/schedule/29754" {
			t.Fatalf("path = %q, want /league/schedule/29754", r.URL.Path)
		}
		page := r.URL.Query().Get("page")
		requestedPages = append(requestedPages, page)
		start := "2026-06-20 18:00:00"
		if page == "2" {
			start = "2026-06-21 18:00:00"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": 1,
			"pagination": map[string]any{
				"current": page,
				"pages":   2,
				"next":    page == "1",
			},
			"data": map[string]any{
				"league": map[string]any{"league_id": "29754", "name": "World Cup 2026"},
				"matches": []map[string]any{
					{
						"id": "target-" + page,
						"h":  "Canada", "a": "Brazil",
						"l": "World Cup", "l_id": "29754",
						"start":  start,
						"status": "upcoming",
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{
		BaseURL:    server.URL,
		Token:      "test-token",
		LeagueIDs:  []string{"29754"},
		HTTPClient: server.Client(),
	})
	from := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	through := from.Add(36 * time.Hour)
	got, err := client.Schedule(context.Background(), from, through)
	if err != nil {
		t.Fatalf("Schedule returned error: %v", err)
	}
	if len(requestedPages) != 2 || requestedPages[0] != "1" || requestedPages[1] != "2" {
		t.Fatalf("requested pages = %v, want [1 2]", requestedPages)
	}
	if len(got) != 1 {
		t.Fatalf("matches = %d, want only match in lookahead window", len(got))
	}
	wantStart := time.Date(2026, 6, 20, 17, 0, 0, 0, time.UTC)
	if got[0].ID != "target-1" || !got[0].Start.Equal(wantStart) {
		t.Fatalf("match = %+v", got[0])
	}
}

func TestTotalCornerAPIDefaultTimezoneParsesNaiveLondonTime(t *testing.T) {
	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{})
	got := totalCornerAPIMatch{
		ID:       "m1",
		Home:     "Switzerland",
		Away:     "Canada",
		League:   "World Cup",
		Start:    "2026-06-24 20:00:00",
		LeagueID: "29754",
	}.toSnapshot(client.timezone)
	want := time.Date(2026, 6, 24, 19, 0, 0, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Start, want)
	}
}

func TestTotalCornerAPICustomTimezoneParsesNaiveStart(t *testing.T) {
	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{Timezone: "UTC"})
	got := totalCornerAPIMatch{
		ID:       "m1",
		Home:     "Switzerland",
		Away:     "Canada",
		League:   "World Cup",
		Start:    "2026-06-24 20:00:00",
		LeagueID: "29754",
	}.toSnapshot(client.timezone)
	want := time.Date(2026, 6, 24, 20, 0, 0, 0, time.UTC)
	if !got.Start.Equal(want) {
		t.Fatalf("start = %s, want %s", got.Start, want)
	}
}

func TestTotalCornerAPIInPlayParsesCounters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/match/today" || r.URL.Query().Get("type") != "inplay" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    1,
			"pagination": map[string]any{"current": 1, "pages": 1, "next": false},
			"data": []map[string]any{
				{
					"id": "m1",
					"h":  "Canada", "a": "Brazil",
					"l": "World Cup", "l_id": "29754",
					"start":  "2026-06-20 18:00:00",
					"status": "35",
					"hc":     "2", "ac": "1", "hg": "0", "ag": "1",
				},
			},
		})
	}))
	defer server.Close()

	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{
		BaseURL:    server.URL,
		Token:      "test-token",
		LeagueIDs:  []string{"29754"},
		HTTPClient: server.Client(),
	})
	got, err := client.InPlay(context.Background())
	if err != nil {
		t.Fatalf("InPlay returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(got))
	}
	if got[0].HomeCorners != 2 || got[0].AwayCorners != 1 || got[0].HomeScore != 0 || got[0].AwayScore != 1 {
		t.Fatalf("counters = %+v", got[0])
	}
}

func TestTotalCornerAPIInPlayRecordsStatsForFilteredRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":    1,
			"pagination": map[string]any{"current": 1, "pages": 1, "next": false},
			"data": []map[string]any{
				{
					"id": "other",
					"h":  "Other Home", "a": "Other Away",
					"l": "Other League", "l_id": "999",
					"status": "35",
					"hc":     "2", "ac": "1", "hg": "0", "ag": "1",
				},
			},
		})
	}))
	defer server.Close()

	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{
		BaseURL:    server.URL,
		Token:      "test-token",
		LeagueIDs:  []string{"29754"},
		HTTPClient: server.Client(),
	})
	got, err := client.InPlay(context.Background())
	if err != nil {
		t.Fatalf("InPlay returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("snapshots = %d, want no tracked rows", len(got))
	}
	stats := client.LastInPlayStats()
	if stats.Rows != 1 || stats.Matched != 0 {
		t.Fatalf("stats rows/matched = %d/%d, want 1/0", stats.Rows, stats.Matched)
	}
	if stats.LeagueCounts["999"] != 1 {
		t.Fatalf("league counts = %#v, want 999:1", stats.LeagueCounts)
	}
	if len(stats.LeagueIDs) != 1 || stats.LeagueIDs[0] != "29754" {
		t.Fatalf("league IDs = %#v, want [29754]", stats.LeagueIDs)
	}
}

func TestTotalCornerAPIErrorObjectReturnsReadableError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/match/today" || r.URL.Query().Get("type") != "inplay" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": 0,
			"error": map[string]any{
				"code":    "AUTH",
				"message": "invalid token",
			},
		})
	}))
	defer server.Close()

	client := NewTotalCornerAPIClient(TotalCornerAPIConfig{
		BaseURL:    server.URL,
		Token:      "test-token",
		LeagueIDs:  []string{"29754"},
		HTTPClient: server.Client(),
	})
	_, err := client.InPlay(context.Background())
	if err == nil {
		t.Fatal("InPlay error = nil, want API failure")
	}
	if strings.Contains(err.Error(), "decode totalcorner response") {
		t.Fatalf("error = %q, want API error rather than decode failure", err)
	}
	if !strings.Contains(err.Error(), "invalid token") || !strings.Contains(err.Error(), "code=AUTH") {
		t.Fatalf("error = %q, want readable error object details", err)
	}
}
