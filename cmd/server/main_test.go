package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type testProcessor struct {
	called chan struct{}
}

func (p *testProcessor) ProcessDeals(ctx context.Context) error {
	select {
	case p.called <- struct{}{}:
	default:
	}
	return nil
}

type scheduledAlertTestStore struct {
	subs []models.Subscription
}

func (s *scheduledAlertTestStore) GetDealByID(context.Context, string) (*models.DealInfo, error) {
	return nil, nil
}

func (s *scheduledAlertTestStore) GetDealsByIDs(context.Context, []string) (map[string]*models.DealInfo, error) {
	return map[string]*models.DealInfo{}, nil
}

func (s *scheduledAlertTestStore) GetRecentDeals(context.Context, time.Duration) ([]models.DealInfo, error) {
	return nil, nil
}

func (s *scheduledAlertTestStore) TryCreateDeal(context.Context, models.DealInfo) error {
	return nil
}

func (s *scheduledAlertTestStore) UpdateDeal(context.Context, models.DealInfo) error {
	return nil
}

func (s *scheduledAlertTestStore) TrimOldDeals(context.Context, int) error {
	return nil
}

func (s *scheduledAlertTestStore) BatchWrite(context.Context, []models.DealInfo, []models.DealInfo) error {
	return nil
}

func (s *scheduledAlertTestStore) Ping(context.Context) error {
	return nil
}

func (s *scheduledAlertTestStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subs, nil
}

type scheduledAlertTestNotifier struct {
	alerts []models.CoreSystemAlert
	subs   [][]models.Subscription
	err    error
}

func (n *scheduledAlertTestNotifier) SendCoreSystemAlert(_ context.Context, alert models.CoreSystemAlert, subs []models.Subscription) error {
	n.alerts = append(n.alerts, alert)
	n.subs = append(n.subs, append([]models.Subscription{}, subs...))
	return n.err
}

func TestProcessDealsHandler_RunsInlineAndReturnsOK(t *testing.T) {
	p := &testProcessor{called: make(chan struct{}, 1)}
	srv := &Server{
		processor: p,
		sem:       make(chan struct{}, 1),
	}

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	rec := httptest.NewRecorder()

	srv.ProcessDealsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	select {
	case <-p.called:
	case <-time.After(2 * time.Second):
		t.Fatal("expected ProcessDeals to be called")
	}
}

