package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

type Processor interface {
	ProcessDeals(ctx context.Context) error
}

type DealProcessor struct {
	store          DealStore
	notifier       DealNotifier
	scraper        DealScraper
	validator      DealValidator
	config         *config.Config
	aiClient       DealAnalyzer
	updateInterval time.Duration
	mu             sync.Mutex // prevents overlapping ProcessDeals runs

	// Title batch queue — accumulates across scrape cycles
	titleQueue      []models.TitleRequest
	titleQueueDeals []*models.DealInfo // parallel slice: deal pointers to write clean titles back
	titleQueueStart time.Time          // time first item was queued
}

type DealAnalyzer interface {
	CleanTitles(ctx context.Context, requests []models.TitleRequest) (map[int]string, error)
	DrainTokens() (int, int)
}

func New(store DealStore, n DealNotifier, s DealScraper, v DealValidator, cfg *config.Config, ai DealAnalyzer) *DealProcessor {
	return &DealProcessor{
		store:          store,
		notifier:       n,
		scraper:        s,
		validator:      v,
		config:         cfg,
		aiClient:       ai,
		updateInterval: cfg.DiscordUpdateInterval,
	}
}

// generateDealID creates a stable deal identity based on PublishedTimestamp.
// This survives title and URL edits by the post author.
func generateDealID(published time.Time) string {
	hash := sha256.Sum256([]byte(published.Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash[:])
}

func (p *DealProcessor) ProcessDeals(ctx context.Context) error {
	// Prevent overlapping processing runs
	if !p.mu.TryLock() {
		slog.Info("ProcessDeals: already in progress, skipping", "processor", "rfd")
		return nil
	}
	defer p.mu.Unlock()

	runID := time.Now().Format("20060102-150405")
	logger := slog.With("processor", "rfd", "runID", runID)

	tracker := metrics.NewTracker("rfd")
	defer tracker.LogSummary()

	// Fetch Recent Deals for deduplication
	recentDeals, err := p.store.GetRecentDeals(ctx, 48*time.Hour)
	if err != nil {
		logger.Warn("Failed to get recent deals for deduplication", "error", err)
	}

	// 1. Scrape and Validate
	scrapedDeals, err := p.scrapeAndValidate(ctx, logger, tracker)
	if err != nil {
		return err
	}

	// 2. Load Existing Deals (Strict ID check)
	existingDeals, err := p.loadExistingDeals(ctx, scrapedDeals, logger)
	if err != nil {
		return err
	}

	// 3. Deduplicate
	validDeals := p.deduplicateDeals(ctx, scrapedDeals, existingDeals, recentDeals, logger)

	// 4. Fetch Details for New/Changed Deals
	detailStats := p.enrichDealsWithDetails(ctx, validDeals, existingDeals, logger)
	if rfdDetailFetchUnhealthy(detailStats) {
		return fmt.Errorf("rfd detail fetch unhealthy: attempted=%d succeeded=%d failed=%d not_found=%d",
			detailStats.Attempted,
			detailStats.Succeeded,
			detailStats.Failed,
			detailStats.NotFound,
		)
	}
	validDeals = p.deduplicateDealsByDetailedURL(ctx, validDeals, existingDeals, recentDeals, logger)

	// 5. AI Analysis for New Deals
	p.analyzeDeals(ctx, validDeals, existingDeals, logger, tracker)

	// 6. Fetch Subscriptions
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		logger.Error("Failed to get subscriptions, skipping notifications", "error", err)
	}

	// 7. Notify Discord and Prepare Updates
	newDeals, updatedDeals, errorMessages := p.processNotificationsAndPrepareUpdates(ctx, validDeals, existingDeals, subs, tracker)

	// 8. Batch Save
	// Optimization: Clear large text fields for AI processed deals to save storage
	// This prevents "leaky bucket" storage growth as requested
	for i := range newDeals {
		if newDeals[i].AIProcessed {
			newDeals[i].Description = ""
			newDeals[i].Comments = ""
			newDeals[i].Summary = ""
		}
	}
	for i := range updatedDeals {
		if updatedDeals[i].AIProcessed {
			updatedDeals[i].Description = ""
			updatedDeals[i].Comments = ""
			updatedDeals[i].Summary = ""
		}
	}
	if len(newDeals) > 0 || len(updatedDeals) > 0 {
		// 8a. Consolidated batch write
		if err := p.store.BatchWrite(ctx, newDeals, updatedDeals); err != nil {
			return fmt.Errorf("batch write failed: %w", err)
		}
		logger.Info("Batch write completed", "created", len(newDeals), "updated", len(updatedDeals))
	}

	// 9. Cleanup Old Deals
	if len(newDeals) > 0 {
		if err := p.store.TrimOldDeals(ctx, p.config.MaxStoredDeals); err != nil {
			logger.Warn("Failed to trim old deals", "error", err)
		}
	}

	if len(errorMessages) > 0 {
		return fmt.Errorf("processed with errors: %s", strings.Join(errorMessages, "; "))
	}
	return nil
}

