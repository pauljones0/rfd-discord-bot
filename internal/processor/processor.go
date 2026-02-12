package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

type Processor interface {
	ProcessDeals(ctx context.Context) error
}

type DealProcessor struct {
	store          *storage.Client
	notifier       *notifier.Client
	scraper        scraper.Scraper
	config         *config.Config
	updateInterval time.Duration
}

func New(store *storage.Client, n *notifier.Client, s scraper.Scraper, cfg *config.Config) *DealProcessor {
	interval, err := time.ParseDuration(cfg.DiscordUpdateInterval)
	if err != nil {
		log.Printf("Warning: Invalid update interval '%s', using 10m: %v", cfg.DiscordUpdateInterval, err)
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

func (p *DealProcessor) ProcessDeals(ctx context.Context) error {
	scrapedDeals, err := p.scraper.ScrapeHotDealsPage(ctx)
	if err != nil {
		return fmt.Errorf("failed to scrape hot deals page: %w", err)
	}
	log.Printf("Successfully scraped %d deals.", len(scrapedDeals))

	var newCount, updatedCount int
	var errorMessages []string

	for _, deal := range scrapedDeals {
		isNew, isUpdated, err := p.processSingleDeal(ctx, deal)
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

	log.Printf("Finished processing. New: %d, Updated: %d", newCount, updatedCount)
	if len(errorMessages) > 0 {
		return fmt.Errorf("processed with errors: %s", strings.Join(errorMessages, "; "))
	}
	return nil
}

func (p *DealProcessor) processSingleDeal(ctx context.Context, deal models.DealInfo) (isNew, isUpdated bool, err error) {
	if strings.TrimSpace(deal.Title) == "" || strings.TrimSpace(deal.PostURL) == "" {
		log.Printf("Skipping invalid deal: %s", deal.Title)
		return false, false, nil
	}

	hash := sha256.Sum256([]byte(deal.PostURL))
	deal.FirestoreID = hex.EncodeToString(hash[:])
	deal.LastUpdated = time.Now()

	// Try to find existing deal
	existing, err := p.store.GetDealByID(ctx, deal.FirestoreID)
	if err != nil {
		return false, false, fmt.Errorf("error checking Firestore for deal %s: %v", deal.FirestoreID, err)
	}

	// New deal — try to create it
	if existing == nil {
		createErr := p.store.TryCreateDeal(ctx, deal)
		if createErr == nil {
			log.Printf("New deal '%s' added.", deal.Title)
			if err := p.store.TrimOldDeals(ctx, 500); err != nil {
				log.Printf("Warning: Failed to trim old deals: %v", err)
			}
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
			log.Printf("Race condition anomaly: deal %s claimed to exist but returned nil", deal.FirestoreID)
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

	// Apply updates
	existing.Title = deal.Title
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

	if err := p.store.UpdateDeal(ctx, *existing); err != nil {
		log.Printf("Warning: Failed to update existing deal %s: %v", existing.FirestoreID, err)
		return false, false, nil
	}
	log.Printf("Updated deal: %s", existing.Title)

	// Throttle Discord updates
	if existing.DiscordMessageID != "" && time.Since(existing.DiscordLastUpdatedTime) >= p.updateInterval {
		if err := p.notifier.Update(ctx, existing.DiscordMessageID, *existing); err == nil {
			existing.DiscordLastUpdatedTime = time.Now()
			if err := p.store.UpdateDeal(ctx, *existing); err != nil {
				log.Printf("Warning: Failed to save Discord update timestamp: %v", err)
			}
		}
	}

	return false, true, nil
}

func (p *DealProcessor) sendAndSaveDiscordID(ctx context.Context, deal *models.DealInfo) {
	msgID, err := p.notifier.Send(ctx, *deal)
	if err != nil {
		log.Printf("Error sending to Discord: %v", err)
		return
	}
	deal.DiscordMessageID = msgID
	deal.DiscordLastUpdatedTime = time.Now()
	if err := p.store.UpdateDeal(ctx, *deal); err != nil {
		log.Printf("Warning: Failed to save Discord message ID for %s: %v", deal.FirestoreID, err)
	}
}

func (p *DealProcessor) dealChanged(existing *models.DealInfo, scraped *models.DealInfo) bool {
	return existing.LikeCount != scraped.LikeCount ||
		existing.CommentCount != scraped.CommentCount ||
		existing.ViewCount != scraped.ViewCount ||
		existing.Title != scraped.Title ||
		existing.ThreadImageURL != scraped.ThreadImageURL ||
		existing.ActualDealURL != scraped.ActualDealURL
}
