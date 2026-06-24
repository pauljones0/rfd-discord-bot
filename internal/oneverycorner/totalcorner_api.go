package oneverycorner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"
)

const (
	DefaultTotalCornerAPIBaseURL  = "https://api.totalcorner.com/v1"
	DefaultTotalCornerAPITimezone = "Europe/London"
	totalCornerAPIMaxPages        = 100
)

type TotalCornerAPIConfig struct {
	BaseURL    string
	Token      string
	LeagueIDs  []string
	Timezone   string
	HTTPClient *http.Client
}

type MatchWindow struct {
	ID         string    `json:"id"`
	LeagueID   string    `json:"league_id"`
	LeagueName string    `json:"league_name"`
	HomeTeam   string    `json:"home_team"`
	AwayTeam   string    `json:"away_team"`
	Start      time.Time `json:"start"`
	Status     string    `json:"status"`
}

type MatchSnapshot struct {
	MatchWindow
	HomeCorners int `json:"home_corners"`
	AwayCorners int `json:"away_corners"`
	HomeScore   int `json:"home_score"`
	AwayScore   int `json:"away_score"`
}

type TotalCornerSource interface {
	Schedule(ctx context.Context, from, through time.Time) ([]MatchWindow, error)
	InPlay(ctx context.Context) ([]MatchSnapshot, error)
}

type TotalCornerAPIClient struct {
	baseURL      string
	token        string
	leagueIDs    map[string]struct{}
	leagueIDList []string
	timezone     *time.Location
	http         *http.Client
}

type totalCornerAPIResponse struct {
	Success    int                      `json:"success"`
	Error      any                      `json:"error"`
	Message    string                   `json:"message"`
	Pagination totalCornerAPIPagination `json:"pagination"`
	Data       json.RawMessage          `json:"data"`
}

type totalCornerAPIPagination struct {
	Current any `json:"current"`
	Pages   any `json:"pages"`
	Next    any `json:"next"`
}

type totalCornerAPIMatch struct {
	ID       any `json:"id"`
	Home     any `json:"h"`
	Away     any `json:"a"`
	League   any `json:"l"`
	LeagueID any `json:"l_id"`
	Start    any `json:"start"`
	Status   any `json:"status"`
	HC       any `json:"hc"`
	AC       any `json:"ac"`
	HG       any `json:"hg"`
	AG       any `json:"ag"`
}

func NewTotalCornerAPIClient(cfg TotalCornerAPIConfig) *TotalCornerAPIClient {
	cfg = normalizeTotalCornerAPIConfig(cfg)
	leagueIDs := make(map[string]struct{}, len(cfg.LeagueIDs))
	for _, id := range cfg.LeagueIDs {
		leagueIDs[id] = struct{}{}
	}
	return &TotalCornerAPIClient{
		baseURL:      strings.TrimRight(cfg.BaseURL, "/"),
		token:        strings.TrimSpace(cfg.Token),
		leagueIDs:    leagueIDs,
		leagueIDList: append([]string(nil), cfg.LeagueIDs...),
		timezone:     loadTotalCornerLocation(cfg.Timezone),
		http:         cfg.HTTPClient,
	}
}

func normalizeTotalCornerAPIConfig(cfg TotalCornerAPIConfig) TotalCornerAPIConfig {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = DefaultTotalCornerAPIBaseURL
	}
	if strings.TrimSpace(cfg.Timezone) == "" {
		cfg.Timezone = DefaultTotalCornerAPITimezone
	}
	cfg.LeagueIDs = normalizeTotalCornerLeagueIDs(cfg.LeagueIDs)
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	return cfg
}