// scrapeAndValidate scrapes the deal list and performs initial validation and ID assignment.
func (p *DealProcessor) scrapeAndValidate(ctx context.Context, logger *slog.Logger, tracker *metrics.Tracker) ([]models.DealInfo, error) {
	scrapedDeals, err := p.scraper.ScrapeDealList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}
	tracker.TrackAdsScraped(len(scrapedDeals))
	logger.Info("Successfully scraped deal list", "count", len(scrapedDeals))

	var validDeals []models.DealInfo
	for i := range scrapedDeals {
		deal := &scrapedDeals[i]

		// Validate using the validator
		if err := p.validator.ValidateStruct(deal); err != nil {
			logger.Error("Validation failed for deal", "title", deal.Title, "error", err)
			continue
		}

		deal.DocumentID = generateDealID(deal.PublishedTimestamp)
		if len(deal.Threads) > 0 {
			deal.Threads[0].DocumentID = deal.DocumentID
		}

		validDeals = append(validDeals, *deal)
	}
	return validDeals, nil
}

// loadExistingDeals fetches existing deals from storage corresponding to the valid scraped deals.
func (p *DealProcessor) loadExistingDeals(ctx context.Context, validDeals []models.DealInfo, logger *slog.Logger) (map[string]*models.DealInfo, error) {
	var idsToLookup []string
	for _, deal := range validDeals {
		idsToLookup = append(idsToLookup, deal.DocumentID)
	}

	existingDeals, err := p.store.GetDealsByIDs(ctx, idsToLookup)
	if err != nil {
		logger.Error("Batch read failed", "error", err)
		return nil, fmt.Errorf("failed to load existing deals: %w", err)
	}
	return existingDeals, nil
}

// enrichDealsWithDetails determines which deals need detail scraping (new or changed) and fetches them.
func (p *DealProcessor) enrichDealsWithDetails(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger) models.DealDetailFetchStats {
	var dealsToDetail []*models.DealInfo
	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.DocumentID]

		if existing == nil {
			// New deal — needs details
			dealsToDetail = append(dealsToDetail, deal)
			continue
		}

		// Optimization: Only fetch details if we actually need them.
		// We need details if:
		// 1. We don't have the ActualDealURL or Description yet.
		// 2. The PostURL changed (new thread/link).
		// 3. The Title changed (likely implies content update or significant edit).
		// Note: If AIProcessed is true, we expect Description to be empty (cleared), so don't re-fetch.
		existingPostURL := existing.PostURL
		if existingPostURL == "" {
			existingPostURL = existing.PrimaryPostURL()
		}
		postChanged := threadKey(existingPostURL) != threadKey(deal.PostURL)
		needsDetails := existing.ActualDealURL == "" ||
			(existing.Description == "" && !existing.AIProcessed) ||
			postChanged ||
			existing.Title != deal.Title

		if needsDetails {
			dealsToDetail = append(dealsToDetail, deal)
		} else {
			// Unchanged or only metrics changed — copy details from existing so we have them for AI (if needed) or storage
			deal.ActualDealURL = existing.ActualDealURL
			deal.ThreadImageURL = existing.ThreadImageURL
			deal.Description = existing.Description
			deal.Comments = existing.Comments
			deal.Summary = existing.Summary
		}
	}

	if len(dealsToDetail) > 0 {
		logger.Info("Fetching details for deals", "count", len(dealsToDetail))
		return p.scraper.FetchDealDetails(ctx, dealsToDetail)
	}
	logger.Info("No deals needed detail scraping")
	return models.DealDetailFetchStats{}
}

