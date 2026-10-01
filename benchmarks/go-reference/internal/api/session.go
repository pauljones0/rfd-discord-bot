package api

import (
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
)

// DiscordGo's automatic rate-limit retry sleeps without checking the request
// context. These short command and preflight requests must return the error so
// their caller can retry within a fresh deadline.
func newSession(token string) (*discordgo.Session, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.MaxRestRetries = 0
	s.ShouldRetryOnRateLimit = false
	s.Client.Timeout = 10 * time.Second
	s.Client.Transport = noWaitDiscordTransport{base: http.DefaultTransport}
	return s, nil
}

// The SDK also caches response headers and sleeps before later requests,
// including after retry-on-429 is disabled. Keep these one-shot operations free
// of that uncancellable wait. A rate-limited operation returns to its caller;
// message delivery has its own context-aware rate limiter in notifier.
type noWaitDiscordTransport struct{ base http.RoundTripper }

func (t noWaitDiscordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err == nil {
		resp.Header = resp.Header.Clone()
		for _, name := range []string{"X-RateLimit-Remaining", "X-RateLimit-Reset", "X-RateLimit-Reset-After", "X-RateLimit-Global"} {
			resp.Header.Del(name)
		}
	}
	return resp, err
}
