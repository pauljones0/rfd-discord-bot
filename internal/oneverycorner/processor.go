package oneverycorner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	TweetMention = "@Enterprise"
	TweetTag     = "#OnEveryCorner"
	TweetText    = TweetMention + " " + TweetTag + " #Sweepstakes"
	TweetURL     = "https://x.com/intent/tweet?text=%40Enterprise+%23OnEveryCorner+%23Sweepstakes"

	DefaultGoalCorrelationWindow = 75 * time.Second
	DefaultSystemAlertCooldown   = 15 * time.Minute
)

// Auto-posting implementation note:
//
// This package intentionally stops at alerting and operator-assisted compose
// links. A future owner evaluating real account posting outside this code path
// would need to solve account/session custody, explicit contest and platform
// compliance, event-to-post idempotency, durable audit logs for every attempted
// entry, reliable post verification, browser/app failure handling, rate limits,
// and immediate disable/rollback controls so a bad parser cannot submit posts.
// Those are product and operational controls, not parsing concerns, so the live
// bot should keep this path limited to Discord notifications and compose URLs.

var (
	allowedSweepstakesTags = []string{"#Sweepstakes", "#Sorteo", "#Gewinnspiele", "#Jeu", "#Concours"}

	tweetVariantEmojiGroups = []string{"⚽", "🔥", "🚨", "🥅", "👀", "🎯", "🙌"}
)

type NotificationEvent struct {
	EventID       string
	StableID      string
	SourcePackage string
	MatchID       string
	Team          string
	HomeTeam      string
	AwayTeam      string
	Title         string
	Text          string
	BigText       string
	TickerText    string
	Lines         []string
	ReceivedAt    time.Time
}

type Store interface {
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

type Notifier interface {
	SendOnEveryCornerAlert(ctx context.Context, alert models.OnEveryCornerAlert, subs []models.Subscription) error
}

type Processor struct {
	store    Store
	notifier Notifier

	mu           sync.Mutex
	corners      map[string]cornerState
	goalAlerts   map[string]time.Time
	systemAlerts map[string]time.Time
	seen         map[string]time.Time
	seenOrder    []string
	maxSeen      int
	window       time.Duration
	timeSource   func() time.Time
}

type cornerState struct {
	MatchName  string
	ReceivedAt time.Time
	EventID    string
	StableID   string
	RawText    string
}

type parsedEvent struct {
	Type        string
	MatchName   string
	MatchKey    string
	MatchID     string
	ScoringSide string
	ScoringTeam string
	Score       string
	CornerScore string
	RawText     string
}

const (
	eventTypeCorner = "corner"
	eventTypeGoal   = "goal"
)

var (
	goalPattern        = regexp.MustCompile(`(?i)\b(go+al|scored|scores|equalis(?:e|z)|takes?\s+the\s+lead)\b`)
	cornerPattern      = regexp.MustCompile(`(?i)\bcorner(?:\s+kick)?s?\b`)
	versusMatchPattern = regexp.MustCompile(`(?i)^(.+?)\s+(?:v|vs\.?|versus|at)\s+(.+?)(?:\s+\d{1,3}(?:\+\d+)?\s*min\b|$)`)
	scorelinePattern   = regexp.MustCompile(`(?i)^(.+?)\s+\[?\d+\]?\s*-\s*\[?\d+\]?\s+(.+)$`)
	metricLinePattern  = regexp.MustCompile(`(?i)^(corners?|score)\s+(\d+)\s*[-:]\s*(\d+)\b`)
	minutePattern      = regexp.MustCompile(`(?i)\s+\d{1,3}(?:\+\d+)?\s*min\b.*$`)
	eventPrefixPattern = regexp.MustCompile(`(?i)\b(corner(?:\s+kick)?|go+al|goal\s+alert|score\s+update|football|soccer|fifa|world\s+cup|notification|alert)\b`)
	spacePattern       = regexp.MustCompile(`\s+`)
	nonKeyPattern      = regexp.MustCompile(`[^a-z0-9]+`)
)

func NewProcessor(store Store, notifier Notifier) *Processor {
	return &Processor{
		store:        store,
		notifier:     notifier,
		corners:      make(map[string]cornerState),
		goalAlerts:   make(map[string]time.Time),
		systemAlerts: make(map[string]time.Time),
		seen:         make(map[string]time.Time),
		maxSeen:      512,
		window:       DefaultGoalCorrelationWindow,
		timeSource:   time.Now,
	}
}

func (p *Processor) ProcessNotification(ctx context.Context, event NotificationEvent) error {
	if p == nil {
		return nil
	}
	if p.store == nil || p.notifier == nil {
		return fmt.Errorf("oneverycorner processor missing store or notifier")
	}

	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = p.timeSource()
	}
	dedupeID := firstNonEmpty(event.StableID, event.EventID, eventHash(event))
	if !p.markSeen(dedupeID, event.ReceivedAt) {
		slog.Debug("OnEveryCorner notification skipped as duplicate", "event_id", event.EventID, "stable_id", event.StableID)
		return nil
	}

	parsed, ok := ParseNotification(event)
	if !ok {
		slog.Debug("OnEveryCorner notification ignored", "event_id", event.EventID, "source_package", event.SourcePackage, "raw", eventRawText(event))
		return nil
	}

	switch parsed.Type {
	case eventTypeCorner:
		alert := p.recordCorner(event, parsed)
		return p.sendAlert(ctx, alert)
	case eventTypeGoal:
		alert, ok, reason := p.correlateGoal(event, parsed)
		if !ok {
			slog.Info("OnEveryCorner goal notification did not produce alert", "match", parsed.MatchName, "event_id", event.EventID, "reason", reason)
			return nil
		}
		return p.sendAlert(ctx, alert)
	default:
		return nil
	}
}