func rfdDetailFetchUnhealthy(stats models.DealDetailFetchStats) bool {
	return stats.Attempted >= 3 && stats.Succeeded == 0 && stats.Failed > 0
}

const (
	titleBatchSize     = 10
	titleBatchMaxDelay = 5 * time.Minute
)

// queueTitleCleaning adds a deal to the title batch queue.
func (p *DealProcessor) queueTitleCleaning(deal *models.DealInfo, index int) {
	if p.titleQueueStart.IsZero() {
		p.titleQueueStart = time.Now()
	}
	p.titleQueue = append(p.titleQueue, models.TitleRequest{
		Index:    index,
		Title:    deal.Title,
		Retailer: deal.Retailer,
		Price:    deal.Price,
	})
	p.titleQueueDeals = append(p.titleQueueDeals, deal)
}

// flushTitleQueue sends the accumulated title queue to AI for cleaning.
func (p *DealProcessor) flushTitleQueue(ctx context.Context, logger *slog.Logger, tracker *metrics.Tracker) {
	if len(p.titleQueue) == 0 {
		return
	}

	shouldFlush := len(p.titleQueue) >= titleBatchSize ||
		(!p.titleQueueStart.IsZero() && time.Since(p.titleQueueStart) >= titleBatchMaxDelay)

	if !shouldFlush {
		logger.Info("Title queue not ready to flush", "queued", len(p.titleQueue),
			"age", time.Since(p.titleQueueStart).Round(time.Second))
		return
	}

	logger.Info("Flushing title queue", "count", len(p.titleQueue))

	results, err := p.aiClient.CleanTitles(ctx, p.titleQueue)
	inTok, outTok := p.aiClient.DrainTokens()
	tracker.TrackGeminiCall(inTok, outTok)

	if err != nil {
		logger.Warn("Batch title cleaning failed, deals keep raw titles", "error", err)
	} else {
		for i, deal := range p.titleQueueDeals {
			if cleanTitle, ok := results[p.titleQueue[i].Index]; ok && cleanTitle != "" {
				deal.CleanTitle = cleanTitle
				deal.AIProcessed = true
				tracker.TrackAdProcessed()
			}
		}
	}

	// Clear the queue
	p.titleQueue = nil
	p.titleQueueDeals = nil
	p.titleQueueStart = time.Time{}
}

// analyzeDeals queues deals for batch title cleaning. No longer performs warm/hot AI analysis.
func (p *DealProcessor) analyzeDeals(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger, tracker *metrics.Tracker) {
	for i := range validDeals {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping title queueing", "remaining", len(validDeals)-i)
			break
		}

		deal := &validDeals[i]
		existing := existingDeals[deal.DocumentID]
		isNew := existing == nil

		// Queue for title cleaning if:
		// 1. New deal without a clean title
		// 2. Existing deal that hasn't been processed
		// 3. Title changed (invalidates previous clean title)
		needsTitle := isNew || !existing.AIProcessed
		if !needsTitle && existing != nil {
			if deal.Title != existing.Title {
				needsTitle = true
				logger.Info("Re-queuing title due to content change", "title", deal.Title)
			}
		}

		if needsTitle {
			p.queueTitleCleaning(deal, i)
		} else if existing != nil {
			// Carry over existing clean title
			deal.CleanTitle = existing.CleanTitle
			deal.AIProcessed = existing.AIProcessed
		}
	}

	// Try to flush the title queue
	p.flushTitleQueue(ctx, logger, tracker)
}

