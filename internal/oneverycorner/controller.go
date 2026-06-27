package oneverycorner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultOnEveryCornerPrimarySource              = "totalcorner"
	DefaultOnEveryCornerBackupSource               = "scoremer"
	DefaultOnEveryCornerScheduleCachePath          = "/data/oneverycorner-schedule-cache.json"
	DefaultOnEveryCornerScheduleLookahead          = 36 * time.Hour
	DefaultOnEveryCornerScheduleRefreshInterval    = 15 * time.Minute
	DefaultOnEveryCornerPendingKickoffPollInterval = 30 * time.Second
	DefaultOnEveryCornerPendingKickoffTimeout      = time.Hour
	DefaultOnEveryCornerLivePollInterval           = 6 * time.Second
	DefaultOnEveryCornerPostLiveGracePeriod        = 10 * time.Minute
	DefaultOnEveryCornerScoremerBackupPollInterval = 10 * time.Second

	scheduleFailureAlertThreshold    = 15 * time.Minute
	scheduleFailureMinChecks         = 3
	scheduleFailureRepeatSuppression = 30 * time.Minute
	livePrimaryStaleThreshold        = 45 * time.Second
	livePrimaryFailureThreshold      = 3
	livePrimaryRecoveryWindow        = 2 * time.Minute
	estimatedMatchDuration           = 4 * time.Hour
)

type totalCornerInPlayDiagnostics interface {
	LastInPlayStats() TotalCornerInPlayStats
}

type ControllerConfig struct {
	Enabled                    bool
	PrimarySource              string
	BackupSources              []string
	ScheduleCachePath          string
	ScheduleLookahead          time.Duration
	ScheduleRefreshInterval    time.Duration
	PendingKickoffPollInterval time.Duration
	PendingKickoffTimeout      time.Duration
	LivePollInterval           time.Duration
	PostLiveGracePeriod        time.Duration
	TotalCornerAPI             TotalCornerAPIConfig
	Scoremer                   ScoremerConfig

	TotalCornerSource TotalCornerSource
}

type Controller struct {
	processor *Processor
	config    ControllerConfig
	primary   TotalCornerSource
	now       func() time.Time

	scoremerFactory func(*Processor, ScoremerConfig) *ScoremerMonitor
	tracker         *snapshotTracker

	scheduleFailureSince     time.Time
	scheduleFailureCount     int
	scheduleAlertActive      bool
	lastScheduleFailureAlert time.Time

	consecutivePrimaryFailures int
	lastLiveSnapshot           time.Time
	primaryHealthySince        time.Time
	backupCancel               context.CancelFunc
	backupDone                 chan struct{}
	backupActivatedAt          time.Time
}

type pendingKickoffState struct {
	matches   []MatchWindow
	startedAt time.Time
	deadline  time.Time
}

type scheduleCacheFile struct {
	Version          int           `json:"version"`
	GeneratedAt      time.Time     `json:"generated_at"`
	ExpiresAt        time.Time     `json:"expires_at"`
	LookaheadSeconds int64         `json:"lookahead_seconds"`
	Matches          []MatchWindow `json:"matches"`
}

func NewController(processor *Processor, cfg ControllerConfig) *Controller {
	cfg = normalizeControllerConfig(cfg)
	primary := cfg.TotalCornerSource
	if primary == nil {
		primary = NewTotalCornerAPIClient(cfg.TotalCornerAPI)
	}
	return &Controller{
		processor: processor,
		config:    cfg,
		primary:   primary,
		now:       time.Now,
		scoremerFactory: func(processor *Processor, cfg ScoremerConfig) *ScoremerMonitor {
			return NewScoremerMonitor(processor, cfg)
		},
		tracker: newSnapshotTracker(),
	}
}

