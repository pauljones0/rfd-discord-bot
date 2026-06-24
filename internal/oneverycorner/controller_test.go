package oneverycorner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestSnapshotTrackerBaselinesThenEmitsOnlyNewDeltas(t *testing.T) {
	tracker := newSnapshotTracker()
	now := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	snapshot := MatchSnapshot{
		MatchWindow: MatchWindow{
			ID:       "m1",
			HomeTeam: "Canada",
			AwayTeam: "Brazil",
		},
		HomeCorners: 1,
		AwayCorners: 0,
		HomeScore:   0,
		AwayScore:   0,
	}

	if events := tracker.Apply(snapshot, now, false); len(events) != 0 {
		t.Fatalf("initial apply events = %d, want baseline only", len(events))
	}

	snapshot.HomeCorners = 2
	snapshot.HomeScore = 1
	events := tracker.Apply(snapshot, now.Add(time.Second), false)
	if len(events) != 2 {
		t.Fatalf("events = %d, want corner and goal", len(events))
	}
	if events[0].Type != eventTypeCorner || events[0].Team != "home" || events[0].Sequence != 2 {
		t.Fatalf("corner event = %+v", events[0])
	}
	if events[1].Type != eventTypeGoal || events[1].Team != "home" || events[1].Sequence != 1 {
		t.Fatalf("goal event = %+v", events[1])
	}

	tracker.Reset()
	snapshot.HomeCorners = 5
	snapshot.HomeScore = 2
	if events := tracker.Apply(snapshot, now.Add(2*time.Second), false); len(events) != 0 {
		t.Fatalf("post-reset apply events = %d, want recovery baseline only", len(events))
	}
}

func TestControllerScheduleSleepJumpsToUpcomingKickoff(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	controller := NewController(nil, ControllerConfig{
		ScheduleRefreshInterval: 15 * time.Minute,
		TotalCornerSource:       &fakeTotalCornerSource{},
	})

	soon := []MatchWindow{{ID: "m1", Start: now.Add(5 * time.Minute), HomeTeam: "Canada", AwayTeam: "Brazil"}}
	if got := controller.scheduleSleepDuration(now, soon); got != 5*time.Minute {
		t.Fatalf("sleep = %s, want 5m", got)
	}

	later := []MatchWindow{{ID: "m2", Start: now.Add(30 * time.Minute), HomeTeam: "A", AwayTeam: "B"}}
	if got := controller.scheduleSleepDuration(now, later); got != 15*time.Minute {
		t.Fatalf("sleep = %s, want 15m heartbeat", got)
	}
}

func TestControllerPendingKickoffWindowStartsAtKickoffOnly(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	controller := NewController(nil, ControllerConfig{
		PendingKickoffTimeout: time.Hour,
		TotalCornerSource:     &fakeTotalCornerSource{},
	})
	schedule := []MatchWindow{{ID: "m1", Start: now.Add(time.Minute), HomeTeam: "Canada", AwayTeam: "Brazil"}}
	if _, ok := controller.pendingKickoff(now, schedule); ok {
		t.Fatal("pending kickoff started before scheduled kickoff")
	}

	schedule[0].Start = now
	pending, ok := controller.pendingKickoff(now, schedule)
	if !ok {
		t.Fatal("pending kickoff did not start at scheduled kickoff")
	}
	if pending.deadline != now.Add(time.Hour) {
		t.Fatalf("deadline = %s, want kickoff + 1h", pending.deadline)
	}
}