// processNotificationsAndPrepareUpdates sends/updates Discord notifications and prepares lists for DB persistence.
func (p *DealProcessor) processNotificationsAndPrepareUpdates(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, subs []models.Subscription, tracker *metrics.Tracker) ([]models.DealInfo, []models.DealInfo, []string) {
	var newDeals []models.DealInfo
	var updatedDeals []models.DealInfo
	var errorMessages []string

	// We need to group validDeals by document ID because deduplication might map multiple
	// scraped deals to the same ID.
	groupedDeals := make(map[string][]models.DealInfo)
	for _, deal := range validDeals {
		groupedDeals[deal.DocumentID] = append(groupedDeals[deal.DocumentID], deal)
	}

	for documentID, dealsGroup := range groupedDeals {
		if ctx.Err() != nil {
			slog.Warn("Context cancelled, stopping notification processing", "processor", "rfd")
			break
		}

		existing := existingDeals[documentID]

		if existing == nil {
			liveDealsGroup := liveScrapedDeals(dealsGroup)
			if len(liveDealsGroup) == 0 {
				slog.Info("Skipping new deal because all scraped RFD threads are gone", "processor", "rfd", "id", documentID)
				continue
			}

			baseDeal := &liveDealsGroup[0]
			if err := p.processNewDeal(ctx, baseDeal, liveDealsGroup, &newDeals, subs, tracker); err != nil {
				slog.Error("Failed to process new deal", "processor", "rfd", "title", baseDeal.Title, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("new deal error %s: %v", baseDeal.Title, err))
			}
		} else {
			if err := p.processExistingDeal(ctx, existing, dealsGroup, &updatedDeals, subs); err != nil {
				slog.Error("Failed to process existing deal", "processor", "rfd", "id", documentID, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("existing deal error %s: %v", documentID, err))
			}
		}
	}
	return newDeals, updatedDeals, errorMessages
}

func (p *DealProcessor) processNewDeal(ctx context.Context, dealToSave *models.DealInfo, scrapedDuplicates []models.DealInfo, newDeals *[]models.DealInfo, subs []models.Subscription, tracker *metrics.Tracker) error {
	dealToSave.LastUpdated = time.Now()

	// Merge any scraped duplicates' threads into this new deal
	for i := 1; i < len(scrapedDuplicates); i++ {
		if len(scrapedDuplicates[i].Threads) > 0 {
			p.mergeThread(dealToSave, scrapedDuplicates[i].Threads[0])
		}
	}
	p.sortThreads(dealToSave)

	// Initialize rank tracking
	dealToSave.HasBeenWarm = p.notifier.IsWarm(*dealToSave)
	dealToSave.HasBeenHot = p.notifier.IsHot(*dealToSave)

	// Filter subscriptions for this new deal
	var eligibleSubs []models.Subscription
	for _, sub := range subs {
		if p.isDealEligibleForSubscription(*dealToSave, sub) {
			eligibleSubs = append(eligibleSubs, sub)
		}
	}

	// Send to Discord to get ID
	msgIDs, err := p.notifier.Send(ctx, *dealToSave, eligibleSubs)
	if err != nil {
		return err
	}
	dealToSave.DiscordMessageIDs = msgIDs
	dealToSave.DiscordLastUpdatedTime = time.Now()
	tracker.TrackDiscordMessage()
	tracker.TrackDealFound()
	*newDeals = append(*newDeals, *dealToSave)
	return nil
}