func normalizeControllerConfig(cfg ControllerConfig) ControllerConfig {
	cfg.PrimarySource = strings.ToLower(strings.TrimSpace(firstNonEmpty(cfg.PrimarySource, DefaultOnEveryCornerPrimarySource)))
	cfg.BackupSources = normalizeSourceList(cfg.BackupSources, []string{DefaultOnEveryCornerBackupSource})
	if strings.TrimSpace(cfg.ScheduleCachePath) == "" {
		cfg.ScheduleCachePath = DefaultOnEveryCornerScheduleCachePath
	}
	if cfg.ScheduleLookahead <= 0 {
		cfg.ScheduleLookahead = DefaultOnEveryCornerScheduleLookahead
	}
	if cfg.ScheduleRefreshInterval <= 0 {
		cfg.ScheduleRefreshInterval = DefaultOnEveryCornerScheduleRefreshInterval
	}
	if cfg.PendingKickoffPollInterval <= 0 {
		cfg.PendingKickoffPollInterval = DefaultOnEveryCornerPendingKickoffPollInterval
	}
	if cfg.PendingKickoffTimeout <= 0 {
		cfg.PendingKickoffTimeout = DefaultOnEveryCornerPendingKickoffTimeout
	}
	if cfg.LivePollInterval <= 0 {
		cfg.LivePollInterval = DefaultOnEveryCornerLivePollInterval
	}
	if cfg.PostLiveGracePeriod <= 0 {
		cfg.PostLiveGracePeriod = DefaultOnEveryCornerPostLiveGracePeriod
	}
	cfg.TotalCornerAPI = normalizeTotalCornerAPIConfig(cfg.TotalCornerAPI)
	if cfg.Scoremer.PollInterval <= 0 {
		cfg.Scoremer.PollInterval = DefaultOnEveryCornerScoremerBackupPollInterval
	}
	cfg.Scoremer = normalizeScoremerConfig(cfg.Scoremer)
	return cfg
}

func normalizeSourceList(values []string, fallback []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	if len(out) == 0 {
		return append([]string(nil), fallback...)
	}
	return out
}

func (c *Controller) Run(ctx context.Context) error {
	if c == nil || !c.config.Enabled {
		return nil
	}
	if c.processor == nil {
		return fmt.Errorf("oneverycorner controller missing processor")
	}
	if c.config.PrimarySource != DefaultOnEveryCornerPrimarySource {
		err := fmt.Errorf("unsupported oneverycorner primary source %q", c.config.PrimarySource)
		slog.Error("source.error", "source", c.config.PrimarySource, "stage", "startup", "error", err)
		_ = c.emitTotalCornerSystem(ctx, totalCornerSystemIssue, "error", "startup", "unsupported_primary_source", "polling disabled", err.Error(), "")
		return waitForContext(ctx)
	}
	if c.config.TotalCornerSource == nil && strings.TrimSpace(c.config.TotalCornerAPI.Token) == "" {
		err := fmt.Errorf("ONEVERYCORNER_TOTALCORNER_API_TOKEN is not set")
		slog.Error("source.error", "source", "totalcorner", "stage", "startup", "error", err)
		_ = c.emitTotalCornerSystem(ctx, totalCornerSystemIssue, "error", "startup", "missing_totalcorner_api_token", "polling disabled", "OnEveryCorner polling is disabled because the TotalCorner API token is missing.", "")
		return waitForContext(ctx)
	}

	slog.Info("OnEveryCorner controller starting",
		"primary_source", c.config.PrimarySource,
		"backup_sources", c.config.BackupSources,
		"schedule_lookahead", c.config.ScheduleLookahead.String(),
		"live_poll_interval", c.config.LivePollInterval.String(),
	)

	schedule := c.loadScheduleCache(c.now())
	startupLiveChecked := false
	for {
		now := c.now()
		refreshed, err := c.refreshSchedule(ctx, now)
		if err != nil {
			c.noteScheduleFailure(ctx, now, err)
		} else {
			schedule = refreshed
			c.noteScheduleSuccess(ctx, now)
			c.saveScheduleCache(now, schedule)
		}

		if !startupLiveChecked {
			startupLiveChecked = true
			started, err := c.startLiveIfInPlay(ctx, schedule, "startup")
			if err != nil {
				return err
			}
			if started {
				continue
			}
		}

		if pending, ok := c.pendingKickoff(now, schedule); ok {
			if err := c.runPendingKickoff(ctx, pending); err != nil {
				return err
			}
			continue
		}

		if liveWindow, ok := c.scheduledLiveWindow(now, schedule); ok {
			sleepFor := c.config.PendingKickoffPollInterval
			slog.Info("schedule.live_window_retry",
				"matches", matchWindowNames(liveWindow.matches),
				"started_at", liveWindow.startedAt.Format(time.RFC3339),
				"deadline", liveWindow.deadline.Format(time.RFC3339),
				"sleep_for", sleepFor.String(),
			)
			if err := sleepContext(ctx, sleepFor); err != nil {
				return err
			}
			continue
		}

		sleepFor := c.scheduleSleepDuration(now, schedule)
		if next, ok := nextFutureMatch(now, schedule); ok {
			slog.Info("schedule.next_match",
				"match_id", next.ID,
				"match", matchDisplayName(next),
				"kickoff", next.Start.Format(time.RFC3339),
				"sleep_for", sleepFor.String(),
			)
		} else {
			slog.Info("schedule.idle", "sleep_for", sleepFor.String(), "lookahead", c.config.ScheduleLookahead.String())
		}
		if err := sleepContext(ctx, sleepFor); err != nil {
			return err
		}
	}
}