func ParseNotification(event NotificationEvent) (parsedEvent, bool) {
	raw := eventRawText(event)
	if raw == "" {
		return parsedEvent{}, false
	}
	eventType := detectEventType(event)
	if eventType == "" {
		return parsedEvent{}, false
	}

	matchName := extractMatchName(event)
	matchKey := sourceMatchKey(event)
	if matchKey == "" {
		matchKey = normalizeMatchKey(matchName)
	}
	if matchKey == "" {
		matchKey = normalizeMatchKey(firstNonEmpty(event.Title, event.BigText, event.Text, event.TickerText, raw))
	}
	if matchKey == "" {
		matchName = "Unknown match"
		matchKey = "unknown"
	}
	cornerScore, score := extractScoreContext(event)

	return parsedEvent{
		Type:        eventType,
		MatchName:   matchName,
		MatchKey:    matchKey,
		MatchID:     strings.TrimSpace(event.MatchID),
		ScoringSide: scoringSide(event),
		ScoringTeam: scoringTeam(event),
		Score:       score,
		CornerScore: cornerScore,
		RawText:     raw,
	}, true
}

func (p *Processor) recordCorner(event NotificationEvent, parsed parsedEvent) models.OnEveryCornerAlert {
	state := cornerState{
		MatchName:  parsed.MatchName,
		ReceivedAt: event.ReceivedAt,
		EventID:    event.EventID,
		StableID:   event.StableID,
		RawText:    parsed.RawText,
	}
	p.mu.Lock()
	p.corners[parsed.MatchKey] = state
	if alias := matchAliasKey(parsed.MatchName); alias != "" && alias != parsed.MatchKey {
		p.corners[alias] = state
	}
	p.mu.Unlock()

	variantTweetText := ComposeCornerVariantTweetTextForContext(parsed.ScoringTeam, event.StableID, event.EventID, parsed.MatchKey)

	return models.OnEveryCornerAlert{
		Kind:             models.OnEveryCornerAlertCorner,
		MatchName:        parsed.MatchName,
		Score:            parsed.Score,
		CornerScore:      parsed.CornerScore,
		ScoringSide:      parsed.ScoringSide,
		ScoringTeam:      parsed.ScoringTeam,
		EventID:          event.EventID,
		StableID:         event.StableID,
		SourcePackage:    event.SourcePackage,
		SourceApp:        sourceAppName(event.SourcePackage),
		RawTitle:         event.Title,
		RawText:          parsed.RawText,
		Lines:            event.Lines,
		ReceivedAt:       event.ReceivedAt,
		CornerAt:         event.ReceivedAt,
		TweetText:        variantTweetText,
		TweetURL:         ComposeURL(variantTweetText),
		VariantTweetText: variantTweetText,
		VariantTweetURL:  ComposeURL(variantTweetText),
	}
}

