package scrapebackend

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestDetectBlockSignal(t *testing.T) {
	tests := []struct {
		name string
		code int
		body string
		want string
	}{
		{
			name: "cloudflare managed challenge",
			body: "Just a moment... Enable JavaScript and cookies to continue /cdn-cgi/challenge-platform/",
			want: "cloudflare-managed-challenge",
		},
		{
			name: "akamai access denied",
			body: `Access Denied You don't have permission to access this server. https://errors.edgesuite.net/`,
			want: "akamai-access-denied",
		},
		{
			name: "status forbidden",
			code: 403,
			body: "Forbidden",
			want: "http-403",
		},
		{
			name: "normal page",
			body: "<html><body>ok</body></html>",
			want: "",
		},
		{
			name: "normal page with captcha CSS",
			body: `<html><head><style>.ifh-captcha .ifh-captcha-header{display:block}</style></head><body><meta property="og:type" content="ebay-objects:item"></body></html>`,
			want: "",
		},
		{
			name: "explicit captcha challenge",
			body: "<html><body>Please verify you are human to continue</body></html>",
			want: "captcha",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectBlockSignal(tt.code, tt.body); got != tt.want {
				t.Fatalf("DetectBlockSignal() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetchHTMLPaidTrialRunsAttemptGuardBeforeCommand(t *testing.T) {
	called := false
	result := FetchHTML(context.Background(), FetchOptions{
		Backend:     BackendPaidTrial,
		URL:         "https://example.com",
		Timeout:     time.Second,
		PaidEnabled: true,
		PaidCommand: "should-not-run",
		PaidAttempt: func(context.Context) error {
			called = true
			return errors.New("cap reached")
		},
	})

	if !called {
		t.Fatalf("PaidAttempt was not called")
	}
	if result.Error == "" || result.Error != "cap reached" {
		t.Fatalf("result error = %q, want cap reached", result.Error)
	}
}

func TestCommandArgsFromEnv(t *testing.T) {
	t.Setenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS", `["python","scripts/fetch.py","{url}","--headless"]`)

	got := CommandArgsFromEnv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS")
	want := []string{"python", "scripts/fetch.py", "{url}", "--headless"}
	if !equalStringSlices(got, want) {
		t.Fatalf("CommandArgsFromEnv() = %#v, want %#v", got, want)
	}
}

func TestCommandArgsFromEnvSkipsBlankArgs(t *testing.T) {
	t.Setenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS", `["python","","scripts/fetch.py"]`)

	got := CommandArgsFromEnv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS")
	want := []string{"python", "scripts/fetch.py"}
	if !equalStringSlices(got, want) {
		t.Fatalf("CommandArgsFromEnv() = %#v, want %#v", got, want)
	}
}

func TestCommandArgsFromEnvInvalidJSON(t *testing.T) {
	t.Setenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS", `python scripts/fetch.py {url}`)

	if got := CommandArgsFromEnv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS"); got != nil {
		t.Fatalf("CommandArgsFromEnv() = %#v, want nil", got)
	}
}

func TestCommandArgsWithTargetOnlyReplacesFullArg(t *testing.T) {
	got := commandArgsWithTarget([]string{"python", "scripts/fetch.py", "prefix-{url}", "{url}"}, "https://example.com/item?id=1")
	want := []string{"python", "scripts/fetch.py", "prefix-{url}", "https://example.com/item?id=1"}
	if !equalStringSlices(got, want) {
		t.Fatalf("commandArgsWithTarget() = %#v, want %#v", got, want)
	}
}

func TestFilterBackendsForPaidEnabled(t *testing.T) {
	backends := []string{BackendHTTP, BackendPaidTrial, BackendAICrawler}

	got := FilterBackendsForPaidEnabled(backends, false)
	want := []string{BackendHTTP, BackendAICrawler}
	if !equalStringSlices(got, want) {
		t.Fatalf("disabled backends = %#v, want %#v", got, want)
	}

	got = FilterBackendsForPaidEnabled(backends, true)
	if !equalStringSlices(got, backends) {
		t.Fatalf("enabled backends = %#v, want %#v", got, backends)
	}
}

func TestAttemptCounterRecordsFailuresAndVerdicts(t *testing.T) {
	counter := NewAttemptCounter()

	counter.RecordAttempt(BackendHTTP)
	counter.RecordFetchResult(BackendHTTP, FetchResult{
		StatusCode:  http.StatusForbidden,
		BlockSignal: "akamai-access-denied",
		Error:       "blocked",
	})
	counter.RecordAttempt(BackendAICrawler)
	counter.RecordVerdict(BackendAICrawler, "no_comps")
	counter.RecordAttempt(BackendCamoufox)
	counter.RecordParseError(BackendCamoufox)

	if got := counter.TotalAttempts(); got != 3 {
		t.Fatalf("TotalAttempts() = %d, want 3", got)
	}
	if !counter.HasFailures() {
		t.Fatalf("HasFailures() = false, want true")
	}
	if got := FormatCounts(counter.attempts); got != "ai-crawler=1,camoufox=1,http=1" {
		t.Fatalf("attempts = %q", got)
	}
	if got := FormatCounts(counter.failures); got != "camoufox=1,http=1" {
		t.Fatalf("failures = %q", got)
	}
	if got := FormatCounts(counter.blocks); got != "http:akamai-access-denied=1" {
		t.Fatalf("blocks = %q", got)
	}
	if got := FormatCounts(counter.errors); got != "http=1" {
		t.Fatalf("errors = %q", got)
	}
	if got := FormatCounts(counter.parseErrors); got != "camoufox=1" {
		t.Fatalf("parse errors = %q", got)
	}
	if got := FormatCounts(counter.verdicts); got != "ai-crawler:no_comps=1" {
		t.Fatalf("verdicts = %q", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
