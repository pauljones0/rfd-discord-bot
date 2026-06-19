package oneverycorner

import (
	"context"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestTotalCornerEventBuildsParseableCornerNotification(t *testing.T) {
	event := TotalCornerEvent{
		Type:        "corner",
		MatchID:     "194699784",
		LeagueID:    "29754",
		LeagueName:  "World Cup 2026",
		HomeTeam:    "Scotland",
		AwayTeam:    "Morocco",
		Team:        "home",
		HomeCorners: 1,
		AwayCorners: 0,
		HomeScore:   0,
		AwayScore:   1,
		Sequence:    1,
		AtUnixMilli: time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC).UnixMilli(),
	}

	notification := event.NotificationEvent(time.Now())
	parsed, ok := ParseNotification(notification)
	if !ok {
		t.Fatal("TotalCorner corner notification did not parse")
	}
	if parsed.Type != eventTypeCorner {
		t.Fatalf("parsed type = %q, want corner", parsed.Type)
	}
	if parsed.MatchName != "Scotland v Morocco" {
		t.Fatalf("match name = %q", parsed.MatchName)
	}
	if notification.SourcePackage != "totalcorner" || notification.MatchID != "194699784" || notification.StableID == "" {
		t.Fatalf("source/match/stable ID = %q/%q/%q", notification.SourcePackage, notification.MatchID, notification.StableID)
	}
}

func TestTotalCornerEventCorrelatesWithScoremerCornerByMatchAlias(t *testing.T) {
	ctx := context.Background()
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	base := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	if err := p.ProcessScoremerEvent(ctx, ScoremerEvent{
		Type:        "corner",
		MatchID:     "scoremer-1",
		LeagueName:  "World Cup",
		HomeTeam:    "Scotland",
		AwayTeam:    "Morocco",
		Team:        "home",
		HomeCorners: 1,
		AwayCorners: 0,
		HomeScore:   0,
		AwayScore:   1,
		Sequence:    1,
		AtUnixMilli: base.UnixMilli(),
	}); err != nil {
		t.Fatalf("scoremer corner error = %v", err)
	}

	if err := p.ProcessTotalCornerEvent(ctx, TotalCornerEvent{
		Type:        "goal",
		MatchID:     "194699784",
		LeagueName:  "World Cup 2026",
		HomeTeam:    "Scotland",
		AwayTeam:    "Morocco",
		Team:        "home",
		HomeCorners: 1,
		AwayCorners: 0,
		HomeScore:   1,
		AwayScore:   1,
		Sequence:    1,
		AtUnixMilli: base.Add(42 * time.Second).UnixMilli(),
	}); err != nil {
		t.Fatalf("totalcorner goal error = %v", err)
	}

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want one possible goal", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertPossibleCornerGoal {
		t.Fatalf("kind = %q, want possible corner goal", alert.Kind)
	}
	if alert.SourcePackage != "totalcorner" || alert.SecondsAfterCorner != 42 {
		t.Fatalf("alert source/seconds = %q/%d, want totalcorner/42", alert.SourcePackage, alert.SecondsAfterCorner)
	}
}

func TestPotentialGoalDedupesAcrossSources(t *testing.T) {
	ctx := context.Background()
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)
	base := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)

	events := []NotificationEvent{
		{
			EventID:       "scoremer-corner",
			StableID:      "scoremer-corner",
			SourcePackage: "scoremer",
			MatchID:       "s1",
			Title:         "Corner - home",
			Text:          "Scotland v Morocco\nCorners 1-0\nScore 0-1",
			Lines:         []string{"Scotland v Morocco", "Corners 1-0", "Score 0-1"},
			Team:          "home",
			HomeTeam:      "Scotland",
			AwayTeam:      "Morocco",
			ReceivedAt:    base,
		},
		{
			EventID:       "totalcorner-corner",
			StableID:      "totalcorner-corner",
			SourcePackage: "totalcorner",
			MatchID:       "tc1",
			Title:         "Corner - home",
			Text:          "Scotland v Morocco\nCorners 1-0\nScore 0-1",
			Lines:         []string{"Scotland v Morocco", "Corners 1-0", "Score 0-1"},
			Team:          "home",
			HomeTeam:      "Scotland",
			AwayTeam:      "Morocco",
			ReceivedAt:    base.Add(time.Second),
		},
		{
			EventID:       "totalcorner-goal",
			StableID:      "totalcorner-goal",
			SourcePackage: "totalcorner",
			MatchID:       "tc1",
			Title:         "Goal - home",
			Text:          "Scotland v Morocco\nCorners 1-0\nScore 1-1",
			Lines:         []string{"Scotland v Morocco", "Corners 1-0", "Score 1-1"},
			Team:          "home",
			HomeTeam:      "Scotland",
			AwayTeam:      "Morocco",
			ReceivedAt:    base.Add(40 * time.Second),
		},
		{
			EventID:       "scoremer-goal",
			StableID:      "scoremer-goal",
			SourcePackage: "scoremer",
			MatchID:       "s1",
			Title:         "Goal - home",
			Text:          "Scotland v Morocco\nCorners 1-0\nScore 1-1",
			Lines:         []string{"Scotland v Morocco", "Corners 1-0", "Score 1-1"},
			Team:          "home",
			HomeTeam:      "Scotland",
			AwayTeam:      "Morocco",
			ReceivedAt:    base.Add(43 * time.Second),
		},
	}
	for _, event := range events {
		if err := p.ProcessNotification(ctx, event); err != nil {
			t.Fatalf("ProcessNotification(%s) error = %v", event.EventID, err)
		}
	}

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want one deduped possible goal", len(notifier.alerts))
	}
	if notifier.alerts[0].SourcePackage != "totalcorner" {
		t.Fatalf("alert source = %q, want first goal source totalcorner", notifier.alerts[0].SourcePackage)
	}
}

func TestProcessTotalCornerSystemEventBuildsSystemAlert(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerPotentialGoals},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)
	now := time.Date(2026, 6, 19, 23, 0, 0, 0, time.UTC)
	p.timeSource = func() time.Time { return now }

	if err := p.ProcessTotalCornerSystemEvent(context.Background(), TotalCornerEvent{
		Type:             totalCornerSystemFixAttempt,
		Severity:         "warning",
		Stage:            "active",
		Status:           "403",
		Attempt:          "page.reload",
		Message:          "TotalCorner polling is unhealthy.",
		StaleSeconds:     31,
		SuppressedEvents: 2,
	}); err != nil {
		t.Fatalf("ProcessTotalCornerSystemEvent error = %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertSystem {
		t.Fatalf("kind = %q, want system", alert.Kind)
	}
	if alert.RawTitle != "OnEveryCorner TotalCorner recovery attempted" {
		t.Fatalf("title = %q", alert.RawTitle)
	}
	if alert.SourcePackage != "totalcorner" || alert.SystemSeverity != "warning" {
		t.Fatalf("source/severity = %q/%q", alert.SourcePackage, alert.SystemSeverity)
	}
}
