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
	AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, error)
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

	// 1. Scrape and Validate
	validDeals, err := p.scrapeAndValidate(ctx, logger)
	if err != nil {
		return err
	}

	// 2. Load Existing Deals
	existingDeals, err := p.loadExistingDeals(ctx, validDeals, logger)
	if err != nil {
		return err
	}

	// 3. Fetch Details for New/Changed Deals
	p.enrichDealsWithDetails(ctx, validDeals, existingDeals, logger)

	// 3a. AI Analysis for New Deals
	p.analyzeDeals(ctx, validDeals, existingDeals, logger)

	// 4. Notify Discord and Prepare Updates
	newDeals, updatedDeals, errorMessages := p.processNotificationsAndPrepareUpdates(ctx, validDeals, existingDeals)

	// 5. Batch Save
	if len(newDeals) > 0 || len(updatedDeals) > 0 {
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
		needsDetails := existing.ActualDealURL == "" ||
			existing.Description == "" ||
			existing.PostURL != deal.PostURL ||
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
		p.scraper.FetchDealDetails(ctx, dealsToDetail)
	} else {
		logger.Info("No deals needed detail scraping")
	}
}

// analyzeDeals runs AI analysis on deals that haven't been processed yet.
func (p *DealProcessor) analyzeDeals(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger) {
	for i := range validDeals {
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
			cleanedTitle, isHot, err := p.aiClient.AnalyzeDeal(ctx, deal)
			if err != nil {
				// Log error but continue. Deal stays "unprocessed" effectively, or we mark it processed with failure?
				// For now, just log. Next run will try again if we don't save AIProcessed=true.
				// However, to avoid infinite loops on bad deals, we could mark as processed?
				// Let's NOT mark as processed so it retries, but maybe we need a retry count later.
				logger.Warn("AI analysis failed", "title", deal.Title, "error", err)

				// Fallback: use original title if we must, but here we just leave CleanTitle empty.
				// The notifier will handle empty CleanTitle by using Title.
			} else {
				deal.CleanTitle = cleanedTitle
				deal.IsLavaHot = isHot
				deal.AIProcessed = true
				logger.Info("AI analysis complete", "original", deal.Title, "clean", cleanedTitle, "hot", isHot)
			}
		} else if existing != nil {
			// Carry over existing AI data
			deal.CleanTitle = existing.CleanTitle
			deal.IsLavaHot = existing.IsLavaHot
			deal.AIProcessed = existing.AIProcessed
		}
	}
}

// processNotificationsAndPrepareUpdates sends/updates Discord notifications and prepares lists for DB persistence.
func (p *DealProcessor) processNotificationsAndPrepareUpdates(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo) ([]models.DealInfo, []models.DealInfo, []string) {
	var newDeals []models.DealInfo
	var updatedDeals []models.DealInfo
	var errorMessages []string

	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.FirestoreID]

		if existing == nil {
			if err := p.processNewDeal(ctx, deal, &newDeals); err != nil {
				slog.Error("Failed to process new deal", "title", deal.Title, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("new deal error %s: %v", deal.Title, err))
			}
		} else {
			if err := p.processExistingDeal(ctx, existing, deal, &updatedDeals); err != nil {
				slog.Error("Failed to process existing deal", "title", deal.Title, "error", err)
				errorMessages = append(errorMessages, fmt.Sprintf("existing deal error %s: %v", deal.Title, err))
			}
		}
	}
	return newDeals, updatedDeals, errorMessages
}

func (p *DealProcessor) processNewDeal(ctx context.Context, deal *models.DealInfo, newDeals *[]models.DealInfo) error {
	dealToSave := deal
	dealToSave.LastUpdated = time.Now()

	// Send to Discord to get ID
	msgID, err := p.notifier.Send(ctx, *dealToSave)
	if err != nil {
		return err
	}
	dealToSave.DiscordMessageID = msgID
	dealToSave.DiscordLastUpdatedTime = time.Now()
	*newDeals = append(*newDeals, *dealToSave)
	return nil
}

func (p *DealProcessor) processExistingDeal(ctx context.Context, existing *models.DealInfo, scraped *models.DealInfo, updatedDeals *[]models.DealInfo) error {
	// Check if changed
	if !p.dealChanged(existing, scraped) {
		return nil
	}

	// Merge changes into existing
	existing.Title = scraped.Title
	existing.PostURL = scraped.PostURL
	existing.LikeCount = scraped.LikeCount
	existing.CommentCount = scraped.CommentCount
	existing.ViewCount = scraped.ViewCount
	existing.ThreadImageURL = scraped.ThreadImageURL
	existing.AuthorName = scraped.AuthorName
	existing.AuthorURL = scraped.AuthorURL
	existing.PublishedTimestamp = scraped.PublishedTimestamp
	existing.ActualDealURL = scraped.ActualDealURL
	existing.Description = scraped.Description
	existing.Comments = scraped.Comments
	existing.Summary = scraped.Summary

	// AI fields
	if scraped.AIProcessed {
		existing.CleanTitle = scraped.CleanTitle
		existing.IsLavaHot = scraped.IsLavaHot
		existing.AIProcessed = scraped.AIProcessed
	}

	existing.LastUpdated = time.Now()

	// Check if we need to update Discord
	if existing.DiscordMessageID == "" {
		// Should have had one. Send now.
		msgID, err := p.notifier.Send(ctx, *existing)
		if err == nil {
			existing.DiscordMessageID = msgID
			existing.DiscordLastUpdatedTime = time.Now()
		} else {
			slog.Warn("Failed to send missing discord notification", "id", existing.FirestoreID, "error", err)
		}
	} else if time.Since(existing.DiscordLastUpdatedTime) >= p.updateInterval {
		if err := p.notifier.Update(ctx, existing.DiscordMessageID, *existing); err == nil {
			existing.DiscordLastUpdatedTime = time.Now()
		} else {
			slog.Warn("Failed to update discord notification", "id", existing.FirestoreID, "error", err)
		}
	}

	*updatedDeals = append(*updatedDeals, *existing)
	return nil
}

func (p *DealProcessor) dealChanged(existing *models.DealInfo, scraped *models.DealInfo) bool {
	return existing.LikeCount != scraped.LikeCount ||
		existing.CommentCount != scraped.CommentCount ||
		existing.ViewCount != scraped.ViewCount ||
		existing.Title != scraped.Title ||
		existing.PostURL != scraped.PostURL ||
		existing.AuthorName != scraped.AuthorName ||
		existing.ThreadImageURL != scraped.ThreadImageURL ||
		existing.ActualDealURL != scraped.ActualDealURL
}
