package memoryexpress

import (
	"context"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

func TestHasCloudflareChallenge(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "cloudflare challenge body is detected",
			body: "Just a moment... Enable JavaScript and cookies to continue /cdn-cgi/challenge-platform/ challenge-form",
			want: true,
		},
		{
			name: "normal html is not flagged",
			body: `<html><body><div class="c-clli-group"></div></body></html>`,
			want: false,
		},
		{
			name: "cloudflare script on loaded clearance page is not flagged",
			body: `<html><body><script src="/cdn-cgi/challenge-platform/h/g/scripts/jsd/main.js"></script><div class="c-clli-group"><div class="c-clli-item"></div></div></body></html>`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasCloudflareChallenge(tt.body); got != tt.want {
				t.Fatalf("hasCloudflareChallenge() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScrapeWithBackends_FallsBackAfterCloudflareChallenge(t *testing.T) {
	original := fetchBackendHTML
	defer func() { fetchBackendHTML = original }()

	var attempted []string
	fetchBackendHTML = func(_ context.Context, backend, _, _ string, _ bool, _ func(context.Context) error) scrapebackend.FetchResult {
		attempted = append(attempted, backend)
		if backend == "http" {
			return scrapebackend.FetchResult{
				Backend:     backend,
				HTML:        "Just a moment... Enable JavaScript and cookies to continue /cdn-cgi/challenge-platform/ challenge-form",
				BlockSignal: "cloudflare-managed-challenge",
			}
		}
		return scrapebackend.FetchResult{
			Backend: backend,
			HTML: `<html><body>
				<div class="c-clli-group">
					<div class="c-clli-group__header-title">Components</div>
					<div class="c-clli-item">
						<div class="c-clli-item-info__title"><a href="/Products/MX001">Great GPU</a></div>
						<div class="c-clli-item-info__codes">SKU: MX001 ILC: 123</div>
						<div class="c-clli-item-price__regular">$499.99</div>
						<div class="c-clli-item-price__clearance-value">$299.99</div>
						<div class="c-clli-item__stock">2</div>
					</div>
				</div>
			</body></html>`,
		}
	}

	products, err := ScrapeWithBackends(context.Background(), "SKST", []string{"http", "chromedp-cloudrun"}, "", false)
	if err != nil {
		t.Fatalf("ScrapeWithBackends() error = %v", err)
	}
	if len(products) != 1 {
		t.Fatalf("products = %d, want 1", len(products))
	}
	if len(attempted) != 2 || attempted[0] != "http" || attempted[1] != "chromedp-cloudrun" {
		t.Fatalf("attempted = %v, want http then chromedp-cloudrun", attempted)
	}
}
