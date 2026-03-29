package hardwareswap

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// RateLimiter provides a simple in-memory token bucket rate limiter.
type RateLimiter struct {
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		lastSeen: make(map[string]time.Time),
	}
}

// Allow checks if the given userID is allowed to perform an action (max 1 request per 2 seconds).
func (rl *RateLimiter) Allow(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	last, ok := rl.lastSeen[userID]
	if ok && time.Since(last) < 2*time.Second {
		return false
	}
	rl.lastSeen[userID] = time.Now()
	return true
}

var sanitizeRegex = regexp.MustCompile(`[^a-zA-Z0-9\s.,!?-]`)

// Sanitize cleans user input to prevent injection or formatting abuse.
func Sanitize(input string) string {
	if len(input) > 500 {
		input = input[:500]
	}
	input = sanitizeRegex.ReplaceAllString(input, "")
	return strings.TrimSpace(input)
}
