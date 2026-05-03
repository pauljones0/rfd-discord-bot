package scrapebackend

import (
	"context"
	"errors"
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
