package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
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
}

type DealAnalyzer interface {
	AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, bool, error)
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
		slog.Info("ProcessDeals: already in progress, skipping")
		return nil
	}
	defer p.mu.Unlock()

	runID := time.Now().Format("20060102-150405")
	logger := slog.With("runID", runID)

	// Fetch Recent Deals for deduplication
	recentDeals, err := p.store.GetRecentDeals(ctx, 48*time.Hour)
	if err != nil {
		logger.Warn("Failed to get recent deals for deduplication", "error", err)
	}

	// 1. Scrape and Validate
	scrapedDeals, err := p.scrapeAndValidate(ctx, logger)
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

	// 3. Fetch Details for New/Changed Deals
	p.enrichDealsWithDetails(ctx, validDeals, existingDeals, logger)

	// 3a. AI Analysis for New Deals
	p.analyzeDeals(ctx, validDeals, existingDeals, logger)

	// 4. Fetch Subscriptions
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		logger.Error("Failed to get subscriptions, skipping notifications", "error", err)
	}

	// 5. Notify Discord and Prepare Updates
	newDeals, updatedDeals, errorMessages := p.processNotificationsAndPrepareUpdates(ctx, validDeals, existingDeals, subs)

	// 6. Batch Save
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
		// 11. Consolidated batch write
		if err := p.store.BatchWrite(ctx, newDeals, updatedDeals); err != nil {
			return fmt.Errorf("batch write failed: %w", err)
		}
		logger.Info("Batch write completed", "created", len(newDeals), "updated", len(updatedDeals))
	}

	// 6. Cleanup Old Deals
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
func (p *DealProcessor) scrapeAndValidate(ctx context.Context, logger *slog.Logger) ([]models.DealInfo, error) {
	scrapedDeals, err := p.scraper.ScrapeDealList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}
	logger.Info("Successfully scraped deal list", "count", len(scrapedDeals))

	var validDeals []models.DealInfo
	for i := range scrapedDeals {
		deal := &scrapedDeals[i]

		// Validate using the validator
		if err := p.validator.ValidateStruct(deal); err != nil {
			slog.Error("Validation failed for deal", "title", deal.Title, "error", err)
			continue
		}

		deal.FirestoreID = generateDealID(deal.PublishedTimestamp)
		if len(deal.Threads) > 0 {
			deal.Threads[0].FirestoreID = deal.FirestoreID
		}

		validDeals = append(validDeals, *deal)
	}
	return validDeals, nil
}

// loadExistingDeals fetches existing deals from Firestore corresponding to the valid scraped deals.
func (p *DealProcessor) loadExistingDeals(ctx context.Context, validDeals []models.DealInfo, logger *slog.Logger) (map[string]*models.DealInfo, error) {
	var idsToLookup []string
	for _, deal := range validDeals {
		idsToLookup = append(idsToLookup, deal.FirestoreID)
	}

	existingDeals, err := p.store.GetDealsByIDs(ctx, idsToLookup)
	if err != nil {
		logger.Error("Batch read failed", "error", err)
		return nil, fmt.Errorf("failed to load existing deals: %w", err)
	}
	return existingDeals, nil
}

// enrichDealsWithDetails determines which deals need detail scraping (new or changed) and fetches them.
func (p *DealProcessor) enrichDealsWithDetails(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger) {
	var dealsToDetail []*models.DealInfo
	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.FirestoreID]

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
		needsDetails := existing.ActualDealURL == "" ||
			(existing.Description == "" && !existing.AIProcessed) ||
			existing.PostURL != deal.PostURL ||
			existing.Title != deal.Title

		// Re-fetch details if engagement has grown significantly (for AI re-analysis)
		if !needsDetails && existing.AIProcessed {
			likes, comments, _ := deal.Stats()
			existingLikes, existingComments, _ := existing.Stats()
			if likes >= existingLikes+10 || comments >= existingComments+10 {
				needsDetails = true
				logger.Info("Re-fetching details for engagement-based re-analysis",
					"title", deal.Title,
					"likes", likes, "prev_likes", existingLikes,
					"comments", comments, "prev_comments", existingComments)
			}
		}

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
		p.scraper.FetchDealDetails(ctx, dealsToDetail)
	} else {
		logger.Info("No deals needed detail scraping")
	}
}

