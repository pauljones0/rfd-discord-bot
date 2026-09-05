package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// DealProcessor owns only long-lived dependencies. All observations, title
// batches, reconciliation results and delivery attempts belong to one poll.
type DealProcessor struct {
	store               DealStore
	notifier            DealNotifier
	scraper             DealScraper
	validator           DealValidator
	config              *config.Config
	aiClient            DealAnalyzer
	updateInterval      time.Duration
	titleCleanupTimeout time.Duration
	mu                  sync.Mutex
}

type DealAnalyzer interface {
	CleanTitles(context.Context, []models.TitleRequest) (map[int]string, error)
	DrainTokens() (int, int)
}

func New(store DealStore, n DealNotifier, s DealScraper, v DealValidator, cfg *config.Config, ai DealAnalyzer) *DealProcessor {
	return &DealProcessor{store: store, notifier: n, scraper: s, validator: v, config: cfg,
		aiClient: ai, updateInterval: cfg.DiscordUpdateInterval, titleCleanupTimeout: 30 * time.Second}
}

// Keep existing identities compatible with deployed databases and imports.
func generateDealID(published time.Time) string {
	hash := sha256.Sum256([]byte(published.Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash[:])
}

func (p *DealProcessor) ProcessDeals(ctx context.Context) error {
	if !p.mu.TryLock() {
		return nil
	}
	defer p.mu.Unlock()
	logger := slog.With("processor", "rfd", "runID", time.Now().Format("20060102-150405"))
	tracker := metrics.NewTracker("rfd")
	defer tracker.LogSummary()

	// History and subscriptions are prerequisites: an unavailable store must not
	// turn existing deals into new alerts or silently lose delivery work.
	recent, err := p.store.GetRecentDeals(ctx, 48*time.Hour)
	if err != nil {
		return fmt.Errorf("load deduplication history: %w", err)
	}
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("load subscriptions: %w", err)
	}
	observed, err := p.scrapeAndValidate(ctx, logger, tracker)
	if err != nil {
		return err
	}
	existing, err := p.loadExistingDeals(ctx, observed, logger)
	if err != nil {
		return err
	}
	stats := p.enrichDealsWithDetails(ctx, observed, existing, logger)
	if rfdDetailFetchUnhealthy(stats) {
		return fmt.Errorf("rfd detail fetch unhealthy: attempted=%d succeeded=%d failed=%d not_found=%d", stats.Attempted, stats.Succeeded, stats.Failed, stats.NotFound)
	}
	// Resolve product identity before a fuzzy title match can group observations.
	// Different product links must veto a match instead of being discovered only
	// after the original document identities have already been overwritten.
	observed = p.deduplicateDeals(ctx, observed, existing, recent, logger)
	observed = p.deduplicateDealsByDetailedURL(ctx, observed, existing, recent, logger)

	groups := make(map[string][]models.DealInfo)
	for _, deal := range observed {
		groups[deal.DocumentID] = append(groups[deal.DocumentID], deal)
	}
	deals := make([]*models.DealInfo, 0, len(groups))
	for id, group := range groups {
		if deal := p.reconcileDeal(existing[id], group); deal != nil {
			deals = append(deals, deal)
		}
	}
	// A stable order makes retries, title batches, and channel delivery repeatable.
	sort.Slice(deals, func(i, j int) bool {
		if !deals[i].PublishedTimestamp.Equal(deals[j].PublishedTimestamp) {
			return deals[i].PublishedTimestamp.Before(deals[j].PublishedTimestamp)
		}
		return deals[i].DocumentID < deals[j].DocumentID
	})
	p.cleanTitles(ctx, deals, logger, tracker)

	var failures []error
	created := 0
	for _, deal := range deals {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		previous := existing[deal.DocumentID]
		p.applyRFDWarmHotState(deal)
		if previous == nil || !reflect.DeepEqual(*previous, *deal) {
			deal.LastUpdated = time.Now()
		}
		delivered, deliveryErr := p.deliver(ctx, deal, subs)
		if deliveryErr != nil {
			failures = append(failures, fmt.Errorf("deliver %s: %w", deal.DocumentID, deliveryErr))
		}
		for range delivered {
			tracker.TrackDiscordMessage()
		}
		if previous != nil && reflect.DeepEqual(*previous, *deal) {
			continue
		}
		var creates, updates []models.DealInfo
		if previous == nil {
			creates = []models.DealInfo{*deal}
		} else {
			updates = []models.DealInfo{*deal}
		}
		// Save every successful receipt before moving on to another deal. A cancelled
		// poll gets a short, bounded opportunity to retain already-sent messages.
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := p.store.BatchWrite(saveCtx, creates, updates)
		cancel()
		if err != nil {
			return errors.Join(append(failures, fmt.Errorf("save deal %s: %w", deal.DocumentID, err))...)
		}
		if previous == nil {
			created++
			tracker.TrackDealFound()
		}
	}
	if created > 0 {
		if err := p.store.TrimOldDeals(ctx, p.config.MaxStoredDeals); err != nil {
			failures = append(failures, fmt.Errorf("trim old deals: %w", err))
		}
	}
	return errors.Join(append(failures, ctx.Err())...)
}