func (c *TotalCornerAPIClient) Schedule(ctx context.Context, from, through time.Time) ([]MatchWindow, error) {
	if c == nil {
		return nil, fmt.Errorf("totalcorner api client is nil")
	}
	if strings.TrimSpace(c.token) == "" {
		return nil, fmt.Errorf("totalcorner api token is missing")
	}
	if through.Before(from) {
		return nil, nil
	}

	from = from.UTC()
	through = through.UTC()
	windows := make([]MatchWindow, 0)
	if len(c.leagueIDList) > 0 {
		for _, leagueID := range c.leagueIDList {
			matches, err := c.fetchMatches(ctx, "/league/schedule/"+url.PathEscape(leagueID), nil)
			if err != nil {
				return nil, err
			}
			for _, match := range matches {
				snapshot := match.toSnapshot(c.timezone)
				if !c.leagueAllowed(snapshot.LeagueID) || snapshot.Start.IsZero() {
					continue
				}
				if snapshot.Start.Before(from) || snapshot.Start.After(through) {
					continue
				}
				windows = append(windows, snapshot.MatchWindow)
			}
		}
	} else {
		startDay := utcDay(from)
		endDay := utcDay(through)
		for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
			values := url.Values{}
			values.Set("date", day.Format("20060102"))
			matches, err := c.fetchMatches(ctx, "/match/schedule", values)
			if err != nil {
				return nil, err
			}
			for _, match := range matches {
				snapshot := match.toSnapshot(c.timezone)
				if !c.leagueAllowed(snapshot.LeagueID) || snapshot.Start.IsZero() {
					continue
				}
				if snapshot.Start.Before(from) || snapshot.Start.After(through) {
					continue
				}
				windows = append(windows, snapshot.MatchWindow)
			}
		}
	}
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Start.Equal(windows[j].Start) {
			return windows[i].ID < windows[j].ID
		}
		return windows[i].Start.Before(windows[j].Start)
	})
	return dedupeMatchWindows(windows), nil
}

func (c *TotalCornerAPIClient) InPlay(ctx context.Context) ([]MatchSnapshot, error) {
	if c == nil {
		return nil, fmt.Errorf("totalcorner api client is nil")
	}
	if strings.TrimSpace(c.token) == "" {
		return nil, fmt.Errorf("totalcorner api token is missing")
	}
	values := url.Values{}
	values.Set("type", "inplay")
	matches, err := c.fetchMatches(ctx, "/match/today", values)
	if err != nil {
		return nil, err
	}
	snapshots := make([]MatchSnapshot, 0, len(matches))
	for _, match := range matches {
		snapshot := match.toSnapshot(c.timezone)
		if !c.leagueAllowed(snapshot.LeagueID) {
			continue
		}
		snapshots = append(snapshots, snapshot)
	}
	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ID < snapshots[j].ID
	})
	return snapshots, nil
}

func (c *TotalCornerAPIClient) fetchMatches(ctx context.Context, path string, values url.Values) ([]totalCornerAPIMatch, error) {
	out := make([]totalCornerAPIMatch, 0)
	for page := 1; page <= totalCornerAPIMaxPages; page++ {
		requestValues := cloneValues(values)
		requestValues.Set("token", c.token)
		requestValues.Set("page", strconv.Itoa(page))
		requestURL := c.endpoint(path, requestValues)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if err != nil {
			return nil, fmt.Errorf("build totalcorner request: %w", err)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("totalcorner request failed: %w", err)
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = fmt.Errorf("totalcorner api returned http %d", resp.StatusCode)
				return
			}
			decoder := json.NewDecoder(resp.Body)
			decoder.UseNumber()
			var apiResp totalCornerAPIResponse
			if decodeErr := decoder.Decode(&apiResp); decodeErr != nil {
				err = fmt.Errorf("decode totalcorner response: %w", decodeErr)
				return
			}
			if apiResp.Success != 1 {
				msg := firstNonEmpty(totalCornerAPIErrorMessage(apiResp.Error), apiResp.Message, "unknown api error")
				err = fmt.Errorf("totalcorner api returned success=%d: %s", apiResp.Success, msg)
				return
			}
			matches, matchesErr := apiResp.matches()
			if matchesErr != nil {
				err = matchesErr
				return
			}
			out = append(out, matches...)
			if !apiResp.hasNext(page) {
				page = totalCornerAPIMaxPages
			}
		}()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r totalCornerAPIResponse) matches() ([]totalCornerAPIMatch, error) {
	if len(r.Data) == 0 || string(r.Data) == "null" {
		return nil, nil
	}
	var direct []totalCornerAPIMatch
	if err := json.Unmarshal(r.Data, &direct); err == nil {
		return direct, nil
	}
	var wrapped struct {
		Matches []totalCornerAPIMatch `json:"matches"`
	}
	if err := json.Unmarshal(r.Data, &wrapped); err != nil {
		return nil, fmt.Errorf("decode totalcorner data matches: %w", err)
	}
	return wrapped.Matches, nil
}