func (c *Controller) refreshSchedule(ctx context.Context, now time.Time) ([]MatchWindow, error) {
	from := now.Add(-c.config.PendingKickoffTimeout)
	through := now.Add(c.config.ScheduleLookahead)
	schedule, err := c.primary.Schedule(ctx, from, through)
	if err != nil {
		slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_discovery", "error", err)
		return nil, err
	}
	return filterScheduleWindow(schedule, from, through), nil
}

func (c *Controller) noteScheduleFailure(ctx context.Context, now time.Time, err error) {
	if c.scheduleFailureSince.IsZero() {
		c.scheduleFailureSince = now
	}
	c.scheduleFailureCount++
	if c.scheduleFailureCount < scheduleFailureMinChecks || now.Sub(c.scheduleFailureSince) < scheduleFailureAlertThreshold {
		return
	}
	if !c.lastScheduleFailureAlert.IsZero() && now.Sub(c.lastScheduleFailureAlert) < scheduleFailureRepeatSuppression {
		return
	}
	c.lastScheduleFailureAlert = now
	c.scheduleAlertActive = true
	_ = c.emitTotalCornerSystem(ctx,
		totalCornerSystemIssue,
		"warning",
		"schedule_discovery",
		fmt.Sprintf("%d failures over %s", c.scheduleFailureCount, now.Sub(c.scheduleFailureSince).Round(time.Second)),
		"using cached schedule if available",
		"TotalCorner schedule discovery has been failing, so OnEveryCorner may not see new kickoffs until it recovers.",
		err.Error(),
	)
}

func (c *Controller) noteScheduleSuccess(ctx context.Context, now time.Time) {
	wasAlerting := c.scheduleAlertActive
	c.scheduleFailureSince = time.Time{}
	c.scheduleFailureCount = 0
	c.scheduleAlertActive = false
	if wasAlerting {
		_ = c.emitTotalCornerSystem(ctx,
			totalCornerSystemRecovered,
			"info",
			"schedule_discovery",
			"schedule refreshed",
			"resumed normal schedule heartbeat",
			"TotalCorner schedule discovery recovered.",
			"",
		)
	}
}

func (c *Controller) startLiveIfInPlay(ctx context.Context, schedule []MatchWindow, stage string) (bool, error) {
	snapshots, err := c.primary.InPlay(ctx)
	now := c.now()
	if err != nil {
		c.notePrimaryFailure(ctx, stage, err, now)
		return false, nil
	}
	if len(snapshots) == 0 {
		return false, nil
	}
	slog.Info("pending_kickoff.live_detected", "stage", stage, "matches", snapshotNames(snapshots))
	if err := c.runLivePoll(ctx, snapshots, schedule, stage); err != nil {
		return true, err
	}
	return true, nil
}