func TestControllerScheduledLiveWindowContinuesAfterPendingTimeout(t *testing.T) {
	kickoff := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	controller := NewController(nil, ControllerConfig{
		PendingKickoffTimeout: time.Hour,
		PostLiveGracePeriod:   10 * time.Minute,
		TotalCornerSource:     &fakeTotalCornerSource{},
	})
	schedule := []MatchWindow{{ID: "m1", Start: kickoff, HomeTeam: "Scotland", AwayTeam: "Brazil"}}
	now := kickoff.Add(61 * time.Minute)

	if _, ok := controller.pendingKickoff(now, schedule); ok {
		t.Fatal("pending kickoff should have elapsed")
	}
	live, ok := controller.scheduledLiveWindow(now, schedule)
	if !ok {
		t.Fatal("scheduled live window should remain active after pending timeout")
	}
	if len(live.matches) != 1 || live.matches[0].ID != "m1" {
		t.Fatalf("live matches = %+v, want m1", live.matches)
	}
	if want := kickoff.Add(estimatedMatchDuration).Add(10 * time.Minute); !live.deadline.Equal(want) {
		t.Fatalf("deadline = %s, want %s", live.deadline, want)
	}
}

func TestControllerScheduleCacheHonorsLookaheadTTL(t *testing.T) {
	now := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	controller := NewController(nil, ControllerConfig{
		ScheduleCachePath: filepath.Join(t.TempDir(), "cache.json"),
		ScheduleLookahead: 36 * time.Hour,
		TotalCornerSource: &fakeTotalCornerSource{},
	})
	schedule := []MatchWindow{{ID: "m1", Start: now.Add(time.Hour), HomeTeam: "Canada", AwayTeam: "Brazil"}}

	controller.saveScheduleCache(now, schedule)
	if got := controller.loadScheduleCache(now.Add(time.Hour)); len(got) != 1 {
		t.Fatalf("loaded matches = %d, want 1", len(got))
	}
	if got := controller.loadScheduleCache(now.Add(37 * time.Hour)); len(got) != 0 {
		t.Fatalf("loaded expired matches = %d, want 0", len(got))
	}
}

func TestControllerScheduleFailureAlertsAndRecovers(t *testing.T) {
	store := &testStore{subs: []models.Subscription{
		{GuildID: "g1", ChannelID: "c1", SubscriptionType: dealtypes.SubscriptionOnEveryCorner, DealType: dealtypes.OnEveryCornerPotentialGoals},
	}}
	notifier := &testNotifier{}
	processor := NewProcessor(store, notifier)
	controller := NewController(processor, ControllerConfig{TotalCornerSource: &fakeTotalCornerSource{}})
	base := time.Date(2026, 6, 20, 18, 0, 0, 0, time.UTC)
	controller.now = func() time.Time { return base.Add(16 * time.Minute) }

	controller.noteScheduleFailure(context.Background(), base, context.DeadlineExceeded)
	controller.noteScheduleFailure(context.Background(), base.Add(8*time.Minute), context.DeadlineExceeded)
	controller.noteScheduleFailure(context.Background(), base.Add(16*time.Minute), context.DeadlineExceeded)
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts after failures = %d, want 1", len(notifier.alerts))
	}
	if notifier.alerts[0].RawTitle != "OnEveryCorner TotalCorner issue" {
		t.Fatalf("alert title = %q", notifier.alerts[0].RawTitle)
	}

	controller.now = func() time.Time { return base.Add(17 * time.Minute) }
	controller.noteScheduleSuccess(context.Background(), base.Add(17*time.Minute))
	if len(notifier.alerts) != 2 {
		t.Fatalf("alerts after recovery = %d, want 2", len(notifier.alerts))
	}
	if notifier.alerts[1].RawTitle != "OnEveryCorner TotalCorner recovered" {
		t.Fatalf("recovery title = %q", notifier.alerts[1].RawTitle)
	}
}

type fakeTotalCornerSource struct {
	schedule []MatchWindow
	inplay   []MatchSnapshot
	err      error
}

func (f *fakeTotalCornerSource) Schedule(context.Context, time.Time, time.Time) ([]MatchWindow, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]MatchWindow(nil), f.schedule...), nil
}

func (f *fakeTotalCornerSource) InPlay(context.Context) ([]MatchSnapshot, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]MatchSnapshot(nil), f.inplay...), nil
}
