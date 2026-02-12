package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

type Processor interface {
	ProcessDeals(ctx context.Context) error
}

type DealProcessor struct {
	store          DealStore
	notifier       DealNotifier
	scraper        scraper.Scraper
	config         *config.Config
	updateInterval time.Duration
}

func New(store DealStore, n DealNotifier, s scraper.Scraper, cfg *config.Config) *DealProcessor {
	interval, err := time.ParseDuration(cfg.DiscordUpdateInterval)
	if err != nil {
		slog.Warn("Invalid update interval, using default", "interval", cfg.DiscordUpdateInterval, "error", err, "default", "10m")
		interval = 10 * time.Minute
	}

	return &DealProcessor{
		store:          store,
		notifier:       n,
		scraper:        s,
		config:         cfg,
		updateInterval: interval,
	}
}

// generateDealID creates a stable deal identity based on PublishedTimestamp.
// This survives title and URL edits by the post author.
func generateDealID(published time.Time) string {
	hash := sha256.Sum256([]byte(published.Format(time.RFC3339Nano)))
	return hex.EncodeToString(hash[:])
}

func (p *DealProcessor) ProcessDeals(ctx context.Context) error {
	scrapedDeals, err := p.scraper.ScrapeDealList(ctx)
	if err != nil {
		return fmt.Errorf("failed to scrape hot deals list: %w", err)
	}
	slog.Info("Successfully scraped deal list", "count", len(scrapedDeals))

	// Filter deals that need detail scraping
	var dealsToDetail []*models.DealInfo

	// Cache existing deals so processSingleDeal doesn't re-read them
	existingDeals := make(map[string]*models.DealInfo)

	for i := range scrapedDeals {
		deal := &scrapedDeals[i]

		// Skip deals missing required fields
		if strings.TrimSpace(deal.Title) == "" || strings.TrimSpace(deal.PostURL) == "" || deal.PublishedTimestamp.IsZero() {
			continue
		}
		deal.FirestoreID = generateDealID(deal.PublishedTimestamp)

		existing, err := p.store.GetDealByID(ctx, deal.FirestoreID)
		if err != nil {
			slog.Warn("Failed to check deal existence, will scrape details", "id", deal.FirestoreID, "error", err)
			dealsToDetail = append(dealsToDetail, deal)
			continue
		}

		if existing == nil {
			// New deal — needs details
			dealsToDetail = append(dealsToDetail, deal)
			continue
		}

		// Cache the existing deal so processSingleDeal skips re-reading
		existingDeals[deal.FirestoreID] = existing

		// Check if any list-visible fields changed
		listChanged := existing.Title != deal.Title ||
			existing.PostURL != deal.PostURL ||
			existing.LikeCount != deal.LikeCount ||
			existing.CommentCount != deal.CommentCount ||
			existing.ViewCount != deal.ViewCount ||
			existing.AuthorName != deal.AuthorName

		if listChanged {
			// Re-scrape details when list data changed
			dealsToDetail = append(dealsToDetail, deal)
		} else {
			// Unchanged. Copy details from existing so processSingleDeal doesn't see a diff.
			deal.ActualDealURL = existing.ActualDealURL
			deal.ThreadImageURL = existing.ThreadImageURL
		}
	}

	if len(dealsToDetail) > 0 {
		slog.Info("Fetching details for deals", "count", len(dealsToDetail))
		p.scraper.FetchDealDetails(ctx, dealsToDetail)
	} else {
		slog.Info("No deals needed detail scraping")
	}

	var newCount, updatedCount int
	var errorMessages []string

	for _, deal := range scrapedDeals {
		existing := existingDeals[deal.FirestoreID] // may be nil for new deals
		isNew, isUpdated, err := p.processSingleDeal(ctx, deal, existing)
		if err != nil {
			errorMessages = append(errorMessages, err.Error())
			continue
		}
		if isNew {
			newCount++
		}
		if isUpdated {
			updatedCount++
		}
	}

	// Trim old deals once per processing run instead of per-deal
	if newCount > 0 {
		if err := p.store.TrimOldDeals(ctx, 500); err != nil {
			slog.Warn("Failed to trim old deals", "error", err)
		}
	}

	slog.Info("Finished processing", "new", newCount, "updated", updatedCount)
	if len(errorMessages) > 0 {
		return fmt.Errorf("processed with errors: %s", strings.Join(errorMessages, "; "))
	}
	return nil
}