func (c *Controller) runPendingKickoff(ctx context.Context, pending pendingKickoffState) error {
	slog.Info("pending_kickoff.start",
		"matches", matchWindowNames(pending.matches),
		"started_at", pending.startedAt.Format(time.RFC3339),
		"deadline", pending.deadline.Format(time.RFC3339),
		"poll_interval", c.config.PendingKickoffPollInterval.String(),
	)
	defer c.stopBackup("pending window ended")

	for {
		now := c.now()
		if now.After(pending.deadline) {
			slog.Info("live_poll.stop", "reason", "pending_kickoff_timeout", "matches", matchWindowNames(pending.matches))
			return nil
		}

		snapshots, err := c.primary.InPlay(ctx)
		now = c.now()
		if err != nil {
			c.notePrimaryFailure(ctx, "pending_kickoff", err, pending.startedAt)
		} else if len(snapshots) > 0 {
			c.notePrimaryLiveSuccess(now)
			slog.Info("pending_kickoff.live_detected", "matches", snapshotNames(snapshots))
			return c.runLivePoll(ctx, snapshots, pending.matches, "pending_kickoff")
		} else {
			c.notePrimaryNoLiveSnapshot(ctx, "pending_kickoff", pending.startedAt)
		}

		if err := sleepContext(ctx, c.config.PendingKickoffPollInterval); err != nil {
			return err
		}
	}
}

func (c *Controller) runLivePoll(ctx context.Context, initial []MatchSnapshot, schedule []MatchWindow, stage string) error {
	c.consecutivePrimaryFailures = 0
	c.primaryHealthySince = c.now()
	c.lastLiveSnapshot = c.now()
	c.resetPrimaryBaseline(ctx, "live_start", initial)

	hardDeadline := c.liveHardDeadline(c.now(), initial, schedule)
	var noSnapshotsSince time.Time
	slog.Info("live_poll.start",
		"stage", stage,
		"matches", snapshotNames(initial),
		"poll_interval", c.config.LivePollInterval.String(),
		"hard_deadline", hardDeadline.Format(time.RFC3339),
	)
	defer c.stopBackup("live poll stopped")

	for {
		if err := sleepContext(ctx, c.config.LivePollInterval); err != nil {
			return err
		}

		now := c.now()
		if now.After(hardDeadline) {
			slog.Info("live_poll.stop", "reason", "estimated_match_window_elapsed", "hard_deadline", hardDeadline.Format(time.RFC3339))
			return nil
		}

		snapshots, err := c.primary.InPlay(ctx)
		now = c.now()
		if err != nil {
			c.notePrimaryFailure(ctx, "live_poll", err, firstNonZeroTime(c.lastLiveSnapshot, now))
			continue
		}
		if len(snapshots) == 0 {
			if noSnapshotsSince.IsZero() {
				noSnapshotsSince = now
			}
			c.notePrimaryNoLiveSnapshot(ctx, "live_poll", firstNonZeroTime(c.lastLiveSnapshot, noSnapshotsSince))
			if now.Sub(noSnapshotsSince) >= c.config.PostLiveGracePeriod {
				slog.Info("live_poll.stop", "reason", "post_live_grace_elapsed", "grace_period", c.config.PostLiveGracePeriod.String())
				return nil
			}
			continue
		}

		noSnapshotsSince = time.Time{}
		c.notePrimaryLiveSuccess(now)
		if c.backupActive() {
			if !c.primaryHealthySince.IsZero() && now.Sub(c.primaryHealthySince) >= livePrimaryRecoveryWindow {
				c.resetPrimaryBaseline(ctx, "totalcorner_recovered", snapshots)
				c.recoverFailover(ctx)
			}
			continue
		}
		if err := c.processPrimarySnapshots(ctx, snapshots, false); err != nil {
			return err
		}
	}
}

func (c *Controller) notePrimaryFailure(ctx context.Context, stage string, err error, staleSince time.Time) {
	now := c.now()
	c.consecutivePrimaryFailures++
	c.primaryHealthySince = time.Time{}
	slog.Warn("source.error",
		"source", "totalcorner",
		"stage", stage,
		"consecutive_failures", c.consecutivePrimaryFailures,
		"error", err,
	)
	staleFor := time.Duration(0)
	if !staleSince.IsZero() {
		staleFor = now.Sub(staleSince)
	}
	if c.consecutivePrimaryFailures >= livePrimaryFailureThreshold || staleFor >= livePrimaryStaleThreshold {
		c.activateFailover(ctx, stage, "totalcorner_api_failure", err, int(staleFor.Round(time.Second).Seconds()), c.totalCornerInPlayDetail())
	}
}

func (c *Controller) notePrimaryNoLiveSnapshot(ctx context.Context, stage string, staleSince time.Time) {
	now := c.now()
	c.primaryHealthySince = time.Time{}
	staleFor := time.Duration(0)
	if !staleSince.IsZero() {
		staleFor = now.Sub(staleSince)
	}
	if staleFor >= livePrimaryStaleThreshold {
		c.activateFailover(ctx, stage, "no_totalcorner_live_snapshot", nil, int(staleFor.Round(time.Second).Seconds()), c.totalCornerInPlayDetail())
	}
}

