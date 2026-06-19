package oneverycorner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	DefaultTotalCornerURL          = "https://www.totalcorner.com/match/today"
	DefaultTotalCornerPollInterval = 1 * time.Second
	defaultTotalCornerRestartDelay = 10 * time.Second

	totalCornerSystemIssue      = "system_issue"
	totalCornerSystemFixAttempt = "system_fix_attempt"
	totalCornerSystemRecovered  = "system_recovered"
	totalCornerSystemFixFailed  = "system_fix_failed"
)

var defaultTotalCornerCommand = []string{"xvfb-run", "-a", "/opt/scrape-venv/bin/python", "/root/scripts/totalcorner_monitor.py"}

type TotalCornerConfig struct {
	Enabled      bool
	URL          string
	LeagueIDs    []string
	PollInterval time.Duration
	Command      []string
	RestartDelay time.Duration
}

type TotalCornerMonitor struct {
	processor *Processor
	config    TotalCornerConfig
	now       func() time.Time
}

type TotalCornerEvent struct {
	Type        string `json:"type"`
	MatchID     string `json:"match_id"`
	MatchName   string `json:"match_name"`
	LeagueID    string `json:"league_id"`
	LeagueName  string `json:"league_name"`
	HomeTeam    string `json:"home_team"`
	AwayTeam    string `json:"away_team"`
	Team        string `json:"team"`
	HomeCorners int    `json:"home_corners"`
	AwayCorners int    `json:"away_corners"`
	HomeScore   int    `json:"home_score"`
	AwayScore   int    `json:"away_score"`
	Sequence    int    `json:"sequence"`
	Status      string `json:"status"`
	AtUnixMilli int64  `json:"at_unix_ms"`

	Severity         string `json:"severity"`
	Stage            string `json:"stage"`
	Message          string `json:"message"`
	Attempt          string `json:"attempt"`
	Detail           string `json:"detail"`
	StaleSeconds     int    `json:"stale_seconds"`
	SuppressedEvents int    `json:"suppressed_events"`
}

func NewTotalCornerMonitor(processor *Processor, cfg TotalCornerConfig) *TotalCornerMonitor {
	cfg = normalizeTotalCornerConfig(cfg)
	return &TotalCornerMonitor{
		processor: processor,
		config:    cfg,
		now:       time.Now,
	}
}

func normalizeTotalCornerConfig(cfg TotalCornerConfig) TotalCornerConfig {
	if strings.TrimSpace(cfg.URL) == "" {
		cfg.URL = DefaultTotalCornerURL
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultTotalCornerPollInterval
	}
	if len(cfg.LeagueIDs) == 0 {
		cfg.LeagueIDs = []string{"29754"}
	}
	cfg.LeagueIDs = normalizeTotalCornerLeagueIDs(cfg.LeagueIDs)
	if len(cfg.Command) == 0 {
		cfg.Command = append([]string(nil), defaultTotalCornerCommand...)
	}
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = defaultTotalCornerRestartDelay
	}
	return cfg
}

func normalizeTotalCornerLeagueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 {
		return []string{"29754"}
	}
	return out
}

func (m *TotalCornerMonitor) Run(ctx context.Context) error {
	if m == nil || !m.config.Enabled {
		return nil
	}
	if m.processor == nil {
		return fmt.Errorf("totalcorner monitor missing oneverycorner processor")
	}
	for {
		if err := m.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("OnEveryCorner TotalCorner monitor process failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.config.RestartDelay):
			slog.Info("Restarting OnEveryCorner TotalCorner monitor")
		}
	}
}

