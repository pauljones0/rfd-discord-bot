package oneverycorner

import (
	"context"
	"strings"
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

func TestProcessNotificationRecordsCornerWithoutRoutineDiscordAlert(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	now := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "e1",
		StableID:      "stable-corner",
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    now,
	})
	if err != nil {
		t.Fatalf("ProcessNotification error = %v", err)
	}
	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts = %d, want 0 for routine corner awareness subscription", len(notifier.alerts))
	}

	p.mu.Lock()
	state, ok := p.corners["canada v germany"]
	p.mu.Unlock()
	if !ok {
		t.Fatal("corner was not recorded for later goal correlation")
	}
	if state.MatchName != "Canada v Germany" || !state.ReceivedAt.Equal(now) {
		t.Fatalf("recorded corner = %#v, want Canada v Germany at %s", state, now)
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
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    start,
	}); err != nil {
		t.Fatalf("corner ProcessNotification error = %v", err)
	}
	if err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "goal",
		StableID:      "stable-goal",
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Goal - Canada",
		ReceivedAt:    start.Add(42 * time.Second),
	}); err != nil {
		t.Fatalf("goal ProcessNotification error = %v", err)
	}

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1 awareness alert", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertPossibleCornerGoal {
		t.Fatalf("kind = %q, want possible_corner_goal", alert.Kind)
	}
	if alert.SecondsAfterCorner != 42 {
		t.Fatalf("seconds after corner = %d, want 42", alert.SecondsAfterCorner)
	}
}

func TestProcessNotificationUsesScoremerMatchIDForCorrelation(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}
	start := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("correlates despite changed display name", func(t *testing.T) {
		notifier := &testNotifier{}
		p := NewProcessor(store, notifier)
		if err := p.ProcessNotification(context.Background(), NotificationEvent{
			EventID:       "corner",
			StableID:      "stable-corner",
			SourcePackage: "scoremer",
			MatchID:       "1533093",
			Title:         "Corner - home",
			Text:          "Unknown match\nCorners 1-0\nScore 0-0",
			Lines:         []string{"Unknown match", "Corners 1-0", "Score 0-0"},
			ReceivedAt:    start,
		}); err != nil {
			t.Fatalf("corner ProcessNotification error = %v", err)
		}
		if err := p.ProcessNotification(context.Background(), NotificationEvent{
			EventID:       "goal",
			StableID:      "stable-goal",
			SourcePackage: "scoremer",
			MatchID:       "1533093",
			Title:         "Goal - home",
			Text:          "Uzbekistan v Colombia\nCorners 1-0\nScore 1-0",
			Lines:         []string{"Uzbekistan v Colombia", "Corners 1-0", "Score 1-0"},
			ReceivedAt:    start.Add(42 * time.Second),
		}); err != nil {
			t.Fatalf("goal ProcessNotification error = %v", err)
		}
		if len(notifier.alerts) != 1 {
			t.Fatalf("alerts = %d, want possible goal only", len(notifier.alerts))
		}
		if notifier.alerts[0].Kind != models.OnEveryCornerAlertPossibleCornerGoal {
			t.Fatalf("alert kind = %q", notifier.alerts[0].Kind)
		}
	})

	t.Run("does not correlate different unknown matches", func(t *testing.T) {
		notifier := &testNotifier{}
		p := NewProcessor(store, notifier)
		_ = p.ProcessNotification(context.Background(), NotificationEvent{
			EventID:       "corner",
			StableID:      "stable-corner",
			SourcePackage: "scoremer",
			MatchID:       "1533093",
			Title:         "Corner - home",
			Text:          "Unknown match\nCorners 1-0\nScore 0-0",
			Lines:         []string{"Unknown match", "Corners 1-0", "Score 0-0"},
			ReceivedAt:    start,
		})
		_ = p.ProcessNotification(context.Background(), NotificationEvent{
			EventID:       "goal",
			StableID:      "stable-goal",
			SourcePackage: "scoremer",
			MatchID:       "9999999",
			Title:         "Goal - home",
			Text:          "Unknown match\nCorners 1-0\nScore 1-0",
			Lines:         []string{"Unknown match", "Corners 1-0", "Score 1-0"},
			ReceivedAt:    start.Add(42 * time.Second),
		})
		if len(notifier.alerts) != 0 {
			t.Fatalf("alerts = %d, want no awareness alerts", len(notifier.alerts))
		}
	})
}