func (c *Controller) notePrimaryLiveSuccess(now time.Time) {
	c.consecutivePrimaryFailures = 0
	c.lastLiveSnapshot = now
	if c.backupActive() {
		if c.primaryHealthySince.IsZero() {
			c.primaryHealthySince = now
		}
		return
	}
	c.primaryHealthySince = now
}

func (c *Controller) activateFailover(ctx context.Context, stage, reason string, cause error, staleSeconds int, extraDetails ...string) {
	if c.backupActive() {
		return
	}
	started, startErr := c.startBackup(ctx)
	eventType := totalCornerSystemFixAttempt
	severity := "warning"
	status := reason
	attempt := "scoremer backup started"
	message := "TotalCorner live polling is unhealthy; Scoremer backup has been started."
	detail := ""
	if cause != nil {
		detail = cause.Error()
	}
	for _, extra := range extraDetails {
		extra = strings.TrimSpace(extra)
		if extra == "" {
			continue
		}
		detail = strings.TrimSpace(strings.Join([]string{detail, extra}, "\n"))
	}
	if !started {
		eventType = totalCornerSystemIssue
		severity = "error"
		attempt = "no backup started"
		message = "TotalCorner live polling is unhealthy and no backup source could be started."
		if startErr != nil {
			detail = strings.TrimSpace(strings.Join([]string{detail, startErr.Error()}, " "))
		}
	}

	c.backupActivatedAt = c.now()
	slog.Warn("source.failover.activate",
		"primary", "totalcorner",
		"backup", DefaultOnEveryCornerBackupSource,
		"stage", stage,
		"reason", reason,
		"started", started,
		"stale_seconds", staleSeconds,
		"detail", detail,
	)
	_ = c.emitTotalCornerSystem(ctx, eventType, severity, stage, status, attempt, message, detail, withStaleSeconds(staleSeconds))
}

func (c *Controller) totalCornerInPlayDetail() string {
	if c == nil || c.primary == nil {
		return ""
	}
	diag, ok := c.primary.(totalCornerInPlayDiagnostics)
	if !ok {
		return ""
	}
	stats := diag.LastInPlayStats()
	if stats.CheckedAt.IsZero() {
		return ""
	}
	parts := []string{
		"last_inplay_check=" + stats.CheckedAt.UTC().Format(time.RFC3339),
		fmt.Sprintf("duration=%s", stats.Duration.Round(time.Millisecond)),
		fmt.Sprintf("api_rows=%d", stats.Rows),
		fmt.Sprintf("tracked_rows=%d", stats.Matched),
	}
	if len(stats.LeagueIDs) > 0 {
		parts = append(parts, "tracked_league_ids="+strings.Join(stats.LeagueIDs, ","))
	}
	if observed := formatLeagueCounts(stats.LeagueCounts, 8); observed != "" {
		parts = append(parts, "observed_leagues="+observed)
	}
	if stats.Error != "" {
		parts = append(parts, "last_error="+stats.Error)
	}
	return strings.Join(parts, " ")
}

func (c *Controller) recoverFailover(ctx context.Context) {
	if !c.backupActive() {
		return
	}
	c.stopBackup("totalcorner recovered")
	c.backupActivatedAt = time.Time{}
	slog.Info("source.failover.recover", "primary", "totalcorner", "backup", DefaultOnEveryCornerBackupSource)
	_ = c.emitTotalCornerSystem(ctx,
		totalCornerSystemRecovered,
		"info",
		"live_poll",
		"totalcorner healthy",
		"scoremer backup stopped",
		"TotalCorner live polling recovered and the Scoremer backup has been stopped.",
		"",
	)
}

