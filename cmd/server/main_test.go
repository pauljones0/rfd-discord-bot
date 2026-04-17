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

func TestProcessDealsHandler_StartsProcessingAndReturnsAccepted(t *testing.T) {
	p := &testProcessor{called: make(chan struct{}, 1)}
	srv := &Server{
		processor: p,
		sem:       make(chan struct{}, 1),
	}

	req := httptest.NewRequest(http.MethodGet, "/process-deals", nil)
	rec := httptest.NewRecorder()

	srv.ProcessDealsHandler(rec, req)
	srv.wg.Wait()

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusAccepted)
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
