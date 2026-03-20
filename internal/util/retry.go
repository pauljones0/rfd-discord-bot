package util

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// RetryWithBackoff calls fn up to maxRetries+1 times with exponential backoff
// and jitter. fn receives the current attempt number (0-indexed). It should
// return nil on success.
// If the context is cancelled, RetryWithBackoff returns the context error immediately.
func RetryWithBackoff(ctx context.Context, maxRetries int, fn func(attempt int) error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = fn(attempt)
		if lastErr == nil {
			return nil
		}

		// Don't wait after the last attempt
		if attempt == maxRetries {
			break
		}

		// Check context before sleeping
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Exponential backoff with jitter: base * 2^attempt, then add up to 50% jitter
		base := time.Duration(1<<attempt) * time.Second
		jitter := time.Duration(rand.Int64N(int64(base / 2)))
		backoff := base + jitter
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}