func (c *Controller) startBackup(ctx context.Context) (bool, error) {
	if !c.backupAllowed(DefaultOnEveryCornerBackupSource) {
		return false, fmt.Errorf("scoremer is not configured as an OnEveryCorner backup source")
	}
	if c.scoremerFactory == nil {
		return false, fmt.Errorf("scoremer backup factory is missing")
	}
	backupCtx, cancel := context.WithCancel(ctx)
	cfg := c.config.Scoremer
	cfg.Enabled = true
	monitor := c.scoremerFactory(c.processor, cfg)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := monitor.Run(backupCtx); err != nil && backupCtx.Err() == nil {
			slog.Error("source.error", "source", "scoremer", "stage", "backup", "error", err)
		}
	}()
	c.backupCancel = cancel
	c.backupDone = done
	return true, nil
}

func (c *Controller) stopBackup(reason string) {
	if !c.backupActive() {
		return
	}
	cancel := c.backupCancel
	done := c.backupDone
	c.backupCancel = nil
	c.backupDone = nil
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		slog.Warn("source.error", "source", "scoremer", "stage", "backup_stop", "reason", reason, "error", "timed out waiting for backup to stop")
	}
}

func (c *Controller) backupActive() bool {
	return c != nil && c.backupCancel != nil
}

func (c *Controller) backupAllowed(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	for _, configured := range c.config.BackupSources {
		if configured == source {
			return true
		}
	}
	return false
}

func (c *Controller) resetPrimaryBaseline(ctx context.Context, reason string, snapshots []MatchSnapshot) {
	c.tracker.Reset()
	slog.Info("source.baseline_reset", "source", "totalcorner", "reason", reason, "matches", snapshotNames(snapshots))
	if err := c.processPrimarySnapshots(ctx, snapshots, true); err != nil {
		slog.Error("source.error", "source", "totalcorner", "stage", "baseline_reset", "error", err)
	}
}

func (c *Controller) processPrimarySnapshots(ctx context.Context, snapshots []MatchSnapshot, baselineOnly bool) error {
	now := c.now()
	for _, snapshot := range snapshots {
		events := c.tracker.Apply(snapshot, now, baselineOnly)
		for _, event := range events {
			if err := c.processor.ProcessTotalCornerEvent(ctx, event); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *Controller) emitTotalCornerSystem(ctx context.Context, eventType, severity, stage, status, attempt, message, detail string, opts ...func(*TotalCornerEvent)) error {
	event := TotalCornerEvent{
		Type:        eventType,
		Severity:    severity,
		Stage:       stage,
		Status:      status,
		Attempt:     attempt,
		Message:     message,
		Detail:      detail,
		AtUnixMilli: c.now().UnixMilli(),
	}
	for _, opt := range opts {
		opt(&event)
	}
	return c.processor.ProcessTotalCornerSystemEvent(ctx, event)
}

func withStaleSeconds(seconds int) func(*TotalCornerEvent) {
	return func(event *TotalCornerEvent) {
		if seconds > 0 {
			event.StaleSeconds = seconds
		}
	}
}

func (c *Controller) pendingKickoff(now time.Time, schedule []MatchWindow) (pendingKickoffState, bool) {
	matches := make([]MatchWindow, 0)
	var startedAt time.Time
	var deadline time.Time
	for _, match := range schedule {
		if match.Start.IsZero() || match.Start.After(now) {
			continue
		}
		matchDeadline := match.Start.Add(c.config.PendingKickoffTimeout)
		if now.After(matchDeadline) {
			continue
		}
		matches = append(matches, match)
		if startedAt.IsZero() || match.Start.Before(startedAt) {
			startedAt = match.Start
		}
		if deadline.IsZero() || matchDeadline.After(deadline) {
			deadline = matchDeadline
		}
	}
	if len(matches) == 0 {
		return pendingKickoffState{}, false
	}
	return pendingKickoffState{matches: matches, startedAt: startedAt, deadline: deadline}, true
}

func (c *Controller) scheduledLiveWindow(now time.Time, schedule []MatchWindow) (pendingKickoffState, bool) {
	matches := make([]MatchWindow, 0)
	var startedAt time.Time
	var deadline time.Time
	for _, match := range schedule {
		if match.Start.IsZero() || match.Start.After(now) {
			continue
		}
		matchDeadline := match.Start.Add(estimatedMatchDuration).Add(c.config.PostLiveGracePeriod)
		if now.After(matchDeadline) {
			continue
		}
		matches = append(matches, match)
		if startedAt.IsZero() || match.Start.Before(startedAt) {
			startedAt = match.Start
		}
		if deadline.IsZero() || matchDeadline.After(deadline) {
			deadline = matchDeadline
		}
	}
	if len(matches) == 0 {
		return pendingKickoffState{}, false
	}
	return pendingKickoffState{matches: matches, startedAt: startedAt, deadline: deadline}, true
}

func (c *Controller) scheduleSleepDuration(now time.Time, schedule []MatchWindow) time.Duration {
	sleepFor := c.config.ScheduleRefreshInterval
	if next, ok := nextFutureMatch(now, schedule); ok {
		until := next.Start.Sub(now)
		if until < 0 {
			return 0
		}
		if until < sleepFor {
			sleepFor = until
		}
	}
	if sleepFor < time.Second {
		return time.Second
	}
	return sleepFor
}

func (c *Controller) liveHardDeadline(now time.Time, snapshots []MatchSnapshot, schedule []MatchWindow) time.Time {
	deadline := now.Add(estimatedMatchDuration)
	ids := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if snapshot.ID != "" {
			ids[snapshot.ID] = struct{}{}
		}
		if !snapshot.Start.IsZero() {
			candidate := snapshot.Start.Add(estimatedMatchDuration)
			if candidate.After(deadline) {
				deadline = candidate
			}
		}
	}
	for _, match := range schedule {
		if len(ids) > 0 {
			if _, ok := ids[match.ID]; !ok {
				continue
			}
		}
		if !match.Start.IsZero() {
			candidate := match.Start.Add(estimatedMatchDuration)
			if candidate.After(deadline) {
				deadline = candidate
			}
		}
	}
	return deadline.Add(c.config.PostLiveGracePeriod)
}

func (c *Controller) loadScheduleCache(now time.Time) []MatchWindow {
	path := strings.TrimSpace(c.config.ScheduleCachePath)
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_cache_read", "error", err)
		}
		return nil
	}
	var cache scheduleCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil {
		slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_cache_parse", "error", err)
		return nil
	}
	if !cache.ExpiresAt.IsZero() && !cache.ExpiresAt.After(now) {
		return nil
	}
	return filterScheduleWindow(cache.Matches, now.Add(-c.config.PendingKickoffTimeout), now.Add(c.config.ScheduleLookahead))
}

