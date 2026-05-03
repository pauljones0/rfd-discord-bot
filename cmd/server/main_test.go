package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