func (p *Processor) correlateGoal(event NotificationEvent, parsed parsedEvent) (models.OnEveryCornerAlert, bool, string) {
	p.mu.Lock()
	state, ok := p.corners[parsed.MatchKey]
	if !ok {
		if alias := matchAliasKey(parsed.MatchName); alias != "" {
			state, ok = p.corners[alias]
		}
	}
	p.mu.Unlock()
	if !ok {
		return models.OnEveryCornerAlert{}, false, "no_recent_corner"
	}
	elapsed := event.ReceivedAt.Sub(state.ReceivedAt)
	if elapsed < 0 || elapsed > p.window {
		return models.OnEveryCornerAlert{}, false, "outside_correlation_window"
	}

	variantTweetText := ComposeGoalVariantTweetTextForContext(parsed.ScoringTeam, parsed.Score, event.StableID, event.EventID, parsed.MatchKey)

	alert := models.OnEveryCornerAlert{
		Kind:               models.OnEveryCornerAlertPossibleCornerGoal,
		MatchName:          firstNonEmpty(parsed.MatchName, state.MatchName),
		Score:              parsed.Score,
		CornerScore:        parsed.CornerScore,
		ScoringSide:        parsed.ScoringSide,
		ScoringTeam:        parsed.ScoringTeam,
		EventID:            event.EventID,
		StableID:           event.StableID,
		SourcePackage:      event.SourcePackage,
		SourceApp:          sourceAppName(event.SourcePackage),
		RawTitle:           event.Title,
		RawText:            parsed.RawText,
		Lines:              event.Lines,
		ReceivedAt:         event.ReceivedAt,
		CornerAt:           state.ReceivedAt,
		GoalAt:             event.ReceivedAt,
		SecondsAfterCorner: int(elapsed.Round(time.Second).Seconds()),
		TweetText:          variantTweetText,
		TweetURL:           ComposeURL(variantTweetText),
		VariantTweetText:   variantTweetText,
		VariantTweetURL:    ComposeURL(variantTweetText),
	}
	if !p.markGoalAlert(alert) {
		return models.OnEveryCornerAlert{}, false, "duplicate_goal_alert"
	}
	return alert, true, ""
}

func (p *Processor) sendAlert(ctx context.Context, alert models.OnEveryCornerAlert) error {
	if !p.markSystemAlert(alert) {
		slog.Info("Suppressed repeated OnEveryCorner system alert",
			"source", alert.SourcePackage,
			"title", alert.RawTitle,
			"severity", alert.SystemSeverity,
		)
		return nil
	}
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("load oneverycorner subscriptions: %w", err)
	}
	filtered := filterSubscriptions(alert.Kind, subs)
	if len(filtered) == 0 {
		slog.Info("No OnEveryCorner subscriptions configured", "kind", alert.Kind, "match", alert.MatchName)
		return nil
	}
	return p.notifier.SendOnEveryCornerAlert(ctx, alert, filtered)
}

