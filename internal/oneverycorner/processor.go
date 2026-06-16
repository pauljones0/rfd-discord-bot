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
	TweetText = "@Enterprise #OnEveryCorner #Sweepstakes"
	TweetURL  = "https://twitter.com/intent/tweet?text=%40Enterprise%20%23OnEveryCorner%20%23Sweepstakes"

	DefaultGoalCorrelationWindow = 120 * time.Second
)

type NotificationEvent struct {
	EventID       string
	StableID      string
	SourcePackage string
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

	mu         sync.Mutex
	corners    map[string]cornerState
	seen       map[string]time.Time
	seenOrder  []string
	maxSeen    int
	window     time.Duration
	timeSource func() time.Time
}

type cornerState struct {
	MatchName  string
	ReceivedAt time.Time
	EventID    string
	StableID   string
	RawText    string
}

type parsedEvent struct {
	Type      string
	MatchName string
	MatchKey  string
	RawText   string
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
	minutePattern      = regexp.MustCompile(`(?i)\s+\d{1,3}(?:\+\d+)?\s*min\b.*$`)
	eventPrefixPattern = regexp.MustCompile(`(?i)\b(corner(?:\s+kick)?|go+al|goal\s+alert|score\s+update|football|soccer|fifa|world\s+cup|bet365|notification|alert)\b`)
	spacePattern       = regexp.MustCompile(`\s+`)
	nonKeyPattern      = regexp.MustCompile(`[^a-z0-9]+`)
)

func NewProcessor(store Store, notifier Notifier) *Processor {
	return &Processor{
		store:      store,
		notifier:   notifier,
		corners:    make(map[string]cornerState),
		seen:       make(map[string]time.Time),
		maxSeen:    512,
		window:     DefaultGoalCorrelationWindow,
		timeSource: time.Now,
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
		alert, ok := p.correlateGoal(event, parsed)
		if !ok {
			slog.Info("OnEveryCorner goal notification had no recent corner", "match", parsed.MatchName, "event_id", event.EventID)
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
	eventType := ""
	if goalPattern.MatchString(raw) {
		eventType = eventTypeGoal
	} else if cornerPattern.MatchString(raw) {
		eventType = eventTypeCorner
	} else {
		return parsedEvent{}, false
	}

	matchName := extractMatchName(event)
	matchKey := normalizeMatchKey(matchName)
	if matchKey == "" {
		matchKey = normalizeMatchKey(firstNonEmpty(event.Title, event.BigText, event.Text, event.TickerText, raw))
	}
	if matchKey == "" {
		matchName = "Unknown match"
		matchKey = "unknown"
	}

	return parsedEvent{
		Type:      eventType,
		MatchName: matchName,
		MatchKey:  matchKey,
		RawText:   raw,
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
	p.mu.Unlock()

	return models.OnEveryCornerAlert{
		Kind:          models.OnEveryCornerAlertCorner,
		MatchName:     parsed.MatchName,
		EventID:       event.EventID,
		StableID:      event.StableID,
		SourcePackage: event.SourcePackage,
		SourceApp:     sourceAppName(event.SourcePackage),
		RawTitle:      event.Title,
		RawText:       parsed.RawText,
		Lines:         event.Lines,
		ReceivedAt:    event.ReceivedAt,
		CornerAt:      event.ReceivedAt,
		TweetText:     TweetText,
		TweetURL:      TweetURL,
	}
}

func (p *Processor) correlateGoal(event NotificationEvent, parsed parsedEvent) (models.OnEveryCornerAlert, bool) {
	p.mu.Lock()
	state, ok := p.corners[parsed.MatchKey]
	p.mu.Unlock()
	if !ok {
		return models.OnEveryCornerAlert{}, false
	}
	elapsed := event.ReceivedAt.Sub(state.ReceivedAt)
	if elapsed < 0 || elapsed > p.window {
		return models.OnEveryCornerAlert{}, false
	}

	return models.OnEveryCornerAlert{
		Kind:               models.OnEveryCornerAlertPossibleCornerGoal,
		MatchName:          firstNonEmpty(parsed.MatchName, state.MatchName),
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
		TweetText:          TweetText,
		TweetURL:           TweetURL,
	}, true
}

func (p *Processor) sendAlert(ctx context.Context, alert models.OnEveryCornerAlert) error {
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("load oneverycorner subscriptions: %w", err)
	}
	filtered := filterSubscriptions(subs)
	if len(filtered) == 0 {
		slog.Info("No OnEveryCorner subscriptions configured", "kind", alert.Kind, "match", alert.MatchName)
		return nil
	}
	return p.notifier.SendOnEveryCornerAlert(ctx, alert, filtered)
}

func filterSubscriptions(subs []models.Subscription) []models.Subscription {
	filtered := make([]models.Subscription, 0, len(subs))
	seenChannels := make(map[string]struct{}, len(subs))
	for _, sub := range subs {
		if !sub.IsOnEveryCorner() {
			continue
		}
		if sub.DealType != "" && sub.DealType != dealtypes.OnEveryCornerAlerts {
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

func eventRawText(event NotificationEvent) string {
	lines := append([]string{}, event.BigText, event.Text, event.TickerText, event.Title)
	lines = append(lines, event.Lines...)
	return strings.Join(uniqueNonEmpty(lines), "\n")
}

func extractMatchName(event NotificationEvent) string {
	candidates := append([]string{}, event.Title, event.BigText, event.Text, event.TickerText)
	candidates = append(candidates, event.Lines...)
	candidates = expandLines(uniqueNonEmpty(candidates))

	for _, candidate := range candidates {
		if match := versusMatchPattern.FindStringSubmatch(candidate); len(match) == 3 {
			home := cleanTeamName(match[1])
			away := cleanTeamName(match[2])
			if home != "" && away != "" {
				return home + " v " + away
			}
		}
	}
	for _, candidate := range candidates {
		if match := scorelinePattern.FindStringSubmatch(candidate); len(match) == 3 {
			home := cleanTeamName(match[1])
			away := cleanTeamName(match[2])
			if home != "" && away != "" {
				return home + " v " + away
			}
		}
	}
	for _, candidate := range candidates {
		cleaned := cleanMatchName(candidate)
		if cleaned == "" || strings.EqualFold(cleaned, "bet365") {
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
	for _, value := range []string{event.SourcePackage, event.Title, event.Text, event.BigText, event.TickerText} {
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
	if strings.Contains(strings.ToLower(pkg), "bet365") {
		return "bet365"
	}
	if pkg == "" {
		return "Android"
	}
	return pkg
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
	return "https://twitter.com/intent/tweet?text=" + url.QueryEscape(tweetText)
}