func TestPotentialGoalSubscriptionSkipsCornerAlertButCorrelatesGoal(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerPotentialGoals},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	start := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	if err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "corner",
		StableID:      "stable-corner",
		SourcePackage: "scoremer",
		Title:         "Corner - home",
		Text:          "Canada v Germany\nCorners 1-0\nScore 0-0",
		Lines:         []string{"Canada v Germany", "Corners 1-0", "Score 0-0"},
		ReceivedAt:    start,
	}); err != nil {
		t.Fatalf("corner ProcessNotification error = %v", err)
	}
	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts after corner = %d, want 0 for potential-goal subscription", len(notifier.alerts))
	}

	if err := p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "goal",
		StableID:      "stable-goal",
		SourcePackage: "scoremer",
		Title:         "Goal - home",
		Text:          "Canada v Germany\nCorners 1-0\nScore 1-0",
		Lines:         []string{"Canada v Germany", "Corners 1-0", "Score 1-0"},
		ReceivedAt:    start.Add(42 * time.Second),
	}); err != nil {
		t.Fatalf("goal ProcessNotification error = %v", err)
	}

	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts after goal = %d, want 1", len(notifier.alerts))
	}
	alert := notifier.alerts[0]
	if alert.Kind != models.OnEveryCornerAlertPossibleCornerGoal {
		t.Fatalf("kind = %q, want possible_corner_goal", alert.Kind)
	}
	if alert.Score != "1-0" || alert.CornerScore != "1-0" {
		t.Fatalf("score context = score %q corners %q, want 1-0/1-0", alert.Score, alert.CornerScore)
	}
}

func TestSystemAlertGoesToAllOnEveryCornerSubscriptionTypes(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "corner", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
		{GuildID: "g1", ChannelID: "goal", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerPotentialGoals},
		{GuildID: "g1", ChannelID: "other", SubscriptionType: dealtypes.SubscriptionRFD, DealType: dealtypes.RFDAll},
	}}
	notifier := &testNotifier{}
	p := NewProcessor(store, notifier)

	err := p.sendAlert(context.Background(), models.OnEveryCornerAlert{
		Kind:           models.OnEveryCornerAlertSystem,
		RawTitle:       "OnEveryCorner Scoremer issue",
		SystemSeverity: "warning",
		SystemDetails:  "Scoremer polling is unhealthy.",
	})
	if err != nil {
		t.Fatalf("sendAlert error = %v", err)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want 1", len(notifier.alerts))
	}
	got := notifier.subs[0]
	if len(got) != 2 {
		t.Fatalf("subscriptions = %d, want both oneverycorner subscription types", len(got))
	}
	if got[0].ChannelID != "corner" || got[1].ChannelID != "goal" {
		t.Fatalf("channels = %q/%q, want corner/goal", got[0].ChannelID, got[1].ChannelID)
	}
}

func TestParseNotificationIgnoresMetricOnlyCornerSummary(t *testing.T) {
	_, ok := ParseNotification(NotificationEvent{
		SourcePackage: "scoremer",
		Title:         "World Cup",
		Text:          "Canada v Germany\nCorners 1-0\nScore 0-0",
		Lines:         []string{"Canada v Germany", "Corners 1-0", "Score 0-0"},
	})
	if ok {
		t.Fatal("metric-only score summary parsed as an event")
	}
}

func TestParseNotificationExtractsScoremerContext(t *testing.T) {
	parsed, ok := ParseNotification(NotificationEvent{
		SourcePackage: "scoremer",
		Title:         "Goal - home",
		Text:          "Canada v Germany\nCorners 3-2\nScore 1-0",
		Lines:         []string{"Canada v Germany", "Corners 3-2", "Score 1-0"},
	})
	if !ok {
		t.Fatal("expected notification to parse")
	}
	if parsed.Type != eventTypeGoal {
		t.Fatalf("type = %q, want goal", parsed.Type)
	}
	if parsed.MatchName != "Canada v Germany" {
		t.Fatalf("match = %q, want Canada v Germany", parsed.MatchName)
	}
	if parsed.Score != "1-0" || parsed.CornerScore != "3-2" {
		t.Fatalf("score context = score %q corners %q, want 1-0/3-2", parsed.Score, parsed.CornerScore)
	}
}