func (c *Controller) saveScheduleCache(now time.Time, schedule []MatchWindow) {
	path := strings.TrimSpace(c.config.ScheduleCachePath)
	if path == "" {
		return
	}
	cache := scheduleCacheFile{
		Version:          1,
		GeneratedAt:      now,
		ExpiresAt:        now.Add(c.config.ScheduleLookahead),
		LookaheadSeconds: int64(c.config.ScheduleLookahead.Seconds()),
		Matches:          schedule,
	}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_cache_encode", "error", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_cache_mkdir", "error", err)
		return
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		slog.Warn("source.error", "source", "totalcorner", "stage", "schedule_cache_write", "error", err)
	}
}

type snapshotCounters struct {
	HomeCorners int
	AwayCorners int
	HomeScore   int
	AwayScore   int
}

type snapshotTracker struct {
	state map[string]snapshotCounters
}

func newSnapshotTracker() *snapshotTracker {
	return &snapshotTracker{state: make(map[string]snapshotCounters)}
}

func (t *snapshotTracker) Reset() {
	t.state = make(map[string]snapshotCounters)
}

func (t *snapshotTracker) Apply(snapshot MatchSnapshot, now time.Time, baselineOnly bool) []TotalCornerEvent {
	if t.state == nil {
		t.state = make(map[string]snapshotCounters)
	}
	key := snapshotKey(snapshot)
	current := snapshotCounters{
		HomeCorners: snapshot.HomeCorners,
		AwayCorners: snapshot.AwayCorners,
		HomeScore:   snapshot.HomeScore,
		AwayScore:   snapshot.AwayScore,
	}
	previous, ok := t.state[key]
	if !ok || baselineOnly || countersRegressed(previous, current) {
		t.state[key] = current
		return nil
	}

	events := make([]TotalCornerEvent, 0, 4)
	for seq := previous.HomeCorners + 1; seq <= current.HomeCorners; seq++ {
		event := totalCornerEventFromSnapshot(snapshot, eventTypeCorner, "home", seq, now)
		event.HomeCorners = seq
		events = append(events, event)
	}
	for seq := previous.AwayCorners + 1; seq <= current.AwayCorners; seq++ {
		event := totalCornerEventFromSnapshot(snapshot, eventTypeCorner, "away", seq, now)
		event.AwayCorners = seq
		events = append(events, event)
	}
	for seq := previous.HomeScore + 1; seq <= current.HomeScore; seq++ {
		event := totalCornerEventFromSnapshot(snapshot, eventTypeGoal, "home", seq, now)
		event.HomeScore = seq
		events = append(events, event)
	}
	for seq := previous.AwayScore + 1; seq <= current.AwayScore; seq++ {
		event := totalCornerEventFromSnapshot(snapshot, eventTypeGoal, "away", seq, now)
		event.AwayScore = seq
		events = append(events, event)
	}
	t.state[key] = current
	return events
}