func (p *DealProcessor) processExistingDeal(ctx context.Context, existing *models.DealInfo, scrapedDuplicates []models.DealInfo, updatedDeals *[]models.DealInfo, subs []models.Subscription) error {
	// Clean up any historical duplicate threads (same thread ID, different slugs)
	changed := deduplicateThreadsByKey(existing)

	removedDeadThreads := removeNotFoundThreads(existing, scrapedDuplicates)
	if removedDeadThreads {
		changed = true
	}

	liveDuplicates := liveScrapedDeals(scrapedDuplicates)
	scrapedBase := contentBaseForExistingDeal(existing, liveDuplicates)

	// Merge all threads from the scraped group into existing
	for _, scraped := range liveDuplicates {
		if len(scraped.Threads) > 0 {
			if p.mergeThread(existing, scraped.Threads[0]) {
				changed = true
			}
		}
	}

	if p.dealChanged(existing, &scrapedBase) {
		changed = true
		// Merge changes into existing
		existing.Title = scrapedBase.Title
		existing.PostURL = scrapedBase.PostURL
		existing.Retailer = scrapedBase.Retailer
		existing.Category = scrapedBase.Category
		existing.Price = scrapedBase.Price
		existing.OriginalPrice = scrapedBase.OriginalPrice
		existing.Savings = scrapedBase.Savings
		existing.ThreadImageURL = scrapedBase.ThreadImageURL
		existing.PublishedTimestamp = scrapedBase.PublishedTimestamp
		existing.ActualDealURL = scrapedBase.ActualDealURL
		existing.Description = scrapedBase.Description
		existing.Comments = scrapedBase.Comments
		existing.Summary = scrapedBase.Summary
		existing.SearchTokens = scrapedBase.SearchTokens

		// AI fields
		if scrapedBase.AIProcessed {
			existing.CleanTitle = scrapedBase.CleanTitle
			existing.AIProcessed = scrapedBase.AIProcessed
		}
	}

	if !changed {
		return nil
	}

	p.sortThreads(existing)
	if removedDeadThreads {
		syncPrimaryPostURL(existing)
	}

	// Update historical rank tracking using aggregated stats
	if !existing.HasBeenWarm && p.notifier.IsWarm(*existing) {
		existing.HasBeenWarm = true
	}
	if !existing.HasBeenHot && p.notifier.IsHot(*existing) {
		existing.HasBeenHot = true
	}

	existing.LastUpdated = time.Now()

	// Handle Discord multi-channel updates
	// 1. Send to newly added channels that don't have this deal yet, OR channels where the deal just reached their threshold
	if len(subs) > 0 {
		var missingSubs []models.Subscription
		if existing.DiscordMessageIDs == nil {
			existing.DiscordMessageIDs = make(map[string]string)
		}

		for _, sub := range subs {
			if _, ok := existing.DiscordMessageIDs[sub.ChannelID]; !ok {
				// The channel doesn't have the deal yet. Should it get it now?
				if p.isDealEligibleForSubscription(*existing, sub) {
					missingSubs = append(missingSubs, sub)
				}
			}
		}

		if len(missingSubs) > 0 {
			newMsgIDs, err := p.notifier.Send(ctx, *existing, missingSubs)
			if err == nil {
				for channelID, msgID := range newMsgIDs {
					existing.DiscordMessageIDs[channelID] = msgID
				}
				existing.DiscordLastUpdatedTime = time.Now()
			} else {
				slog.Warn("Failed to send missing discord notifications", "processor", "rfd", "id", existing.DocumentID, "error", err)
			}
		}
	}

	// 2. Update existing channels
	// Discord error 30046: "Maximum number of edits to messages older than 1 hour reached."
	// The exact threshold is undocumented, but one developer hit it after ~3,600 edits
	// over 10 hours on a single message (editing every 10 seconds).
	// At our edit frequency (~1 per minute per deal), 2 hours is well within safe limits.
	// See: https://github.com/discord/discord-api-docs/issues/4413
	if len(existing.DiscordMessageIDs) > 0 && time.Since(existing.DiscordLastUpdatedTime) >= p.updateInterval && time.Since(existing.PublishedTimestamp) < 2*time.Hour {
		if err := p.notifier.Update(ctx, *existing); err == nil {
			existing.DiscordLastUpdatedTime = time.Now()
		} else {
			slog.Warn("Failed to update discord notifications", "processor", "rfd", "id", existing.DocumentID, "error", err)
		}
	}

	*updatedDeals = append(*updatedDeals, *existing)
	return nil
}

