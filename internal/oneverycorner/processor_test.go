package oneverycorner

import (
	"context"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type testStore struct {
	subs []models.Subscription
}

func (s *testStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

type testNotifier struct {
	alerts []models.OnEveryCornerAlert
	subs   [][]models.Subscription
}

func (n *testNotifier) SendOnEveryCornerAlert(_ context.Context, alert models.OnEveryCornerAlert, subs []models.Subscription) error {
	n.alerts = append(n.alerts, alert)
	n.subs = append(n.subs, subs)
	return nil
}

func TestProcessNotificationSendsCornerAlert(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	now := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "e1",
		StableID:      "stable-corner",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    now,
	})
	if err != nil {
		t.Fatalf("ProcessNotification error = %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertCorner {
		t.Fatalf("kind = %q, want corner", alert.Kind)
	}
	if alert.MatchName != "Canada v Germany" {
		t.Fatalf("match = %q, want Canada v Germany", alert.MatchName)
	}
	if alert.TweetText != TweetText || alert.TweetURL != TweetURL {
		t.Fatalf("tweet fields not populated: %#v", alert)
	}
}

func TestProcessNotificationCorrelatesGoalAfterCorner(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	start := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	if err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "corner",
		StableID:      "stable-corner",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    start,
	}); err != nil {
		t.Fatalf("corner ProcessNotification error = %v", err)
	}
	if err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "goal",
		StableID:      "stable-goal",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Goal - Canada",
		ReceivedAt:    start.Add(42 * time.Second),
	}); err != nil {
		t.Fatalf("goal ProcessNotification error = %v", err)
	}

	if len(notifier.alerts) != 2 {
		t.Fatalf("alerts = %d, want 2", len(notifier.alerts))
	}
	alert := notifier.alerts[1]
	if alert.Kind != models.OnEveryCornerAlertPossibleCornerGoal {
		t.Fatalf("kind = %q, want possible_corner_goal", alert.Kind)
	}
	if alert.SecondsAfterCorner != 42 {
		t.Fatalf("seconds after corner = %d, want 42", alert.SecondsAfterCorner)
	}
}

func TestParseNotificationExtractsBet365CornerMatch(t *testing.T) {
	parsed, ok := ParseNotification(NotificationEvent{
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Corner",
		Text:          "Iran v New Zealand 90+4 min\n4th Iran - 5th Total",
	})
	if !ok {
		t.Fatal("expected notification to parse")
	}
	if parsed.Type != eventTypeCorner {
		t.Fatalf("type = %q, want corner", parsed.Type)
	}
	if parsed.MatchName != "Iran v New Zealand" {
		t.Fatalf("match = %q, want Iran v New Zealand", parsed.MatchName)
	}
}

func TestParseNotificationExtractsBet365GoalMatchFromScoreline(t *testing.T) {
	parsed, ok := ParseNotification(NotificationEvent{
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "GOAL!",
		Text:          "Iran 64 min\nIran [2] - 2 New Zealand",
	})
	if !ok {
		t.Fatal("expected notification to parse")
	}
	if parsed.Type != eventTypeGoal {
		t.Fatalf("type = %q, want goal", parsed.Type)
	}
	if parsed.MatchName != "Iran v New Zealand" {
		t.Fatalf("match = %q, want Iran v New Zealand", parsed.MatchName)
	}
}

func TestProcessNotificationIgnoresOldGoalAfterCorner(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	start := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	_ = p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "corner",
		StableID:      "stable-corner",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    start,
	})
	_ = p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "goal",
		StableID:      "stable-goal",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Goal - Canada",
		ReceivedAt:    start.Add(3 * time.Minute),
	})

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want only corner alert", len(notifier.alerts))
	}
}

func TestProcessNotificationDedupesStableID(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	event := NotificationEvent{
		EventID:       "e1",
		StableID:      "same-active-notification",
		SourcePackage: "com.bet365SportsCA.Bet365_Application",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    time.Now(),
	}
	_ = p.ProcessNotification(context.Background(), event)
	event.EventID = "e2"
	event.ReceivedAt = event.ReceivedAt.Add(time.Second)
	_ = p.ProcessNotification(context.Background(), event)

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1 after duplicate", len(notifier.alerts))
	}
}
