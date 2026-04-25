package memoryexpress

import "testing"

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
