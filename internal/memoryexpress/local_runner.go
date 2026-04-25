package memoryexpress

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const defaultLocalPollInterval = 30 * time.Minute

// SubscriptionStore loads Memory Express subscriptions for the local runner.
type SubscriptionStore interface {
	GetMemExpressSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// LocalRunner keeps a persistent local browser session aligned with active subscriptions.
type LocalRunner struct {
	store     SubscriptionStore
	processor *Processor
	session   *LocalBrowserSession
	interval  time.Duration
}

// NewLocalRunner creates a local Memory Express runner.
func NewLocalRunner(store SubscriptionStore, processor *Processor, session *LocalBrowserSession, interval time.Duration) *LocalRunner {
	if interval <= 0 {
		interval = defaultLocalPollInterval
	}
	return &LocalRunner{
		store:     store,
		processor: processor,
		session:   session,
		interval:  interval,
	}
}

// Run starts the local polling loop and blocks until the context is cancelled.
func (r *LocalRunner) Run(ctx context.Context) error {
	for {
		if err := r.runCycle(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("Memory Express local runner cycle failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.interval):
		}
	}
}

func (r *LocalRunner) runCycle(ctx context.Context) error {
	subs, err := r.store.GetMemExpressSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("load Memory Express subscriptions: %w", err)
	}

	storeCodes := subscribedStoreCodes(subs)
	if err := r.session.SyncStores(ctx, storeCodes); err != nil {
		return fmt.Errorf("sync Memory Express tabs: %w", err)
	}

	slog.Info("Memory Express local runner synced tabs",
		"stores", len(storeCodes),
		"store_codes", storeCodes,
	)

	if err := r.processor.ProcessMemExpressDeals(ctx); err != nil {
		return fmt.Errorf("process Memory Express deals: %w", err)
	}

	return nil
}

func subscribedStoreCodes(subs []models.Subscription) []string {
	storeCodes := make([]string, 0, len(subs))
	for _, sub := range subs {
		if sub.SubscriptionType != "memoryexpress" {
			continue
		}
		storeCodes = append(storeCodes, sub.StoreCode)
	}
	return normalizeStoreCodes(storeCodes)
}

func normalizeStoreCodes(storeCodes []string) []string {
	unique := make(map[string]struct{}, len(storeCodes))
	for _, code := range storeCodes {
		if !ValidStoreCode(code) {
			continue
		}
		unique[code] = struct{}{}
	}

	normalized := make([]string, 0, len(unique))
	for code := range unique {
		normalized = append(normalized, code)
	}
	sort.Strings(normalized)
	return normalized
}