// deliver reconciles channel receipts every poll, independently of source
// changes, so new subscriptions and failed sends do not need a title edit or vote.
func (p *DealProcessor) deliver(ctx context.Context, deal *models.DealInfo, subs []models.Subscription) (int, error) {
	// Confirmed-gone deals may clear their existing cards, but must not produce
	// a new alert when a subscription is added.
	if deal.PostURL == "" && len(deal.Threads) == 0 {
		subs = nil
	}
	var missing []models.Subscription
	seen := make(map[string]bool)
	for _, sub := range subs {
		if !seen[sub.ChannelID] && deal.DiscordMessageIDs[sub.ChannelID] == "" && p.isDealEligibleForSubscription(*deal, sub) {
			missing = append(missing, sub)
			seen[sub.ChannelID] = true
		}
	}
	// A receipt from a newly subscribed channel acknowledges only that channel.
	// Keep the earlier receipt set and its shared edit checkpoint until updates
	// to those messages have succeeded.
	previousReceipts := cloneDeal(*deal)
	var errs []error
	count := 0
	if len(missing) > 0 {
		receipts, err := p.notifier.Send(ctx, *deal, missing)
		if err != nil {
			errs = append(errs, err)
		}
		p.recordDiscordReceipts(deal, receipts)
		count = len(receipts)
		if count > 0 && len(previousReceipts.DiscordMessageIDs) == 0 {
			deal.DiscordLastUpdatedTime = time.Now()
		}
	}
	// A due edit may be a retry or publish a change saved during a previous poll's
	// edit cooldown. Imported receipts remain protected by notifier ownership.
	if len(previousReceipts.DiscordMessageIDs) > 0 && deal.LastUpdated.After(deal.DiscordLastUpdatedTime) && time.Since(deal.DiscordLastUpdatedTime) >= p.updateInterval && time.Since(deal.PublishedTimestamp) < 2*time.Hour {
		update := *deal
		update.DiscordMessageIDs = previousReceipts.DiscordMessageIDs
		update.DiscordMessageApplicationIDs = previousReceipts.DiscordMessageApplicationIDs
		if err := p.notifier.Update(ctx, update); err != nil {
			errs = append(errs, err)
		} else {
			deal.DiscordLastUpdatedTime = time.Now()

		}
	}
	return count, errors.Join(errs...)
}

func (p *DealProcessor) recordDiscordReceipts(deal *models.DealInfo, receipts map[string]string) {
	if len(receipts) == 0 {
		return
	}
	if deal.DiscordMessageIDs == nil {
		deal.DiscordMessageIDs = make(map[string]string)
	}
	if p.config.DiscordAppID != "" && deal.DiscordMessageApplicationIDs == nil {
		deal.DiscordMessageApplicationIDs = make(map[string]string)
	}
	for channel, message := range receipts {
		deal.DiscordMessageIDs[channel] = message
		if p.config.DiscordAppID != "" {
			deal.DiscordMessageApplicationIDs[channel] = p.config.DiscordAppID
		}
	}
}
