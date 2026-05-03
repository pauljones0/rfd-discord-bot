package paidbrowser

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

type Usage struct {
	Site      string    `docstore:"site" docstore:"site"`
	Day       string    `docstore:"day" docstore:"day"`
	Attempts  int       `docstore:"attempts" docstore:"attempts"`
	UpdatedAt time.Time `docstore:"updatedAt" docstore:"updatedAt"`
}

type Store interface {
	GetPaidBrowserUsage(ctx context.Context, site, day string) (*Usage, error)
	SavePaidBrowserUsage(ctx context.Context, usage Usage) error
}

type Limiter struct {
	store     Store
	site      string
	maxPerRun int
	maxPerDay int

	mu       sync.Mutex
	runCount int
}

func NewLimiter(store Store, site string, maxPerRun, maxPerDay int) *Limiter {
	return &Limiter{
		store:     store,
		site:      strings.ToLower(strings.TrimSpace(site)),
		maxPerRun: maxPerRun,
		maxPerDay: maxPerDay,
	}
}

func (l *Limiter) BeginRun() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.runCount = 0
}

func (l *Limiter) BeforeAttempt(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if l.site == "" {
		return fmt.Errorf("paid browser site is required")
	}
	if l.maxPerRun <= 0 {
		return fmt.Errorf("paid browser disabled for %s: per-run cap is %d", l.site, l.maxPerRun)
	}
	if l.maxPerDay <= 0 {
		return fmt.Errorf("paid browser disabled for %s: daily cap is %d", l.site, l.maxPerDay)
	}
	if l.store == nil {
		return fmt.Errorf("paid browser usage store is not configured")
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.runCount >= l.maxPerRun {
		return fmt.Errorf("paid browser run cap reached for %s: %d/%d", l.site, l.runCount, l.maxPerRun)
	}

	day := time.Now().UTC().Format("2006-01-02")
	usage, err := l.store.GetPaidBrowserUsage(ctx, l.site, day)
	if err != nil {
		return fmt.Errorf("load paid browser usage for %s: %w", l.site, err)
	}
	if usage == nil {
		usage = &Usage{Site: l.site, Day: day}
	}
	if usage.Attempts >= l.maxPerDay {
		return fmt.Errorf("paid browser daily cap reached for %s: %d/%d", l.site, usage.Attempts, l.maxPerDay)
	}

	usage.Attempts++
	usage.UpdatedAt = time.Now()
	if err := l.store.SavePaidBrowserUsage(ctx, *usage); err != nil {
		return fmt.Errorf("save paid browser usage for %s: %w", l.site, err)
	}
	l.runCount++
	return nil
}