func liveScrapedDeals(scrapedDeals []models.DealInfo) []models.DealInfo {
	liveDeals := make([]models.DealInfo, 0, len(scrapedDeals))
	for _, deal := range scrapedDeals {
		if len(deal.Threads) == 0 {
			liveDeals = append(liveDeals, deal)
			continue
		}

		filtered := deal
		filtered.Threads = nil
		for _, thread := range deal.Threads {
			if !thread.NotFound {
				filtered.Threads = append(filtered.Threads, thread)
			}
		}
		if len(filtered.Threads) == 0 {
			continue
		}
		if removedPrimaryThread(deal, filtered) {
			filtered.PostURL = filtered.Threads[0].PostURL
		}
		liveDeals = append(liveDeals, filtered)
	}
	return liveDeals
}

func removedPrimaryThread(original, filtered models.DealInfo) bool {
	if len(original.Threads) == 0 || len(filtered.Threads) == 0 {
		return false
	}
	originalPrimaryKey := threadKey(original.PrimaryPostURL())
	return originalPrimaryKey != "" && originalPrimaryKey != threadKey(filtered.Threads[0].PostURL)
}

func removeNotFoundThreads(existing *models.DealInfo, scrapedDeals []models.DealInfo) bool {
	notFoundKeys := make(map[string]struct{})
	for _, deal := range scrapedDeals {
		for _, thread := range deal.Threads {
			if !thread.NotFound {
				continue
			}
			if key := threadKey(thread.PostURL); key != "" {
				notFoundKeys[key] = struct{}{}
			}
		}
	}
	if len(notFoundKeys) == 0 {
		return false
	}

	filtered := existing.Threads[:0]
	changed := false
	for _, thread := range existing.Threads {
		if _, ok := notFoundKeys[threadKey(thread.PostURL)]; ok {
			changed = true
			continue
		}
		filtered = append(filtered, thread)
	}
	if changed {
		existing.Threads = filtered
	}
	return changed
}

func syncPrimaryPostURL(deal *models.DealInfo) {
	if len(deal.Threads) == 0 {
		deal.PostURL = ""
		return
	}
	deal.PostURL = deal.Threads[0].PostURL
}

func contentBaseForExistingDeal(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) models.DealInfo {
	if len(scrapedDuplicates) == 0 {
		return *existing
	}

	if sameThread := scrapeForExistingThread(existing, scrapedDuplicates); sameThread != nil {
		base := *sameThread
		preserveExistingDetails(&base, existing)
		return base
	}
	if sameDocument := scrapeForExistingDocument(existing, scrapedDuplicates); sameDocument != nil {
		base := *sameDocument
		preserveExistingDetails(&base, existing)
		return base
	}

	base := *existing
	for _, scraped := range scrapedDuplicates {
		fillMissingDetails(&base, scraped)
	}
	return base
}

func scrapeForExistingThread(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) *models.DealInfo {
	existingThreadKeys := make(map[string]struct{}, len(existing.Threads))
	for _, thread := range existing.Threads {
		if key := threadKey(thread.PostURL); key != "" {
			existingThreadKeys[key] = struct{}{}
		}
	}

	for i := range scrapedDuplicates {
		for _, thread := range scrapedDuplicates[i].Threads {
			if _, ok := existingThreadKeys[threadKey(thread.PostURL)]; ok {
				return &scrapedDuplicates[i]
			}
		}
	}
	return nil
}

func scrapeForExistingDocument(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) *models.DealInfo {
	for i := range scrapedDuplicates {
		if scrapedDuplicates[i].DocumentID != existing.DocumentID {
			continue
		}
		if scrapeWasRemappedFromAnotherDocument(scrapedDuplicates[i], existing.DocumentID) {
			continue
		}
		return &scrapedDuplicates[i]
	}
	return nil
}

func scrapeWasRemappedFromAnotherDocument(scraped models.DealInfo, documentID string) bool {
	for _, thread := range scraped.Threads {
		if thread.DocumentID != "" && thread.DocumentID != documentID {
			return true
		}
	}
	return false
}