func TestParseNotificationExtractsGoalMatchFromScoreline(t *testing.T) {
	parsed, ok := ParseNotification(NotificationEvent{
		SourcePackage: "scoremer",
		Title:         "Goal - home",
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
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    start,
	})
	_ = p.ProcessNotification(context.Background(), NotificationEvent{
		EventID:       "goal",
		StableID:      "stable-goal",
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Goal - Canada",
		ReceivedAt:    start.Add(3 * time.Minute),
	})

	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts = %d, want no awareness alerts", len(notifier.alerts))
	}
}

func TestProcessNotificationGoalCorrelationWindowIsSeventyFiveSeconds(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerAlerts},
	}}

	start := time.Date(2026, 6, 16, 20, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name       string
		goalAfter  time.Duration
		wantAlerts int
	}{
		{name: "at window", goalAfter: 75 * time.Second, wantAlerts: 1},
		{name: "past window", goalAfter: 76 * time.Second, wantAlerts: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			notifier := &testNotifier{}
			p := NewProcessor(store, notifier)
			if err := p.ProcessNotification(context.Background(), NotificationEvent{
				EventID:       tt.name + "-corner",
				StableID:      tt.name + "-stable-corner",
				SourcePackage: "scoremer",
				Title:         "Corner - home",
				Text:          "Canada v Germany\nCorners 1-0\nScore 0-0",
				Lines:         []string{"Canada v Germany", "Corners 1-0", "Score 0-0"},
				ReceivedAt:    start,
			}); err != nil {
				t.Fatalf("corner ProcessNotification error = %v", err)
			}
			if err := p.ProcessNotification(context.Background(), NotificationEvent{
				EventID:       tt.name + "-goal",
				StableID:      tt.name + "-stable-goal",
				SourcePackage: "scoremer",
				Title:         "Goal - home",
				Text:          "Canada v Germany\nCorners 1-0\nScore 1-0",
				Lines:         []string{"Canada v Germany", "Corners 1-0", "Score 1-0"},
				ReceivedAt:    start.Add(tt.goalAfter),
			}); err != nil {
				t.Fatalf("goal ProcessNotification error = %v", err)
			}
			if len(notifier.alerts) != tt.wantAlerts {
				t.Fatalf("alerts = %d, want %d", len(notifier.alerts), tt.wantAlerts)
			}
		})
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
		SourcePackage: "scoremer",
		Title:         "Canada v Germany",
		Text:          "Corner - Canada",
		ReceivedAt:    time.Now(),
	}
	_ = p.ProcessNotification(context.Background(), event)
	event.EventID = "e2"
	event.ReceivedAt = event.ReceivedAt.Add(time.Second)
	_ = p.ProcessNotification(context.Background(), event)

	if len(notifier.alerts) != 0 {
		t.Fatalf("alerts = %d, want no routine corner alerts", len(notifier.alerts))
	}
	p.mu.Lock()
	seenCount := len(p.seen)
	state := p.corners["canada v germany"]
	p.mu.Unlock()
	if seenCount != 1 {
		t.Fatalf("seen entries = %d, want one stable ID after duplicate", seenCount)
	}
	if state.EventID != "e1" {
		t.Fatalf("corner state event ID = %q, want first event e1", state.EventID)
	}
}

func TestComposeURLUsesXIntent(t *testing.T) {
	got := ComposeURL("@Enterprise #OnEveryCorner #Sweepstakes")
	want := "https://x.com/intent/tweet?text=%40Enterprise+%23OnEveryCorner+%23Sweepstakes"
	if got != want {
		t.Fatalf("ComposeURL() = %q, want %q", got, want)
	}
}