func TestProcessDealsHandler_ReturnsBusyWhenSemaphoreFull(t *testing.T) {
	srv := &Server{
		sem: make(chan struct{}, 1),
	}
	srv.sem <- struct{}{}

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	rec := httptest.NewRecorder()

	srv.ProcessDealsHandler(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["status"] != "busy" {
		t.Fatalf("status body = %q, want %q", body["status"], "busy")
	}
}

func TestRunScheduledJobRunsWhenAvailable(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := &Server{}
	sem := make(chan struct{}, 1)

	ran := srv.runScheduledJob(context.Background(), "test", sem, time.Second, func(context.Context) error {
		called <- struct{}{}
		return nil
	})

	if !ran {
		t.Fatal("runScheduledJob() = false, want true")
	}
	select {
	case <-called:
	default:
		t.Fatal("scheduled job did not call processor")
	}
	if len(sem) != 0 {
		t.Fatalf("semaphore length = %d, want released", len(sem))
	}
}

func TestRunScheduledJobSkipsWhenSemaphoreBusy(t *testing.T) {
	called := false
	srv := &Server{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	ran := srv.runScheduledJob(context.Background(), "test", sem, time.Second, func(context.Context) error {
		called = true
		return nil
	})

	if ran {
		t.Fatal("runScheduledJob() = true, want false")
	}
	if called {
		t.Fatal("processor should not be called when semaphore is busy")
	}
	if len(sem) != 1 {
		t.Fatalf("semaphore length = %d, want busy token preserved", len(sem))
	}
}

func TestRunScheduledJobSendsFailureAndRecoveryAlerts(t *testing.T) {
	notifier := &scheduledAlertTestNotifier{}
	srv := &Server{
		store: &scheduledAlertTestStore{subs: []models.Subscription{
			{GuildID: "guild", ChannelID: "rfd-channel", SubscriptionType: dealtypes.SubscriptionRFD, DealType: dealtypes.RFDAll},
		}},
		systemNotifier:    notifier,
		schedulerFailures: make(map[string]scheduledProcessorFailure),
	}
	sem := make(chan struct{}, 1)

	ran := srv.runScheduledJob(context.Background(), "rfd", sem, time.Second, func(context.Context) error {
		return errors.New("context deadline exceeded")
	})

	if !ran {
		t.Fatal("runScheduledJob() = false, want true")
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("alerts = %d, want failure alert", len(notifier.alerts))
	}
	if notifier.alerts[0].Title != "RFD monitor failure" {
		t.Fatalf("failure title = %q", notifier.alerts[0].Title)
	}
	if len(notifier.subs) != 1 || len(notifier.subs[0]) != 1 || notifier.subs[0][0].ChannelID != "rfd-channel" {
		t.Fatalf("failure subs = %#v", notifier.subs)
	}

	ran = srv.runScheduledJob(context.Background(), "rfd", sem, time.Second, func(context.Context) error {
		return nil
	})

	if !ran {
		t.Fatal("second runScheduledJob() = false, want true")
	}
	if len(notifier.alerts) != 2 {
		t.Fatalf("alerts = %d, want failure and recovery", len(notifier.alerts))
	}
	if notifier.alerts[1].Title != "RFD monitor recovered" {
		t.Fatalf("recovery title = %q", notifier.alerts[1].Title)
	}
}

func TestScheduledFailureRetriesAlertWhenDiscordSendFails(t *testing.T) {
	notifier := &scheduledAlertTestNotifier{err: errors.New("discord unavailable")}
	srv := &Server{
		store: &scheduledAlertTestStore{subs: []models.Subscription{
			{GuildID: "guild", ChannelID: "rfd-channel", SubscriptionType: dealtypes.SubscriptionRFD, DealType: dealtypes.RFDAll},
		}},
		systemNotifier:    notifier,
		schedulerFailures: make(map[string]scheduledProcessorFailure),
	}

	srv.reportScheduledProcessorFailure("rfd", time.Second, time.Second, errors.New("context deadline exceeded"))
	srv.reportScheduledProcessorFailure("rfd", time.Second, time.Second, errors.New("context deadline exceeded"))

	if len(notifier.alerts) != 2 {
		t.Fatalf("alert attempts = %d, want retry when Discord send fails", len(notifier.alerts))
	}
	if state := srv.schedulerFailures["rfd"]; state.AlertSent {
		t.Fatalf("AlertSent = true after failed Discord send")
	}

	notifier.err = nil
	srv.reportScheduledProcessorFailure("rfd", time.Second, time.Second, errors.New("context deadline exceeded"))
	if len(notifier.alerts) != 3 {
		t.Fatalf("alert attempts after recovery of notifier = %d, want 3", len(notifier.alerts))
	}
	if state := srv.schedulerFailures["rfd"]; !state.AlertSent {
		t.Fatalf("AlertSent = false after successful Discord send")
	}
}

func TestScheduledProcessorSubscriptionsRoutesBestBuyComputeSeparately(t *testing.T) {
	subs := []models.Subscription{
		{ChannelID: "bestbuy-deals", SubscriptionType: dealtypes.SubscriptionBestBuy, DealType: dealtypes.BestBuyWarmHot},
		{ChannelID: "bestbuy-compute", SubscriptionType: dealtypes.SubscriptionBestBuy, DealType: dealtypes.BestBuyCompute},
		{ChannelID: "bestbuy-compute", SubscriptionType: dealtypes.SubscriptionBestBuy, DealType: dealtypes.BestBuyCompute},
	}

	dealSubs := scheduledProcessorSubscriptions("bestbuy", subs)
	if len(dealSubs) != 1 || dealSubs[0].ChannelID != "bestbuy-deals" {
		t.Fatalf("bestbuy subs = %#v, want only non-compute Best Buy channel", dealSubs)
	}

	computeSubs := scheduledProcessorSubscriptions("bestbuy_compute", subs)
	if len(computeSubs) != 1 || computeSubs[0].ChannelID != "bestbuy-compute" {
		t.Fatalf("bestbuy_compute subs = %#v, want deduped compute channel only", computeSubs)
	}
}

func TestRootHandler_ReturnsOK(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	rootHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status body = %q, want %q", body["status"], "ok")
	}
}

func TestRootHandler_ReturnsNotFoundForUnknownPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/process", nil)
	rec := httptest.NewRecorder()

	rootHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestAdminOnlyRejectsMissingConfiguredToken(t *testing.T) {
	called := false
	handler := adminOnly("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	req.Header.Set("Authorization", "Bearer supplied")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if called {
		t.Fatal("handler should not be called when admin token is not configured")
	}
}

func TestAdminOnlyRejectsInvalidBearer(t *testing.T) {
	called := false
	handler := adminOnly("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if called {
		t.Fatal("handler should not be called with invalid bearer token")
	}
}

func TestAdminOnlyAllowsValidBearer(t *testing.T) {
	called := false
	handler := adminOnly("secret-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !called {
		t.Fatal("handler was not called with valid bearer token")
	}
}

func TestSwordswallowerOnlyAllowsListenerSecretHeader(t *testing.T) {
	called := false
	handler := swordswallowerOnly("admin-token", "listener-token", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", nil)
	req.Header.Set("X-Swordswallower-Secret", "listener-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !called {
		t.Fatal("handler was not called with valid listener secret")
	}
}

func TestSwordswallowerOnlyAllowsAdminBearer(t *testing.T) {
	called := false
	handler := swordswallowerOnly("admin-token", "", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/ingest/discord-notification", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
	}
	if !called {
		t.Fatal("handler was not called with valid admin bearer")
	}
}

func TestLoggingMiddlewareRecordsResponseStatusAndBytes(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previous)
	})

	handler := loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		if _, err := w.Write([]byte("short")); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodGet, "/teapot", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	got := logs.String()
	for _, want := range []string{"HTTP Request Completed", "status=418", "bytes=5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("logs missing %q: %s", want, got)
		}
	}
}