func preserveExistingDetails(scraped *models.DealInfo, existing *models.DealInfo) {
	if scraped.PostURL == "" {
		scraped.PostURL = existing.PostURL
	}
	if scraped.Retailer == "" {
		scraped.Retailer = existing.Retailer
	}
	if scraped.Category == "" {
		scraped.Category = existing.Category
	}
	if scraped.Price == "" {
		scraped.Price = existing.Price
	}
	if scraped.OriginalPrice == "" {
		scraped.OriginalPrice = existing.OriginalPrice
	}
	if scraped.Savings == "" {
		scraped.Savings = existing.Savings
	}
	if existing.ActualDealURL != "" && (scraped.ActualDealURL == "" || sameCanonicalDealURL(existing.ActualDealURL, scraped.ActualDealURL)) {
		scraped.ActualDealURL = existing.ActualDealURL
	}
	if scraped.ThreadImageURL == "" {
		scraped.ThreadImageURL = existing.ThreadImageURL
	}
	if scraped.Description == "" {
		scraped.Description = existing.Description
	}
	if scraped.Comments == "" {
		scraped.Comments = existing.Comments
	}
	if scraped.Summary == "" {
		scraped.Summary = existing.Summary
	}
	if len(scraped.SearchTokens) == 0 {
		scraped.SearchTokens = existing.SearchTokens
	}
}

func fillMissingDetails(base *models.DealInfo, candidate models.DealInfo) {
	if base.PostURL == "" {
		base.PostURL = candidate.PostURL
	}
	if base.Retailer == "" {
		base.Retailer = candidate.Retailer
	}
	if base.Category == "" {
		base.Category = candidate.Category
	}
	if base.Price == "" {
		base.Price = candidate.Price
	}
	if base.OriginalPrice == "" {
		base.OriginalPrice = candidate.OriginalPrice
	}
	if base.Savings == "" {
		base.Savings = candidate.Savings
	}
	if base.ActualDealURL == "" {
		base.ActualDealURL = candidate.ActualDealURL
	}
	if base.ThreadImageURL == "" {
		base.ThreadImageURL = candidate.ThreadImageURL
	}
	if base.Description == "" {
		base.Description = candidate.Description
	}
	if base.Comments == "" {
		base.Comments = candidate.Comments
	}
	if base.Summary == "" {
		base.Summary = candidate.Summary
	}
	if len(base.SearchTokens) == 0 {
		base.SearchTokens = candidate.SearchTokens
	}
}

func (p *DealProcessor) dealChanged(existing *models.DealInfo, scraped *models.DealInfo) bool {
	// Stats changes are handled by mergeThread now, so we only check content fields.
	actualURLChanged := existing.ActualDealURL != scraped.ActualDealURL
	if actualURLChanged && sameCanonicalDealURL(existing.ActualDealURL, scraped.ActualDealURL) {
		actualURLChanged = false
	}
	return existing.Title != scraped.Title ||
		existing.PostURL != scraped.PostURL ||
		existing.Retailer != scraped.Retailer ||
		existing.Category != scraped.Category ||
		existing.Price != scraped.Price ||
		existing.OriginalPrice != scraped.OriginalPrice ||
		existing.Savings != scraped.Savings ||
		existing.ThreadImageURL != scraped.ThreadImageURL ||
		actualURLChanged
}

