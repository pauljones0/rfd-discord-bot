package oneverycorner

import (
	"context"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestScoremerEventBuildsParseableCornerNotification(t *testing.T) {
	event := ScoremerEvent{
		Type:        "corner",
		MatchID:     "1533093",
		LeagueID:    "3559",
		LeagueName:  "World Cup",
		HomeTeam:    "Uzbekistan",
		AwayTeam:    "Colombia",
		Team:        "home",
		HomeCorners: 3,
		AwayCorners: 2,
		HomeScore:   0,
		AwayScore:   0,
		Sequence:    3,
		AtUnixMilli: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}

	notification := event.NotificationEvent(time.Now())
	parsed, ok := ParseNotification(notification)
	if !ok {
		t.Fatal("Scoremer corner notification did not parse")
	}
	if parsed.Type != eventTypeCorner {
		t.Fatalf("parsed type = %q, want corner", parsed.Type)
	}
	if parsed.MatchName != "Uzbekistan v Colombia" {
		t.Fatalf("match name = %q", parsed.MatchName)
	}
	if notification.SourcePackage != "scoremer" || notification.MatchID != "1533093" || notification.StableID == "" {
		t.Fatalf("source/match/stable ID = %q/%q/%q", notification.SourcePackage, notification.MatchID, notification.StableID)
	}
}

func TestProcessScoremerEventCorrelatesGoalAfterCorner(t *testing.T) {
	ctx := context.Background()
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	corner := ScoremerEvent{
		Type:        "corner",
		MatchID:     "1533093",
		LeagueName:  "World Cup",
		HomeTeam:    "Uzbekistan",
		AwayTeam:    "Colombia",
		Team:        "home",
		HomeCorners: 1,
		AwayCorners: 0,
		Sequence:    1,
		AtUnixMilli: base.UnixMilli(),
	}
	if err := p.ProcessScoremerEvent(ctx, corner); err != nil {
		t.Fatalf("corner error = %v", err)
	}

	goal := corner
	goal.Type = "goal"
	goal.HomeScore = 1
	goal.Sequence = 1
	goal.AtUnixMilli = base.Add(42 * time.Second).UnixMilli()
	if err := p.ProcessScoremerEvent(ctx, goal); err != nil {
		t.Fatalf("goal error = %v", err)
	}

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want possible goal only", len(notifier.alerts))
	}
	if notifier.alerts[0].Kind != models.OnEveryCornerAlertPossibleCornerGoal {
		t.Fatalf("alert kind = %q", notifier.alerts[0].Kind)
	}
	if notifier.alerts[0].SecondsAfterCorner != 42 {
		t.Fatalf("seconds after corner = %d, want 42", notifier.alerts[0].SecondsAfterCorner)
	}
	if notifier.alerts[0].ScoringSide != "home" || notifier.alerts[0].ScoringTeam != "Uzbekistan" {
		t.Fatalf("scoring context = %q/%q, want home/Uzbekistan", notifier.alerts[0].ScoringSide, notifier.alerts[0].ScoringTeam)
	}
	if notifier.alerts[0].Score != "1-0" {
		t.Fatalf("score = %q, want 1-0", notifier.alerts[0].Score)
	}
}

func TestProcessScoremerEventIgnoresUnsupportedType(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	if err := p.ProcessScoremerEvent(context.Background(), ScoremerEvent{
		Type:        "period",
		MatchID:     "1533093",
		HomeTeam:    "Uzbekistan",
		AwayTeam:    "Colombia",
		HomeCorners: 1,
	}); err != nil {
		t.Fatalf("ProcessScoremerEvent error = %v", err)
	}
	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts = %d, want 0", len(notifier.alerts))
	}
}

func TestProcessScoremerEventUsesProcessorClockWhenTimestampMissing(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	p.timeSource = func() time.Time { return now }

	if err := p.ProcessScoremerEvent(context.Background(), ScoremerEvent{
		Type:        "corner",
		MatchID:     "1533093",
		HomeTeam:    "Uzbekistan",
		AwayTeam:    "Colombia",
		Team:        "home",
		HomeCorners: 1,
		Sequence:    1,
	}); err != nil {
		t.Fatalf("ProcessScoremerEvent error = %v", err)
	}
	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts = %d, want no routine corner alerts", len(notifier.alerts))
	}
	p.mu.Lock()
	state := p.corners["scoremer:1533093"]
	p.mu.Unlock()
	if !state.ReceivedAt.Equal(now) {
		t.Fatalf("recorded corner receivedAt = %s, want %s", state.ReceivedAt, now)
	}
}

func TestProcessScoremerSystemEventBuildsSystemAlert(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerPotentialGoals},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	p.timeSource = func() time.Time { return now }

	if err := p.ProcessScoremerSystemEvent(context.Background(), ScoremerEvent{
		Type:             scoremerSystemFixAttempt,
		Severity:         "warning",
		Stage:            "active",
		Status:           "403",
		Attempt:          "page.reload",
		Message:          "Scoremer polling is unhealthy.",
		StaleSeconds:     31,
		SuppressedEvents: 5,
	}); err != nil {
		t.Fatalf("ProcessScoremerSystemEvent error = %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertSystem {
		t.Fatalf("kind = %q, want system", alert.Kind)
	}
	if alert.RawTitle != "OnEveryCorner Scoremer recovery attempted" {
		t.Fatalf("title = %q", alert.RawTitle)
	}
	if alert.SystemSeverity != "warning" || alert.SystemDetails == "" {
		t.Fatalf("severity/details = %q/%q", alert.SystemSeverity, alert.SystemDetails)
	}
	if len(alert.SystemFields) < 4 {
		t.Fatalf("fields = %#v, want status fields", alert.SystemFields)
	}
}
