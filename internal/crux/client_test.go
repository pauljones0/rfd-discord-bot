package crux

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

func TestPageURLUsesRawQuerySeparators(t *testing.T) {
	client := NewClient(ClientConfig{
		BaseURL:   "https://www.cruxinvestor.com/companies",
		Exchanges: []string{"TSXV", "TSX", "CSE"},
	})

	got := client.pageURL(65)
	if strings.Contains(got, "&amp;") {
		t.Fatalf("pageURL() = %q, must not contain escaped ampersand", got)
	}
	if !strings.Contains(got, "97d0d7a7_page=65") || !strings.Contains(got, "ticker=TSXV%2CTSX%2CCSE") {
		t.Fatalf("pageURL() = %q, want page and ticker query params", got)
	}
}

func TestFetchPageReturnsParentCancellationWithoutParseNoise(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient(ClientConfig{
		Backends:  []string{scrapebackend.BackendHTTP, scrapebackend.BackendExternalStealth},
		Timeout:   time.Second,
		PageDelay: time.Millisecond,
	})

	calls := 0
	client.fetchHTML = func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult {
		calls++
		cancel()
		return scrapebackend.FetchResult{
			Backend: scrapebackend.BackendHTTP,
			Error:   context.Canceled.Error(),
		}
	}

	_, _, _, err := client.FetchPage(ctx, 65)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchPage() error = %v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "company list not found") {
		t.Fatalf("FetchPage() error = %v, should not report parser failure for canceled empty fetch", err)
	}
	if calls != 1 {
		t.Fatalf("fetch calls = %d, want no fallback after parent cancellation", calls)
	}
}

func TestFetchPageReportsEmptyBackendResponseClearly(t *testing.T) {
	client := NewClient(ClientConfig{
		Backends: []string{scrapebackend.BackendHTTP},
		Timeout:  time.Second,
	})
	client.fetchHTML = func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult {
		return scrapebackend.FetchResult{
			Backend:    scrapebackend.BackendHTTP,
			StatusCode: 403,
			Error:      "HTTP 403",
		}
	}

	_, _, _, err := client.FetchPage(context.Background(), 65)
	if err == nil {
		t.Fatal("FetchPage() error = nil, want failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") || !strings.Contains(err.Error(), "parse:empty response") {
		t.Fatalf("FetchPage() error = %v, want status and empty response", err)
	}
	if strings.Contains(err.Error(), "company list not found") {
		t.Fatalf("FetchPage() error = %v, should not parse an empty response as a Crux page", err)
	}
}