// analyzeDeals runs AI analysis on deals that haven't been processed yet.
func (p *DealProcessor) analyzeDeals(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger) {
	for i := range validDeals {
		if ctx.Err() != nil {
			logger.Warn("Context cancelled, stopping AI analysis", "remaining", len(validDeals)-i)
			break
		}

		deal := &validDeals[i]
		existing := existingDeals[deal.FirestoreID]
		isNew := existing == nil

		// We analyze if:
		// 1. It's a new deal.
		// 2. Or existing deal hasn't been processed.
		// 3. Or significant fields changed (Title/URL) which invalidates previous AI analysis.
		shouldAnalyze := isNew || !existing.AIProcessed

		if !shouldAnalyze && existing != nil {
			if deal.Title != existing.Title || deal.PostURL != existing.PostURL {
				shouldAnalyze = true
				logger.Info("Re-analyzing deal due to content change", "title", deal.Title)
			}
		}

		// Re-analyze if engagement has grown significantly since last analysis
		if !shouldAnalyze && existing != nil && existing.AIProcessed {
			likes, comments, _ := deal.Stats()
			existingLikes, existingComments, _ := existing.Stats()
			if likes >= existingLikes+10 || comments >= existingComments+10 {
				shouldAnalyze = true
				logger.Info("Re-analyzing deal due to engagement growth",
					"title", deal.Title,
					"likes", likes, "prev_likes", existingLikes,
					"comments", comments, "prev_comments", existingComments)
			}
		}

		if shouldAnalyze {
			// Double check if we already have a clean title from somewhere else (unlikely with current flow)
			// But if we are re-analyzing, we ignore the existing clean title.
			if !isNew && deal.CleanTitle != "" && deal.AIProcessed {
				// This case shouldn't really happen with current flow unless we set it manually before here
				continue
			}

			// Call AI
			// Note: This is done sequentially here. For high volume, we might want concurrency,
			// but for a few deals every 10 mins, sequential is fine and safer for rate limits.
			cleanedTitle, isWarm, isHot, err := p.aiClient.AnalyzeDeal(ctx, deal)

			if err != nil {
				// Log error but continue. Deal stays "unprocessed" effectively.
				logger.Warn("AI analysis failed", "deal_id", deal.FirestoreID, "title", deal.Title, "error", err)
			} else {
				deal.CleanTitle = cleanedTitle
				deal.IsWarm = isWarm
				deal.IsLavaHot = isHot
				deal.AIProcessed = true
			}
		} else if existing != nil {
			// Carry over existing AI data
			deal.CleanTitle = existing.CleanTitle
			deal.IsWarm = existing.IsWarm
			deal.IsLavaHot = existing.IsLavaHot
			deal.AIProcessed = existing.AIProcessed
		}
	}
}

// processNotificationsAndPrepareUpdates sends/updates Discord notifications and prepares lists for DB persistence.
func (p *DealProcessor) processNotificationsAndPrepareUpdates(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, subs []models.Subscription) ([]models.DealInfo, []models.DealInfo, []string) {
	var newDeals []models.DealInfo
	var updatedDeals []models.DealInfo
	var errorMessages []string

	// We need to group validDeals by FirestoreID because deduplication might map multiple
	// scraped deals to the same ID.
	groupedDeals := make(map[string][]models.DealInfo)
	for _, deal := range validDeals {
		groupedDeals[deal.FirestoreID] = append(groupedDeals[deal.FirestoreID], deal)
	}

	for firestoreID, dealsGroup := range groupedDeals {
		if ctx.Err() != nil {
			slog.Warn("Context cancelled, stopping notification processing")
			break
		}

		// Use the first deal in the group as the base
		baseDeal := &dealsGroup[0]
		existing := existingDeals[firestoreID]

		if existing == nil {
			if err := p.processNewDeal(ctx, baseDeal, dealsGroup, &newDeals, subs); err != nil {
				slog.Error("Failed to process new deal", "title", baseDeal.Title, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("new deal error %s: %v", baseDeal.Title, err))
			}
		} else {
			if err := p.processExistingDeal(ctx, existing, dealsGroup, &updatedDeals, subs); err != nil {
				slog.Error("Failed to process existing deal", "title", baseDeal.Title, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("existing deal error %s: %v", baseDeal.Title, err))
			}
		}
	}
	return newDeals, updatedDeals, errorMessages
}