func (p *Processor) markSystemAlert(alert models.OnEveryCornerAlert) bool {
	if alert.Kind != models.OnEveryCornerAlertSystem {
		return true
	}
	seenAt := alert.ReceivedAt
	if seenAt.IsZero() {
		seenAt = time.Now()
		if p != nil && p.timeSource != nil {
			seenAt = p.timeSource()
		}
	}
	source := strings.TrimSpace(alert.SourcePackage)
	if strings.Contains(strings.ToLower(alert.RawTitle), "recovered") {
		p.mu.Lock()
		defer p.mu.Unlock()
		for key := range p.systemAlerts {
			if strings.HasPrefix(key, source+"|") {
				delete(p.systemAlerts, key)
			}
		}
		return true
	}
	key := systemAlertKey(alert)
	if key == "" {
		return true
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.systemAlerts == nil {
		p.systemAlerts = make(map[string]time.Time)
	}
	for existingKey, at := range p.systemAlerts {
		if seenAt.Sub(at) > DefaultSystemAlertCooldown {
			delete(p.systemAlerts, existingKey)
		}
	}
	if at, ok := p.systemAlerts[key]; ok && seenAt.Sub(at) <= DefaultSystemAlertCooldown {
		return false
	}
	p.systemAlerts[key] = seenAt
	return true
}

func systemAlertKey(alert models.OnEveryCornerAlert) string {
	parts := []string{
		strings.ToLower(strings.TrimSpace(alert.SourcePackage)),
		strings.ToLower(strings.TrimSpace(alert.RawTitle)),
		strings.ToLower(strings.TrimSpace(alert.SystemSeverity)),
		strings.ToLower(strings.TrimSpace(systemAlertField(alert, "Stage"))),
		strings.ToLower(strings.TrimSpace(systemAlertField(alert, "Status"))),
		strings.ToLower(strings.TrimSpace(systemAlertField(alert, "Attempted fix"))),
	}
	return strings.Join(parts, "|")
}

func systemAlertField(alert models.OnEveryCornerAlert, name string) string {
	for _, field := range alert.SystemFields {
		if strings.EqualFold(strings.TrimSpace(field.Name), name) {
			return field.Value
		}
	}
	return ""
}

func filterSubscriptions(alertKind string, subs []models.Subscription) []models.Subscription {
	filtered := make([]models.Subscription, 0, len(subs))
	seenChannels := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		if !sub.IsOnEveryCorner() {
			continue
		}
		if !subscriptionAllowsOnEveryCornerAlert(sub.DealType, alertKind) {
			continue
		}
		if _, ok := seenChannels[sub.ChannelID]; ok {
			continue
		}
		seenChannels[sub.ChannelID] = struct{}{}
		filtered = append(filtered, sub)
	}
	return filtered
}

func subscriptionAllowsOnEveryCornerAlert(dealType, alertKind string) bool {
	if alertKind == models.OnEveryCornerAlertSystem {
		return true
	}
	switch strings.TrimSpace(dealType) {
	case "", dealtypes.OnEveryCornerAlerts, dealtypes.OnEveryCornerPotentialGoals:
		return alertKind == models.OnEveryCornerAlertPossibleCornerGoal
	default:
		return false
	}
}

func (p *Processor) markSeen(id string, seenAt time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.seen[id]; ok {
		return false
	}
	if p.seen == nil {
		p.seen = make(map[string]time.Time)
	}
	p.seen[id] = seenAt
	p.seenOrder = append(p.seenOrder, id)
	for len(p.seenOrder) > p.maxSeen {
		oldest := p.seenOrder[0]
		p.seenOrder = p.seenOrder[1:]
		delete(p.seen, oldest)
	}
	return true
}