func (p *DealProcessor) processSingleDeal(ctx context.Context, deal models.DealInfo, existing *models.DealInfo) (isNew, isUpdated bool, err error) {
	if strings.TrimSpace(deal.Title) == "" || strings.TrimSpace(deal.PostURL) == "" || deal.PublishedTimestamp.IsZero() {
		slog.Info("Skipping invalid deal", "title", deal.Title)
		return false, false, nil
	}

	// ID was already computed in ProcessDeals; ensure it's set
	if deal.FirestoreID == "" {
		deal.FirestoreID = generateDealID(deal.PublishedTimestamp)
	}
	deal.LastUpdated = time.Now()

	// New deal — try to create it
	if existing == nil {
		createErr := p.store.TryCreateDeal(ctx, deal)
		if createErr == nil {
			slog.Info("New deal added", "title", deal.Title)
			p.sendAndSaveDiscordID(ctx, &deal)
			return true, false, nil
		}

		// Race condition: another instance created it first
		if !errors.Is(createErr, storage.ErrDealExists) {
			return false, false, fmt.Errorf("failed to create deal %s: %v", deal.Title, createErr)
		}

		existing, err = p.store.GetDealByID(ctx, deal.FirestoreID)
		if err != nil {
			return false, false, fmt.Errorf("error recovering from race for deal %s: %v", deal.FirestoreID, err)
		}
		if existing == nil {
			slog.Warn("Race condition anomaly: deal claimed to exist but returned nil", "id", deal.FirestoreID)
			return false, false, nil
		}
	}

	// Ensure deal has a Discord message
	if existing.DiscordMessageID == "" {
		p.sendAndSaveDiscordID(ctx, existing)
	}

	// Check if scraped data differs from stored data
	if !p.dealChanged(existing, &deal) {
		return false, false, nil
	}

	// Apply updates (including PostURL which may have changed)
	existing.Title = deal.Title
	existing.PostURL = deal.PostURL
	existing.LikeCount = deal.LikeCount
	existing.CommentCount = deal.CommentCount
	existing.ViewCount = deal.ViewCount
	existing.ThreadImageURL = deal.ThreadImageURL
	existing.AuthorName = deal.AuthorName
	existing.AuthorURL = deal.AuthorURL
	existing.PostedTime = deal.PostedTime
	existing.PublishedTimestamp = deal.PublishedTimestamp
	existing.ActualDealURL = deal.ActualDealURL
	existing.LastUpdated = time.Now()

	// If a throttled Discord update will happen, set the timestamp before persisting
	// so we only need a single Firestore write.
	shouldUpdateDiscord := existing.DiscordMessageID != "" &&
		time.Since(existing.DiscordLastUpdatedTime) >= p.updateInterval
	if shouldUpdateDiscord {
		existing.DiscordLastUpdatedTime = time.Now()
	}

	if err := p.store.UpdateDeal(ctx, *existing); err != nil {
		return false, false, fmt.Errorf("failed to update deal %s: %w", existing.FirestoreID, err)
	}
	slog.Info("Updated deal", "title", existing.Title)

	// Send Discord update after persisting
	if shouldUpdateDiscord {
		if err := p.notifier.Update(ctx, existing.DiscordMessageID, *existing); err != nil {
			slog.Warn("Discord update failed", "id", existing.FirestoreID, "error", err)
		}
	}

	return false, true, nil
}

func (p *DealProcessor) sendAndSaveDiscordID(ctx context.Context, deal *models.DealInfo) {
	msgID, err := p.notifier.Send(ctx, *deal)
	if err != nil {
		slog.Error("Error sending to Discord", "error", err)
		return
	}
	deal.DiscordMessageID = msgID
	deal.DiscordLastUpdatedTime = time.Now()
	if err := p.store.UpdateDeal(ctx, *deal); err != nil {
		slog.Warn("Failed to save Discord message ID", "id", deal.FirestoreID, "error", err)
	}
}

func (p *DealProcessor) dealChanged(existing *models.DealInfo, scraped *models.DealInfo) bool {
	return existing.LikeCount != scraped.LikeCount ||
		existing.CommentCount != scraped.CommentCount ||
		existing.ViewCount != scraped.ViewCount ||
		existing.Title != scraped.Title ||
		existing.PostURL != scraped.PostURL ||
		existing.ThreadImageURL != scraped.ThreadImageURL ||
		existing.ActualDealURL != scraped.ActualDealURL
}
