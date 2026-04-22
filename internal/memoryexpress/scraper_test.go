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
			body: "Just a moment... Enable JavaScript and cookies to continue /cdn-cgi/challenge-platform/",
			want: true,
		},
		{
			name: "normal html is not flagged",
			body: `<html><body><div class="c-clli-group"></div></body></html>`,
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