func totalCornerEventFromSnapshot(snapshot MatchSnapshot, eventType, team string, sequence int, now time.Time) TotalCornerEvent {
	return TotalCornerEvent{
		Type:        eventType,
		MatchID:     snapshot.ID,
		MatchName:   matchDisplayName(snapshot.MatchWindow),
		LeagueID:    snapshot.LeagueID,
		LeagueName:  snapshot.LeagueName,
		HomeTeam:    snapshot.HomeTeam,
		AwayTeam:    snapshot.AwayTeam,
		Team:        team,
		HomeCorners: snapshot.HomeCorners,
		AwayCorners: snapshot.AwayCorners,
		HomeScore:   snapshot.HomeScore,
		AwayScore:   snapshot.AwayScore,
		Sequence:    sequence,
		Status:      snapshot.Status,
		AtUnixMilli: now.UnixMilli(),
	}
}

func countersRegressed(previous, current snapshotCounters) bool {
	return current.HomeCorners < previous.HomeCorners ||
		current.AwayCorners < previous.AwayCorners ||
		current.HomeScore < previous.HomeScore ||
		current.AwayScore < previous.AwayScore
}

func snapshotKey(snapshot MatchSnapshot) string {
	key := strings.TrimSpace(snapshot.ID)
	if key != "" {
		return key
	}
	return normalizeMatchKey(matchDisplayName(snapshot.MatchWindow))
}

func filterScheduleWindow(schedule []MatchWindow, from, through time.Time) []MatchWindow {
	out := make([]MatchWindow, 0, len(schedule))
	for _, match := range schedule {
		if match.Start.IsZero() || match.Start.Before(from) || match.Start.After(through) {
			continue
		}
		out = append(out, match)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start.Equal(out[j].Start) {
			return out[i].ID < out[j].ID
		}
		return out[i].Start.Before(out[j].Start)
	})
	return dedupeMatchWindows(out)
}

func nextFutureMatch(now time.Time, schedule []MatchWindow) (MatchWindow, bool) {
	for _, match := range schedule {
		if match.Start.After(now) {
			return match, true
		}
	}
	return MatchWindow{}, false
}

func matchDisplayName(match MatchWindow) string {
	home := strings.TrimSpace(match.HomeTeam)
	away := strings.TrimSpace(match.AwayTeam)
	if home != "" && away != "" {
		return home + " v " + away
	}
	return firstNonEmpty(home, away, "Unknown match")
}

func matchWindowNames(matches []MatchWindow) []string {
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		names = append(names, matchDisplayName(match))
	}
	return names
}

func snapshotNames(snapshots []MatchSnapshot) []string {
	names := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		names = append(names, matchDisplayName(snapshot.MatchWindow))
	}
	return names
}

func formatLeagueCounts(counts map[string]int, limit int) string {
	if len(counts) == 0 || limit <= 0 {
		return ""
	}
	type leagueCount struct {
		id    string
		count int
	}
	values := make([]leagueCount, 0, len(counts))
	for id, count := range counts {
		id = strings.TrimSpace(id)
		if id == "" || count <= 0 {
			continue
		}
		values = append(values, leagueCount{id: id, count: count})
	}
	if len(values) == 0 {
		return ""
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].count == values[j].count {
			return values[i].id < values[j].id
		}
		return values[i].count > values[j].count
	})
	if len(values) > limit {
		values = values[:limit]
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%s:%d", value.id, value.count))
	}
	return strings.Join(parts, ",")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		d = time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func waitForContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}