func (r totalCornerAPIResponse) hasNext(page int) bool {
	if pages := fieldInt(r.Pagination.Pages); pages > 0 {
		return page < pages
	}
	switch next := r.Pagination.Next.(type) {
	case bool:
		return next
	case string:
		return strings.TrimSpace(next) != "" && !strings.EqualFold(strings.TrimSpace(next), "false")
	case json.Number:
		n, _ := next.Int64()
		return n > int64(page)
	case float64:
		return int(next) > page
	default:
		return false
	}
}

func totalCornerAPIErrorMessage(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		return strconv.FormatBool(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if msg := totalCornerAPIErrorMessage(item); msg != "" {
				parts = append(parts, msg)
			}
		}
		return strings.Join(uniqueNonEmpty(parts), "; ")
	case map[string]any:
		parts := make([]string, 0, 4)
		for _, key := range []string{"message", "msg", "detail", "error"} {
			if msg := totalCornerAPIErrorMessage(v[key]); msg != "" {
				parts = append(parts, msg)
			}
		}
		if code := totalCornerAPIErrorMessage(v["code"]); code != "" {
			parts = append(parts, "code="+code)
		}
		if len(parts) > 0 {
			return strings.Join(uniqueNonEmpty(parts), " ")
		}
		body, err := json.Marshal(v)
		if err == nil {
			return string(body)
		}
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func (c *TotalCornerAPIClient) endpoint(path string, values url.Values) string {
	base := c.baseURL
	if strings.HasSuffix(base, "/match/today") {
		base = strings.TrimSuffix(base, "/match/today")
	}
	if strings.HasSuffix(base, "/match/schedule") {
		base = strings.TrimSuffix(base, "/match/schedule")
	}
	return base + path + "?" + values.Encode()
}

func (c *TotalCornerAPIClient) leagueAllowed(id string) bool {
	if len(c.leagueIDs) == 0 {
		return true
	}
	_, ok := c.leagueIDs[strings.TrimSpace(id)]
	return ok
}

func loadTotalCornerLocation(name string) *time.Location {
	location, err := time.LoadLocation(strings.TrimSpace(name))
	if err != nil {
		return time.UTC
	}
	return location
}

func (m totalCornerAPIMatch) toSnapshot(location *time.Location) MatchSnapshot {
	start, _ := parseTotalCornerStart(fieldString(m.Start), location)
	return MatchSnapshot{
		MatchWindow: MatchWindow{
			ID:         fieldString(m.ID),
			LeagueID:   fieldString(m.LeagueID),
			LeagueName: fieldString(m.League),
			HomeTeam:   fieldString(m.Home),
			AwayTeam:   fieldString(m.Away),
			Start:      start,
			Status:     fieldString(m.Status),
		},
		HomeCorners: fieldInt(m.HC),
		AwayCorners: fieldInt(m.AC),
		HomeScore:   fieldInt(m.HG),
		AwayScore:   fieldInt(m.AG),
	}
}

func parseTotalCornerStart(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("empty totalcorner start")
	}
	if location == nil {
		location = time.UTC
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), nil
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
	} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse totalcorner start %q", value)
}

func fieldString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func fieldInt(value any) int {
	raw := fieldString(value)
	if raw == "" {
		return 0
	}
	parsed, err := strconv.Atoi(raw)
	if err == nil {
		return parsed
	}
	asFloat, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return int(asFloat)
}

func cloneValues(values url.Values) url.Values {
	out := make(url.Values, len(values))
	for key, vals := range values {
		out[key] = append([]string(nil), vals...)
	}
	return out
}

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func dedupeMatchWindows(windows []MatchWindow) []MatchWindow {
	out := make([]MatchWindow, 0, len(windows))
	seen := make(map[string]struct{}, len(windows))
	for _, window := range windows {
		key := firstNonEmpty(window.ID, window.HomeTeam+"|"+window.AwayTeam+"|"+window.Start.Format(time.RFC3339))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, window)
	}
	return out
}
