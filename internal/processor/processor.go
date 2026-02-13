package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
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
	updateInterval time.Duration
	mu             sync.Mutex // prevents overlapping ProcessDeals runs
}

func New(store DealStore, n DealNotifier, s DealScraper, v DealValidator, cfg *config.Config) *DealProcessor {
	return &DealProcessor{
		store:          store,
		notifier:       n,
		scraper:        s,
		validator:      v,
		config:         cfg,
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

	scrapedDeals, err := p.scraper.ScrapeDealList(ctx)
	if err != nil {
		return fmt.Errorf("failed to scrape hot deals list: %w", err)
	}
	logger.Info("Successfully scraped deal list", "count", len(scrapedDeals))

	// Assign IDs and filter/validate deals upfront
	var validDeals []models.DealInfo
	var idsToLookup []string

	for i := range scrapedDeals {
		deal := &scrapedDeals[i]

		// Validate using the validator
		if err := p.validator.ValidateStruct(deal); err != nil {
			p.handleDeadLetter(deal, err)
			continue
		}

		deal.FirestoreID = generateDealID(deal.PublishedTimestamp)
		validDeals = append(validDeals, *deal)
		idsToLookup = append(idsToLookup, deal.FirestoreID)
	}

	// Batch read all existing deals in one Firestore call
	existingDeals, err := p.store.GetDealsByIDs(ctx, idsToLookup)
	if err != nil {
		logger.Warn("Batch read failed, falling back to individual reads", "error", err)
		existingDeals = make(map[string]*models.DealInfo)
	}

	// Determine which deals need detail scraping
	var dealsToDetail []*models.DealInfo
	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.FirestoreID]

		if existing == nil {
			// New deal — needs details
			dealsToDetail = append(dealsToDetail, deal)
			continue
		}

		// Check if any tracked fields changed
		if p.dealChanged(existing, deal) {
			dealsToDetail = append(dealsToDetail, deal)
		} else {
			// Unchanged — copy details from existing so processSingleDeal doesn't see a diff
			deal.ActualDealURL = existing.ActualDealURL
			deal.ThreadImageURL = existing.ThreadImageURL
		}
	}

	if len(dealsToDetail) > 0 {
		logger.Info("Fetching details for deals", "count", len(dealsToDetail))
		p.scraper.FetchDealDetails(ctx, dealsToDetail)
	} else {
		logger.Info("No deals needed detail scraping")
	}

	var newDeals []models.DealInfo
	var updatedDeals []models.DealInfo
	var errorMessages []string

	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.FirestoreID]

		// Ensure deal has a Discord message
		// If existing is nil, we treat it as new.
		// If existing is not nil but missing DiscordMessageID, we treat it as update-needing-discord???
		// Wait, if it's new, we need to get Discord ID first.

		isNew := existing == nil
		var dealToSave *models.DealInfo

		if isNew {
			// It's a new deal.
			dealToSave = deal
			dealToSave.LastUpdated = time.Now()

			// Send to Discord to get ID
			msgID, err := p.notifier.Send(ctx, *dealToSave)
			if err != nil {
				slog.Error("Failed to send new deal notification", "title", deal.Title, "error", err)
				// Continue without Discord ID? Or skip saving?
				// If we skip saving, next run will retry. Ideally we skip saving.
				errorMessages = append(errorMessages, fmt.Sprintf("discord send failed for %s: %v", deal.Title, err))
				continue
			}
			dealToSave.DiscordMessageID = msgID
			dealToSave.DiscordLastUpdatedTime = time.Now()
			newDeals = append(newDeals, *dealToSave)

		} else {
			// Existing deal
			// Check if changed
			if !p.dealChanged(existing, deal) {
				continue
			}

			// Merge changes into existing
			existing.Title = deal.Title
			existing.PostURL = deal.PostURL
			existing.LikeCount = deal.LikeCount
			existing.CommentCount = deal.CommentCount
			existing.ViewCount = deal.ViewCount
			existing.ThreadImageURL = deal.ThreadImageURL
			existing.AuthorName = deal.AuthorName
			existing.AuthorURL = deal.AuthorURL
			existing.PublishedTimestamp = deal.PublishedTimestamp
			existing.ActualDealURL = deal.ActualDealURL
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

			updatedDeals = append(updatedDeals, *existing)
		}
	}

	if len(newDeals) > 0 || len(updatedDeals) > 0 {
		if err := p.store.BatchWrite(ctx, newDeals, updatedDeals); err != nil {
			return fmt.Errorf("batch write failed: %w", err)
		}
		logger.Info("Batch write completed", "created", len(newDeals), "updated", len(updatedDeals))
	}

	// Trim old deals
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

func (p *DealProcessor) handleDeadLetter(deal *models.DealInfo, err error) {
	slog.Warn("Validation failed, sending to dead letter file", "error", err, "title", deal.Title)

	f, fileErr := os.OpenFile("dead_letter.jsonl", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if fileErr != nil {
		slog.Error("Failed to open dead letter file", "error", fileErr)
		return
	}
	defer f.Close()

	type deadLetterEntry struct {
		Time  time.Time        `json:"time"`
		Error string           `json:"error"`
		Deal  *models.DealInfo `json:"deal"`
	}

	entry := deadLetterEntry{
		Time:  time.Now(),
		Error: err.Error(),
		Deal:  deal,
	}

	if jsonErr := json.NewEncoder(f).Encode(entry); jsonErr != nil {
		slog.Error("Failed to write to dead letter file", "error", jsonErr)
	}
}