func (p *DealProcessor) processNewDeal(ctx context.Context, dealToSave *models.DealInfo, scrapedDuplicates []models.DealInfo, newDeals *[]models.DealInfo, subs []models.Subscription) error {
	dealToSave.LastUpdated = time.Now()

	// Merge any scraped duplicates' threads into this new deal
	for i := 1; i < len(scrapedDuplicates); i++ {
		p.mergeThread(dealToSave, scrapedDuplicates[i].Threads[0])
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
	*newDeals = append(*newDeals, *dealToSave)
	return nil
}

func (p *DealProcessor) processExistingDeal(ctx context.Context, existing *models.DealInfo, scrapedDuplicates []models.DealInfo, updatedDeals *[]models.DealInfo, subs []models.Subscription) error {
	// Clean up any historical duplicate threads (same thread ID, different slugs)
	changed := deduplicateThreadsByKey(existing)

	// Merge all threads from the scraped group into existing
	for _, scraped := range scrapedDuplicates {
		if p.mergeThread(existing, scraped.Threads[0]) {
			changed = true
		}
	}

	// Check if main content changed (use [0] deal since titles/urls are same for dupes within batch)
	scrapedBase := &scrapedDuplicates[0]
	if p.dealChanged(existing, scrapedBase) {
		changed = true
		// Merge changes into existing
		existing.Title = scrapedBase.Title
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
			existing.IsWarm = scrapedBase.IsWarm
			existing.IsLavaHot = scrapedBase.IsLavaHot
			existing.AIProcessed = scrapedBase.AIProcessed
		}
	}

	if !changed {
		return nil
	}

	p.sortThreads(existing)

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
				slog.Warn("Failed to send missing discord notifications", "id", existing.FirestoreID, "error", err)
			}
		}
	}

	// 2. Update existing channels
	// To avoid Discord rate limits ("Maximum number of edits to messages older than 1 hour reached"),
	// stop updating Discord messages for deals published more than an hour ago.
	if len(existing.DiscordMessageIDs) > 0 && time.Since(existing.DiscordLastUpdatedTime) >= p.updateInterval && time.Since(existing.PublishedTimestamp) < time.Hour {
		if err := p.notifier.Update(ctx, *existing); err == nil {
			existing.DiscordLastUpdatedTime = time.Now()
		} else {
			slog.Warn("Failed to update discord notifications", "id", existing.FirestoreID, "error", err)
		}
	}

	*updatedDeals = append(*updatedDeals, *existing)
	return nil
}

func (p *DealProcessor) dealChanged(existing *models.DealInfo, scraped *models.DealInfo) bool {
	// Stats changes are handled by mergeThread now, so we only check content fields.
	return existing.Title != scraped.Title ||
		existing.ThreadImageURL != scraped.ThreadImageURL ||
		existing.ActualDealURL != scraped.ActualDealURL
}

// mergeThread updates the stats for an existing thread or appends a new one.
// Returns true if anything actually changed (stats or URL).
func (p *DealProcessor) mergeThread(deal *models.DealInfo, newThread models.ThreadContext) bool {
	newKey := threadKey(newThread.PostURL)
	for i := range deal.Threads {
		if threadKey(deal.Threads[i].PostURL) == newKey {
			changed := deal.Threads[i].LikeCount != newThread.LikeCount ||
				deal.Threads[i].CommentCount != newThread.CommentCount ||
				deal.Threads[i].ViewCount != newThread.ViewCount ||
				deal.Threads[i].PostURL != newThread.PostURL

			deal.Threads[i].LikeCount = newThread.LikeCount
			deal.Threads[i].CommentCount = newThread.CommentCount
			deal.Threads[i].ViewCount = newThread.ViewCount
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
	if strings.Contains(rawURL, "redflagdeals.com/") {
		lastSlash := strings.LastIndex(rawURL, "/")
		if lastSlash >= 0 {
			slug := rawURL[lastSlash+1:]
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
	for i := 0; i < len(deal.Threads)-1; i++ {
		for j := i + 1; j < len(deal.Threads); j++ {
			ti := deal.Threads[i]
			tj := deal.Threads[j]
			if tj.LikeCount > ti.LikeCount || (tj.LikeCount == ti.LikeCount && tj.CommentCount > ti.CommentCount) {
				deal.Threads[i], deal.Threads[j] = deal.Threads[j], deal.Threads[i]
			}
		}
	}
}

func (p *DealProcessor) isDealEligibleForSubscription(deal models.DealInfo, sub models.Subscription) bool {
	isTech := deal.Category != "" && util.IsTechCategory(deal.Category)
	isWarm := deal.HasBeenWarm || p.notifier.IsWarm(deal)
	isHot := deal.HasBeenHot || p.notifier.IsHot(deal)

	switch sub.DealType {
	// RFD: all deals
	case "rfd_all":
		return true
	// RFD: tech only
	case "rfd_tech":
		return isTech
	// RFD: warm + hot (all categories)
	case "rfd_warm_hot":
		return isWarm || isHot
	// RFD: warm + hot tech only
	case "rfd_warm_hot_tech":
		return (isWarm || isHot) && isTech
	// RFD: hot only
	case "rfd_hot":
		return isHot
	// RFD: hot tech only
	case "rfd_hot_tech":
		return isHot && isTech

	// Cross-source: RFD deals are eligible
	case "warm_hot_all":
		return isWarm || isHot
	case "hot_all":
		return isHot

	// eBay-only: RFD deals are NOT eligible
	case "ebay_warm_hot", "ebay_hot":
		return false
	}
	return false
}