func (m *TotalCornerMonitor) runOnce(ctx context.Context) error {
	args := append([]string(nil), m.config.Command...)
	if len(args) == 0 {
		return fmt.Errorf("totalcorner command is empty")
	}
	args = append(args,
		"--url", m.config.URL,
		"--poll-ms", strconv.Itoa(int(m.config.PollInterval.Milliseconds())),
		"--league-ids", strings.Join(m.config.LeagueIDs, ","),
	)

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open totalcorner stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open totalcorner stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start totalcorner monitor: %w", err)
	}

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		logTotalCornerStderr(stderr)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			slog.Info("TotalCorner monitor", "message", line)
			continue
		}
		var event TotalCornerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			slog.Warn("Ignoring malformed TotalCorner event", "line", line, "error", err)
			continue
		}
		if isTotalCornerSystemEvent(event.Type) {
			slog.Info("TotalCorner monitor status",
				"type", event.Type,
				"severity", event.Severity,
				"stage", event.Stage,
				"status", event.Status,
				"attempt", event.Attempt,
				"message", event.Message,
			)
			if err := m.processor.ProcessTotalCornerSystemEvent(ctx, event); err != nil {
				slog.Error("Failed to process TotalCorner system event", "event", event, "error", err)
			}
			continue
		}
		if err := m.processor.ProcessTotalCornerEvent(ctx, event); err != nil {
			slog.Error("Failed to process TotalCorner event", "event", event, "error", err)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-stderrDone
		return fmt.Errorf("read totalcorner stdout: %w", err)
	}

	err = cmd.Wait()
	<-stderrDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("totalcorner monitor exited: %w", err)
	}
	return nil
}

func logTotalCornerStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			slog.Info("TotalCorner monitor", "message", line)
		}
	}
}

func (p *Processor) ProcessTotalCornerEvent(ctx context.Context, event TotalCornerEvent) error {
	if p == nil {
		return nil
	}
	if normalizeTotalCornerEventType(event.Type) == "" {
		slog.Debug("Ignoring unsupported TotalCorner event type", "type", event.Type, "match_id", event.MatchID)
		return nil
	}
	now := time.Now()
	if p.timeSource != nil {
		now = p.timeSource()
	}
	return p.ProcessNotification(ctx, event.NotificationEvent(now))
}

func (p *Processor) ProcessTotalCornerSystemEvent(ctx context.Context, event TotalCornerEvent) error {
	if p == nil {
		return nil
	}
	if p.store == nil || p.notifier == nil {
		return fmt.Errorf("oneverycorner processor missing store or notifier")
	}
	now := time.Now()
	if p.timeSource != nil {
		now = p.timeSource()
	}
	receivedAt := now
	if event.AtUnixMilli > 0 {
		receivedAt = time.UnixMilli(event.AtUnixMilli).UTC()
	}

	eventID := fmt.Sprintf("totalcorner:%s:%d", strings.TrimSpace(event.Type), receivedAt.UnixMilli())
	fields := make([]models.CoreSystemAlertField, 0, 6)
	if event.Stage != "" {
		fields = append(fields, models.CoreSystemAlertField{Name: "Stage", Value: event.Stage})
	}
	if event.Attempt != "" {
		fields = append(fields, models.CoreSystemAlertField{Name: "Attempted fix", Value: event.Attempt})
	}
	if event.Status != "" {
		fields = append(fields, models.CoreSystemAlertField{Name: "Status", Value: event.Status})
	}
	if event.StaleSeconds > 0 {
		fields = append(fields, models.CoreSystemAlertField{Name: "Stale duration", Value: fmt.Sprintf("%ds", event.StaleSeconds)})
	}
	if event.SuppressedEvents > 0 {
		fields = append(fields, models.CoreSystemAlertField{Name: "Suppressed catch-up events", Value: strconv.Itoa(event.SuppressedEvents)})
	}
	if event.Detail != "" {
		fields = append(fields, models.CoreSystemAlertField{Name: "Detail", Value: event.Detail})
	}

	alert := models.OnEveryCornerAlert{
		Kind:           models.OnEveryCornerAlertSystem,
		MatchName:      "TotalCorner monitor",
		EventID:        eventID,
		StableID:       eventID,
		SourcePackage:  "totalcorner",
		SourceApp:      sourceAppName("totalcorner"),
		RawTitle:       totalCornerSystemTitle(event),
		RawText:        event.Message,
		ReceivedAt:     receivedAt,
		SystemSeverity: strings.TrimSpace(event.Severity),
		SystemDetails:  firstNonEmpty(event.Message, event.Detail),
		SystemFields:   fields,
	}
	return p.sendAlert(ctx, alert)
}

