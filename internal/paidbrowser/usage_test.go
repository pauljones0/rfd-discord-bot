package paidbrowser

import (
	"context"
	"testing"
)

type memoryUsageStore struct {
	values map[string]Usage
}

func (s *memoryUsageStore) GetPaidBrowserUsage(_ context.Context, site, day string) (*Usage, error) {
	if s.values == nil {
		s.values = make(map[string]Usage)
	}
	usage, ok := s.values[site+"_"+day]
	if !ok {
		return nil, nil
	}
	return &usage, nil
}

func (s *memoryUsageStore) SavePaidBrowserUsage(_ context.Context, usage Usage) error {
	if s.values == nil {
		s.values = make(map[string]Usage)
	}
	s.values[usage.Site+"_"+usage.Day] = usage
	return nil
}

func TestLimiterCountsAttemptsAndRespectsRunCap(t *testing.T) {
	store := &memoryUsageStore{}
	limiter := NewLimiter(store, "ebay", 1, 10)
	limiter.BeginRun()

	if err := limiter.BeforeAttempt(context.Background()); err != nil {
		t.Fatalf("first attempt error = %v", err)
	}
	if err := limiter.BeforeAttempt(context.Background()); err == nil {
		t.Fatalf("second attempt error = nil, want run cap")
	}
}

func TestLimiterRespectsDailyCap(t *testing.T) {
	store := &memoryUsageStore{}
	limiter := NewLimiter(store, "ebay", 5, 1)
	limiter.BeginRun()

	if err := limiter.BeforeAttempt(context.Background()); err != nil {
		t.Fatalf("first attempt error = %v", err)
	}
	limiter.BeginRun()
	if err := limiter.BeforeAttempt(context.Background()); err == nil {
		t.Fatalf("second run attempt error = nil, want daily cap")
	}
}

func TestLimiterDisabledWhenCapsAreZero(t *testing.T) {
	limiter := NewLimiter(&memoryUsageStore{}, "memoryexpress", 0, 0)
	limiter.BeginRun()
	if err := limiter.BeforeAttempt(context.Background()); err == nil {
		t.Fatalf("attempt error = nil, want disabled")
	}
}
