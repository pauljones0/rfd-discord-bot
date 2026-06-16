package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