func (event TotalCornerEvent) NotificationEvent(now time.Time) NotificationEvent {
	receivedAt := now
	if event.AtUnixMilli > 0 {
		receivedAt = time.UnixMilli(event.AtUnixMilli).UTC()
	}
	matchName := event.normalizedMatchName()
	typeName := normalizeTotalCornerEventType(event.Type)
	title := "Corner"
	if typeName == eventTypeGoal {
		title = "Goal"
	}
	team := strings.TrimSpace(event.Team)
	if team != "" {
		title += " - " + team
	}

	lines := []string{
		matchName,
		fmt.Sprintf("Corners %d-%d", event.HomeCorners, event.AwayCorners),
		fmt.Sprintf("Score %d-%d", event.HomeScore, event.AwayScore),
	}
	if event.LeagueName != "" {
		lines = append(lines, event.LeagueName)
	}
	rawText := strings.Join(append([]string{title}, lines...), "\n")
	eventID := event.eventID(typeName)

	return NotificationEvent{
		EventID:       eventID,
		StableID:      eventID,
		SourcePackage: "totalcorner",
		MatchID:       strings.TrimSpace(event.MatchID),
		Team:          strings.TrimSpace(event.Team),
		HomeTeam:      strings.TrimSpace(event.HomeTeam),
		AwayTeam:      strings.TrimSpace(event.AwayTeam),
		Title:         title,
		Text:          rawText,
		BigText:       rawText,
		TickerText:    title + " - " + matchName,
		Lines:         lines,
		ReceivedAt:    receivedAt,
	}
}

func (event TotalCornerEvent) normalizedMatchName() string {
	if matchName := strings.TrimSpace(event.MatchName); usableScoremerMatchName(matchName) {
		return matchName
	}
	home := strings.TrimSpace(event.HomeTeam)
	away := strings.TrimSpace(event.AwayTeam)
	if home != "" && away != "" {
		return home + " v " + away
	}
	if home != "" {
		return home
	}
	if away != "" {
		return away
	}
	return "Unknown match"
}

func (event TotalCornerEvent) eventID(typeName string) string {
	if typeName == "" {
		typeName = normalizeTotalCornerEventType(event.Type)
	}
	matchID := strings.TrimSpace(event.MatchID)
	if matchID == "" {
		matchID = event.normalizedMatchName()
	}
	parts := []string{
		"totalcorner",
		typeName,
		matchID,
		strings.ToLower(strings.TrimSpace(event.Team)),
		strconv.Itoa(event.Sequence),
		strconv.Itoa(event.HomeCorners),
		strconv.Itoa(event.AwayCorners),
		strconv.Itoa(event.HomeScore),
		strconv.Itoa(event.AwayScore),
	}
	return strings.Join(parts, ":")
}

func normalizeTotalCornerEventType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case eventTypeCorner:
		return eventTypeCorner
	case eventTypeGoal:
		return eventTypeGoal
	default:
		return ""
	}
}

func isTotalCornerSystemEvent(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case totalCornerSystemIssue, totalCornerSystemFixAttempt, totalCornerSystemRecovered, totalCornerSystemFixFailed:
		return true
	default:
		return false
	}
}

func totalCornerSystemTitle(event TotalCornerEvent) string {
	switch strings.ToLower(strings.TrimSpace(event.Type)) {
	case totalCornerSystemIssue:
		return "OnEveryCorner TotalCorner issue"
	case totalCornerSystemFixAttempt:
		return "OnEveryCorner TotalCorner recovery attempted"
	case totalCornerSystemRecovered:
		return "OnEveryCorner TotalCorner recovered"
	case totalCornerSystemFixFailed:
		return "OnEveryCorner TotalCorner recovery failed"
	default:
		return "OnEveryCorner TotalCorner status"
	}
}