func (p *Processor) markGoalAlert(alert models.OnEveryCornerAlert) bool {
	key := goalAlertKey(alert)
	if key == "" {
		return true
	}
	seenAt := alert.ReceivedAt
	if seenAt.IsZero() {
		seenAt = time.Now()
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.goalAlerts == nil {
		p.goalAlerts = make(map[string]time.Time)
	}
	for existingKey, at := range p.goalAlerts {
		if seenAt.Sub(at) > 10*time.Minute {
			delete(p.goalAlerts, existingKey)
		}
	}
	if at, ok := p.goalAlerts[key]; ok && seenAt.Sub(at) <= 10*time.Minute {
		return false
	}
	p.goalAlerts[key] = seenAt
	return true
}

func goalAlertKey(alert models.OnEveryCornerAlert) string {
	matchKey := matchAliasKey(alert.MatchName)
	score := strings.TrimSpace(alert.Score)
	if matchKey == "" || score == "" {
		return ""
	}
	side := strings.ToLower(strings.TrimSpace(firstNonEmpty(alert.ScoringSide, alert.ScoringTeam)))
	return strings.Join([]string{matchKey, score, side}, "|")
}

func eventRawText(event NotificationEvent) string {
	lines := append([]string{}, event.BigText, event.Text, event.TickerText, event.Title)
	lines = append(lines, event.Lines...)
	return strings.Join(uniqueNonEmpty(lines), "\n")
}

func detectEventType(event NotificationEvent) string {
	lines := notificationLines(event)
	for _, line := range lines {
		if isMetricLine(line) {
			continue
		}
		if goalPattern.MatchString(line) {
			return eventTypeGoal
		}
	}
	for _, line := range lines {
		if isMetricLine(line) {
			continue
		}
		if cornerPattern.MatchString(line) {
			return eventTypeCorner
		}
	}
	return ""
}

func extractScoreContext(event NotificationEvent) (cornerScore string, score string) {
	for _, line := range notificationLines(event) {
		match := metricLinePattern.FindStringSubmatch(strings.TrimSpace(line))
		if len(match) != 4 {
			continue
		}
		value := match[2] + "-" + match[3]
		switch strings.ToLower(match[1]) {
		case "corner", "corners":
			if cornerScore == "" {
				cornerScore = value
			}
		case "score":
			if score == "" {
				score = value
			}
		}
	}
	return cornerScore, score
}

func extractMatchName(event NotificationEvent) string {
	candidates := notificationLines(event)

	for _, candidate := range candidates {
		if isMetricLine(candidate) {
			continue
		}
		if match := versusMatchPattern.FindStringSubmatch(candidate); len(match) == 3 {
			home := cleanTeamName(match[1])
			away := cleanTeamName(match[2])
			if home != "" && away != "" {
				return home + " v " + away
			}
		}
	}
	for _, candidate := range candidates {
		if isMetricLine(candidate) {
			continue
		}
		if match := scorelinePattern.FindStringSubmatch(candidate); len(match) == 3 {
			home := cleanTeamName(match[1])
			away := cleanTeamName(match[2])
			if home != "" && away != "" {
				return home + " v " + away
			}
		}
	}
	for _, candidate := range candidates {
		if isMetricLine(candidate) {
			continue
		}
		cleaned := cleanMatchName(candidate)
		if unusableMatchName(cleaned) {
			continue
		}
		if strings.Contains(strings.ToLower(cleaned), "corner") || strings.Contains(strings.ToLower(cleaned), "goal") {
			continue
		}
		return cleaned
	}
	return "Unknown match"
}

func cleanMatchName(value string) string {
	value = strings.TrimSpace(value)
	if isMetricLine(value) {
		return ""
	}
	if idx := strings.LastIndex(value, ":"); idx >= 0 && idx < len(value)-1 {
		value = value[idx+1:]
	}
	value = minutePattern.ReplaceAllString(value, "")
	value = eventPrefixPattern.ReplaceAllString(value, " ")
	value = strings.NewReplacer(" - ", " ", "|", " ", "  ", " ").Replace(value)
	value = strings.Trim(value, " \t\r\n:-|")
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func cleanTeamName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "[", "")
	value = strings.ReplaceAll(value, "]", "")
	value = minutePattern.ReplaceAllString(value, "")
	value = strings.Trim(value, " \t\r\n:-|")
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func normalizeMatchKey(value string) string {
	value = strings.ToLower(cleanMatchName(value))
	value = nonKeyPattern.ReplaceAllString(value, " ")
	value = spacePattern.ReplaceAllString(value, " ")
	return strings.TrimSpace(value)
}

func matchAliasKey(matchName string) string {
	key := normalizeMatchKey(matchName)
	switch key {
	case "", "unknown", "unknown match", "v":
		return ""
	default:
		return key
	}
}

func sourceMatchKey(event NotificationEvent) string {
	source := strings.ToLower(strings.TrimSpace(event.SourcePackage))
	matchID := strings.TrimSpace(event.MatchID)
	if matchID == "" {
		return ""
	}
	switch source {
	case "scoremer", "totalcorner":
	default:
		return ""
	}
	matchID = strings.ToLower(matchID)
	matchID = nonKeyPattern.ReplaceAllString(matchID, " ")
	matchID = spacePattern.ReplaceAllString(matchID, " ")
	matchID = strings.TrimSpace(matchID)
	if matchID == "" {
		return ""
	}
	return source + ":" + matchID
}

func scoringSide(event NotificationEvent) string {
	side := strings.ToLower(strings.TrimSpace(event.Team))
	switch side {
	case "home", "away":
		return side
	}
	title := strings.ToLower(strings.TrimSpace(event.Title))
	if strings.Contains(title, " - home") {
		return "home"
	}
	if strings.Contains(title, " - away") {
		return "away"
	}
	return ""
}

func scoringTeam(event NotificationEvent) string {
	switch scoringSide(event) {
	case "home":
		return strings.TrimSpace(event.HomeTeam)
	case "away":
		return strings.TrimSpace(event.AwayTeam)
	default:
		return ""
	}
}

func notificationLines(event NotificationEvent) []string {
	candidates := append([]string{}, event.Title, event.BigText, event.Text, event.TickerText)
	candidates = append(candidates, event.Lines...)
	return expandLines(uniqueNonEmpty(candidates))
}

func isMetricLine(value string) bool {
	return metricLinePattern.MatchString(strings.TrimSpace(value))
}

func unusableMatchName(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "home", "away", "total":
		return true
	default:
		return false
	}
}

func uniqueNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func expandLines(values []string) []string {
	expanded := make([]string, 0, len(values))
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				expanded = append(expanded, line)
			}
		}
	}
	return uniqueNonEmpty(expanded)
}

func eventHash(event NotificationEvent) string {
	h := sha256.New()
	for _, value := range []string{event.SourcePackage, event.MatchID, event.Team, event.HomeTeam, event.AwayTeam, event.Title, event.Text, event.BigText, event.TickerText} {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	for _, line := range event.Lines {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{0})
	}
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:12])
}

func sourceAppName(pkg string) string {
	pkg = strings.TrimSpace(pkg)
	if pkg == "" {
		return "Android"
	}
	return pkg
}

func ComposeTweetText(seedValues ...string) string {
	tag := allowedSweepstakesTags[stableIndex(allowedSweepstakesTags, seedValues...)]
	return strings.Join([]string{TweetMention, TweetTag, tag}, " ")
}

func ComposeVariantTweetText(seedValues ...string) string {
	return ComposeCornerVariantTweetText(seedValues...)
}

func ComposeCornerVariantTweetText(seedValues ...string) string {
	return composeCompactTweetText("", "", seedValues...)
}

func ComposeCornerVariantTweetTextForContext(scoringTeam string, seedValues ...string) string {
	return composeCompactTweetText(scoringTeam, "", seedValues...)
}

func ComposeGoalVariantTweetText(seedValues ...string) string {
	return composeCompactTweetText("", "", seedValues...)
}

func ComposeGoalVariantTweetTextForContext(scoringTeam, score string, seedValues ...string) string {
	return composeCompactTweetText(scoringTeam, score, seedValues...)
}

func composeCompactTweetText(scoringTeam, score string, seedValues ...string) string {
	scoringTeam = strings.TrimSpace(scoringTeam)
	score = strings.TrimSpace(score)

	seed := append([]string{scoringTeam, score}, seedValues...)
	parts := []string{ComposeTweetText(seed...)}
	if scoringTeam != "" {
		parts = append(parts, scoringTeam)
	}
	if score != "" {
		parts = append(parts, score)
	}
	parts = append(parts, compactTweetEmojiSuffix(seed...))
	return strings.Join(parts, " ")
}

func compactTweetEmojiSuffix(seedValues ...string) string {
	first := stableChoice(tweetVariantEmojiGroups, "emoji-primary", seedValues...)
	if stableIndex(tweetVariantEmojiGroups, append([]string{"emoji-secondary-toggle"}, seedValues...)...)%2 != 0 {
		return first
	}

	second := stableChoice(tweetVariantEmojiGroups, "emoji-secondary", seedValues...)
	if second == "" || second == first {
		return first
	}
	return first + second
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func ComposeURL(tweetText string) string {
	tweetText = firstNonEmpty(tweetText, TweetText)
	return "https://x.com/intent/tweet?text=" + url.QueryEscape(tweetText)
}

func stableIndex(values []string, seedValues ...string) int {
	if len(values) == 0 {
		return 0
	}
	h := sha256.New()
	seeded := false
	for _, value := range seedValues {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		seeded = true
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	if !seeded {
		return 0
	}
	sum := h.Sum(nil)
	return int(sum[0]) % len(values)
}

func stableChoice(values []string, salt string, seedValues ...string) string {
	if len(values) == 0 {
		return ""
	}
	salted := make([]string, 0, len(seedValues)+1)
	salted = append(salted, salt)
	salted = append(salted, seedValues...)
	return values[stableIndex(values, salted...)]
}