func TestComposeTweetTextUsesOnlyAllowedElements(t *testing.T) {
	for _, seed := range []string{"a", "b", "c", "d", "e", "f"} {
		text := ComposeTweetText(seed)
		if !strings.HasPrefix(text, "@Enterprise #OnEveryCorner #") {
			t.Fatalf("ComposeTweetText(%q) = %q, want Enterprise/OnEveryCorner prefix", seed, text)
		}
		tag := strings.TrimPrefix(text, "@Enterprise #OnEveryCorner ")
		found := false
		for _, allowed := range allowedSweepstakesTags {
			if tag == allowed {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("ComposeTweetText(%q) used disallowed tag %q", seed, tag)
		}
	}
}

func TestComposeVariantTweetTextUsesCompactContext(t *testing.T) {
	cornerSeeds := []string{"stable-corner", "corner", "canada germany"}
	goalSeeds := []string{"stable-goal", "goal", "canada germany"}
	cornerSafe := ComposeTweetText(cornerSeeds...)
	goalSafe := ComposeTweetText(goalSeeds...)
	cornerVariant := ComposeCornerVariantTweetText(cornerSeeds...)
	goalVariant := ComposeGoalVariantTweetText(goalSeeds...)
	contextCornerVariant := ComposeCornerVariantTweetTextForContext("Canada", cornerSeeds...)
	contextGoalVariant := ComposeGoalVariantTweetTextForContext("Uzbekistan", "1-0", goalSeeds...)

	if !strings.HasPrefix(cornerVariant, cornerSafe+" ") {
		t.Fatalf("corner variant = %q, want safe text prefix %q", cornerVariant, cornerSafe)
	}
	if !strings.HasPrefix(goalVariant, goalSafe+" ") {
		t.Fatalf("goal variant = %q, want safe text prefix %q", goalVariant, goalSafe)
	}
	if !strings.HasPrefix(contextGoalVariant, ComposeTweetText("Uzbekistan", "1-0", goalSeeds[0], goalSeeds[1], goalSeeds[2])+" ") {
		t.Fatalf("context goal variant = %q, want safe text prefix", contextGoalVariant)
	}
	if !strings.Contains(contextCornerVariant, "Canada") {
		t.Fatalf("context corner variant = %q, want team", contextCornerVariant)
	}
	if !strings.Contains(contextGoalVariant, "Uzbekistan") || !strings.Contains(contextGoalVariant, "1-0") {
		t.Fatalf("context goal variant = %q, want team and score", contextGoalVariant)
	}
	if !containsAnyEmoji(cornerVariant) || !containsAnyEmoji(goalVariant) || !containsAnyEmoji(contextGoalVariant) {
		t.Fatalf("variants should include emoji suffixes: corner=%q goal=%q context=%q", cornerVariant, goalVariant, contextGoalVariant)
	}
	if len([]rune(cornerVariant)) > 280 || len([]rune(goalVariant)) > 280 {
		t.Fatalf("variant length over 280: corner=%d goal=%d", len([]rune(cornerVariant)), len([]rune(goalVariant)))
	}
	if cornerVariant != ComposeCornerVariantTweetText(cornerSeeds...) || goalVariant != ComposeGoalVariantTweetText(goalSeeds...) {
		t.Fatal("variants should be stable for same seeds")
	}

	weakPhrases := []string{
		"Set piece sweat",
		"Goalmouth noise",
		"Mark up",
		"Delivery on point",
		"All eyes",
		"corner chaos",
		"panic",
		"delivery",
		"keeper",
		"box",
	}
	if containsAnyPhrase(cornerVariant, weakPhrases) || containsAnyPhrase(goalVariant, weakPhrases) {
		t.Fatalf("variants include legacy phrasing: corner=%q goal=%q", cornerVariant, goalVariant)
	}

	seen := map[string]struct{}{}
	for i := 0; i < 50; i++ {
		candidate := ComposeGoalVariantTweetText("seed", time.Duration(i).String())
		seen[candidate] = struct{}{}
		if !containsAnyEmoji(candidate) {
			t.Fatalf("variant sample = %q, want emoji", candidate)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("variant diversity = %d unique outputs from 50 seeds, want at least 10", len(seen))
	}
}

func containsAnyPhrase(value string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(value, phrase) {
			return true
		}
	}
	return false
}

func containsAnyEmoji(value string) bool {
	for _, emoji := range tweetVariantEmojiGroups {
		if strings.Contains(value, emoji) {
			return true
		}
	}
	return false
}
