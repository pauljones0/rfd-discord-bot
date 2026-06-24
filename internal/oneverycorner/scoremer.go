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
	DefaultScoremerURL          = "https://lv.scoremer.com/"
	DefaultScoremerPollInterval = 1 * time.Second
	defaultScoremerRestartDelay = 10 * time.Second

	scoremerSystemIssue      = "system_issue"
	scoremerSystemFixAttempt = "system_fix_attempt"
	scoremerSystemRecovered  = "system_recovered"
	scoremerSystemFixFailed  = "system_fix_failed"
)

var defaultScoremerCommand = []string{"xvfb-run", "-a", "/opt/scrape-venv/bin/python", "/root/scripts/scoremer_monitor.py"}

type ScoremerConfig struct {
	Enabled      bool
	URL          string
	LeagueIDs    []string
	PollInterval time.Duration
	Command      []string
	RestartDelay time.Duration
}

type ScoremerMonitor struct {
	processor *Processor
	config    ScoremerConfig
	now       func() time.Time
}

type ScoremerEvent struct {
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

func NewScoremerMonitor(processor *Processor, cfg ScoremerConfig) *ScoremerMonitor {
	cfg = normalizeScoremerConfig(cfg)
	return &ScoremerMonitor{
		processor: processor,
		config:    cfg,
		now:       time.Now,
	}
}

func normalizeScoremerConfig(cfg ScoremerConfig) ScoremerConfig {
	if strings.TrimSpace(cfg.URL) == "" {
		cfg.URL = DefaultScoremerURL
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultScoremerPollInterval
	}
	if len(cfg.LeagueIDs) == 0 {
		cfg.LeagueIDs = []string{"3559"}
	}
	cfg.LeagueIDs = normalizeScoremerLeagueIDs(cfg.LeagueIDs)
	if len(cfg.Command) == 0 {
		cfg.Command = append([]string(nil), defaultScoremerCommand...)
	}
	if cfg.RestartDelay <= 0 {
		cfg.RestartDelay = defaultScoremerRestartDelay
	}
	return cfg
}

func normalizeScoremerLeagueIDs(ids []string) []string {
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
		return []string{"3559"}
	}
	return out
}

func (m *ScoremerMonitor) Run(ctx context.Context) error {
	if m == nil || !m.config.Enabled {
		return nil
	}
	if m.processor == nil {
		return fmt.Errorf("scoremer monitor missing oneverycorner processor")
	}
	for {
		if err := m.runOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("OnEveryCorner Scoremer monitor process failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(m.config.RestartDelay):
			slog.Info("Restarting OnEveryCorner Scoremer monitor")
		}
	}
}

func (m *ScoremerMonitor) runOnce(ctx context.Context) error {
	args := append([]string(nil), m.config.Command...)
	if len(args) == 0 {
		return fmt.Errorf("scoremer command is empty")
	}
	args = append(args,
		"--url", m.config.URL,
		"--poll-ms", strconv.Itoa(int(m.config.PollInterval.Milliseconds())),
		"--league-ids", strings.Join(m.config.LeagueIDs, ","),
	)

	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	prepareMonitorCommand(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open scoremer stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("open scoremer stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start scoremer monitor: %w", err)
	}
	stopWatcher := watchMonitorContext(ctx, cmd)
	defer stopWatcher()

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		logScoremerStderr(stderr)
	}()

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "{") {
			slog.Info("Scoremer monitor", "message", line)
			continue
		}
		var event ScoremerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			slog.Warn("Ignoring malformed Scoremer event", "line", line, "error", err)
			continue
		}
		if isScoremerSystemEvent(event.Type) {
			slog.Info("Scoremer monitor status",
				"type", event.Type,
				"severity", event.Severity,
				"stage", event.Stage,
				"status", event.Status,
				"attempt", event.Attempt,
				"message", event.Message,
			)
			if err := m.processor.ProcessScoremerSystemEvent(ctx, event); err != nil {
				slog.Error("Failed to process Scoremer system event", "event", event, "error", err)
			}
			continue
		}
		if err := m.processor.ProcessScoremerEvent(ctx, event); err != nil {
			slog.Error("Failed to process Scoremer event", "event", event, "error", err)
		}
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		<-stderrDone
		return fmt.Errorf("read scoremer stdout: %w", err)
	}

	err = cmd.Wait()
	<-stderrDone
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("scoremer monitor exited: %w", err)
	}
	return nil
}

func logScoremerStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 16*1024), 256*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			slog.Info("Scoremer monitor", "message", line)
		}
	}
}

func (p *Processor) ProcessScoremerEvent(ctx context.Context, event ScoremerEvent) error {
	if p == nil {
		return nil
	}
	if normalizeScoremerEventType(event.Type) == "" {
		slog.Debug("Ignoring unsupported Scoremer event type", "type", event.Type, "match_id", event.MatchID)
		return nil
	}
	now := time.Now()
	if p.timeSource != nil {
		now = p.timeSource()
	}
	return p.ProcessNotification(ctx, event.NotificationEvent(now))
}

func (p *Processor) ProcessScoremerSystemEvent(ctx context.Context, event ScoremerEvent) error {
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

	eventID := fmt.Sprintf("scoremer:%s:%d", strings.TrimSpace(event.Type), receivedAt.UnixMilli())
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
		MatchName:      "Scoremer monitor",
		EventID:        eventID,
		StableID:       eventID,
		SourcePackage:  "scoremer",
		SourceApp:      sourceAppName("scoremer"),
		RawTitle:       scoremerSystemTitle(event),
		RawText:        event.Message,
		ReceivedAt:     receivedAt,
		SystemSeverity: strings.TrimSpace(event.Severity),
		SystemDetails:  firstNonEmpty(event.Message, event.Detail),
		SystemFields:   fields,
	}
	return p.sendAlert(ctx, alert)
}

func (event ScoremerEvent) NotificationEvent(now time.Time) NotificationEvent {
	receivedAt := now
	if event.AtUnixMilli > 0 {
		receivedAt = time.UnixMilli(event.AtUnixMilli).UTC()
	}
	matchName := event.normalizedMatchName()
	typeName := normalizeScoremerEventType(event.Type)
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
		SourcePackage: "scoremer",
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

func (event ScoremerEvent) normalizedMatchName() string {
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

func (event ScoremerEvent) eventID(typeName string) string {
	if typeName == "" {
		typeName = normalizeScoremerEventType(event.Type)
	}
	matchID := strings.TrimSpace(event.MatchID)
	if matchID == "" {
		matchID = event.normalizedMatchName()
	}
	parts := []string{
		"scoremer",
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

func normalizeScoremerEventType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case eventTypeCorner:
		return eventTypeCorner
	case eventTypeGoal:
		return eventTypeGoal
	default:
		return ""
	}
}

func isScoremerSystemEvent(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case scoremerSystemIssue, scoremerSystemFixAttempt, scoremerSystemRecovered, scoremerSystemFixFailed:
		return true
	default:
		return false
	}
}

func scoremerSystemTitle(event ScoremerEvent) string {
	switch strings.ToLower(strings.TrimSpace(event.Type)) {
	case scoremerSystemIssue:
		return "OnEveryCorner Scoremer issue"
	case scoremerSystemFixAttempt:
		return "OnEveryCorner Scoremer recovery attempted"
	case scoremerSystemRecovered:
		return "OnEveryCorner Scoremer recovered"
	case scoremerSystemFixFailed:
		return "OnEveryCorner Scoremer recovery failed"
	default:
		return "OnEveryCorner Scoremer status"
	}
}

func usableScoremerMatchName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	return !strings.EqualFold(value, "v")
}