// mergeThread updates the stats for an existing thread or appends a new one.
// Returns true if anything actually changed (stats or URL).
func (p *DealProcessor) mergeThread(deal *models.DealInfo, newThread models.ThreadContext) bool {
	if newThread.NotFound {
		return false
	}

	newKey := threadKey(newThread.PostURL)
	for i := range deal.Threads {
		if threadKey(deal.Threads[i].PostURL) == newKey {
			viewChanged := false
			if newThread.ViewCountAvailable {
				viewChanged = deal.Threads[i].ViewCount != newThread.ViewCount ||
					!deal.Threads[i].ViewCountAvailable
			} else {
				viewChanged = deal.Threads[i].ViewCount != 0 ||
					deal.Threads[i].ViewCountAvailable
			}

			changed := deal.Threads[i].LikeCount != newThread.LikeCount ||
				deal.Threads[i].CommentCount != newThread.CommentCount ||
				viewChanged ||
				deal.Threads[i].PostURL != newThread.PostURL

			deal.Threads[i].LikeCount = newThread.LikeCount
			deal.Threads[i].CommentCount = newThread.CommentCount
			if newThread.ViewCountAvailable {
				deal.Threads[i].ViewCount = newThread.ViewCount
				deal.Threads[i].ViewCountAvailable = true
			} else {
				deal.Threads[i].ViewCount = 0
				deal.Threads[i].ViewCountAvailable = false
			}
			deal.Threads[i].PostURL = newThread.PostURL // keep latest URL slug
			return changed
		}
	}
	// New thread duplicate found
	deal.Threads = append(deal.Threads, newThread)
	return true
}

// deduplicateThreadsByKey collapses threads that share the same threadKey,
// keeping the entry with the highest LikeCount. This cleans up historical data
// where slug-variant duplicates were stored before threadKey used the thread ID.
func deduplicateThreadsByKey(deal *models.DealInfo) bool {
	seen := make(map[string]int) // key -> index in deduped slice
	var deduped []models.ThreadContext
	changed := false
	for _, t := range deal.Threads {
		key := threadKey(t.PostURL)
		if idx, exists := seen[key]; exists {
			changed = true
			// Keep the one with higher likes
			if t.LikeCount > deduped[idx].LikeCount {
				deduped[idx] = t
			}
		} else {
			seen[key] = len(deduped)
			deduped = append(deduped, t)
		}
	}
	if changed {
		deal.Threads = deduped
	}
	return changed
}

// threadKey normalizes a PostURL for deduplication.
// For RFD URLs it extracts the numeric thread ID (e.g. "rfd:2806520") so that
// slug variations of the same thread (caused by title edits) collapse to one key.
// Non-RFD URLs fall back to the full URL stripped of fragments and trailing slashes.
func threadKey(rawURL string) string {
	// Strip fragment
	if idx := strings.Index(rawURL, "#"); idx != -1 {
		rawURL = rawURL[:idx]
	}
	rawURL = strings.TrimRight(rawURL, "/")

	// For RFD URLs, extract the numeric thread ID as the canonical key.
	// RFD thread URLs end with -{numeric_id}, e.g. /firehouse-subs-deal-2806520
	if parsed, err := url.Parse(rawURL); err == nil && strings.Contains(strings.ToLower(parsed.Hostname()), "redflagdeals.com") {
		path := strings.TrimRight(parsed.Path, "/")
		lastSlash := strings.LastIndex(path, "/")
		if lastSlash >= 0 {
			slug := path[lastSlash+1:]
			lastHyphen := strings.LastIndex(slug, "-")
			if lastHyphen >= 0 && lastHyphen < len(slug)-1 {
				candidate := slug[lastHyphen+1:]
				if isAllDigits(candidate) {
					return "rfd:" + candidate
				}
			}
		}
	}

	return rawURL
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// sortThreads sorts a deal's threads array descending by LikeCount, then by CommentCount
func (p *DealProcessor) sortThreads(deal *models.DealInfo) {
	sort.Slice(deal.Threads, func(i, j int) bool {
		if deal.Threads[i].LikeCount != deal.Threads[j].LikeCount {
			return deal.Threads[i].LikeCount > deal.Threads[j].LikeCount
		}
		return deal.Threads[i].CommentCount > deal.Threads[j].CommentCount
	})
}

func (p *DealProcessor) isDealEligibleForSubscription(deal models.DealInfo, sub models.Subscription) bool {
	isTech := deal.Category != "" && util.IsTechCategory(deal.Category)
	isWarm := deal.HasBeenWarm || p.notifier.IsWarm(deal)
	isHot := deal.HasBeenHot || p.notifier.IsHot(deal)
	return dealtypes.RFDEligible(sub.DealType, isTech, isWarm, isHot)
}
