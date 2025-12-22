package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

const discordUpdateInterval = 10 * time.Minute

type Processor interface {
	ProcessDeals(ctx context.Context) error
}

type DealProcessor struct {
	store    *storage.Client
	notifier *notifier.Client
	scraper  scraper.Scraper
}

func New(store *storage.Client, n *notifier.Client, s scraper.Scraper) *DealProcessor {
	return &DealProcessor{
		store:    store,
		notifier: n,
		scraper:  s,
	}
}

func (p *DealProcessor) ProcessDeals(ctx context.Context) error {
	var errorMessages []string

	scrapedDeals, err := p.scraper.ScrapeHotDealsPage(ctx)
	if err != nil {
		return fmt.Errorf("failed to scrape hot deals page: %w", err)
	}
	log.Printf("Successfully scraped %d deals.", len(scrapedDeals))

	var newDealsCount, updatedDealsCount int

	for _, dealToProcess := range scrapedDeals {
		if strings.TrimSpace(dealToProcess.Title) == "" || strings.TrimSpace(dealToProcess.PostURL) == "" {
			log.Printf("Skipping invalid deal: %s", dealToProcess.Title)
			continue
		}

		hash := sha256.Sum256([]byte(dealToProcess.PostURL))
		dealToProcess.FirestoreID = hex.EncodeToString(hash[:])
		dealToProcess.LastUpdated = time.Now()

		existingDeal, err := p.store.GetDealByID(ctx, dealToProcess.FirestoreID)
		if err != nil {
			msg := fmt.Sprintf("Error checking Firestore for deal %s: %v", dealToProcess.FirestoreID, err)
			log.Println(msg)
			errorMessages = append(errorMessages, msg)
			continue
		}

		if existingDeal == nil {
			err := p.store.TryCreateDeal(ctx, dealToProcess)
			if err != nil {
				if err.Error() == "deal already exists" {
					var getErr error
					existingDeal, getErr = p.store.GetDealByID(ctx, dealToProcess.FirestoreID)
					if getErr != nil {
						msg := fmt.Sprintf("Error recovering from race condition for deal %s: %v", dealToProcess.FirestoreID, getErr)
						log.Println(msg)
						errorMessages = append(errorMessages, msg)
						continue
					}
					if existingDeal == nil {
						// Should not happen if it claimed to exist
						log.Printf("Race condition anomaly: Deal %s claimed to exist but returned nil on refetch", dealToProcess.FirestoreID)
						continue
					}
				} else {
					msg := fmt.Sprintf("Failed to create deal %s: %v", dealToProcess.Title, err)
					log.Println(msg)
					errorMessages = append(errorMessages, msg)
					continue
				}
			} else {
				log.Printf("New deal '%s' added.", dealToProcess.Title)
				newDealsCount++
				if err := p.store.TrimOldDeals(ctx, 50); err != nil {
					log.Printf("Warning: Failed to trim old deals: %v", err)
				}

				msgID, sendErr := p.notifier.Send(ctx, dealToProcess)
				if sendErr == nil {
					dealToProcess.DiscordMessageID = msgID
					dealToProcess.DiscordLastUpdatedTime = time.Now()
					if err := p.store.UpdateDeal(ctx, dealToProcess); err != nil {
						log.Printf("Warning: Failed to update deal %s with Discord Message ID: %v", dealToProcess.FirestoreID, err)
					}
				} else {
					log.Printf("Error sending to Discord: %v", sendErr)
				}
				continue
			}
		}

		if existingDeal != nil {
			if existingDeal.DiscordMessageID == "" {
				msgID, sendErr := p.notifier.Send(ctx, *existingDeal)
				if sendErr == nil {
					existingDeal.DiscordMessageID = msgID
					existingDeal.DiscordLastUpdatedTime = time.Now()
					if err := p.store.UpdateDeal(ctx, *existingDeal); err != nil {
						log.Printf("Warning: Failed to update existing deal %s with Discord Message ID: %v", existingDeal.FirestoreID, err)
					}
				}
			}

			updateNeeded := false
			if existingDeal.LikeCount != dealToProcess.LikeCount ||
				existingDeal.CommentCount != dealToProcess.CommentCount ||
				existingDeal.ViewCount != dealToProcess.ViewCount ||
				existingDeal.Title != dealToProcess.Title ||
				existingDeal.ThreadImageURL != dealToProcess.ThreadImageURL {
				updateNeeded = true
			}

			existingDeal.Title = dealToProcess.Title
			existingDeal.LikeCount = dealToProcess.LikeCount
			existingDeal.CommentCount = dealToProcess.CommentCount
			existingDeal.ViewCount = dealToProcess.ViewCount
			existingDeal.ThreadImageURL = dealToProcess.ThreadImageURL
			existingDeal.AuthorName = dealToProcess.AuthorName
			existingDeal.AuthorURL = dealToProcess.AuthorURL
			existingDeal.PostedTime = dealToProcess.PostedTime
			existingDeal.PublishedTimestamp = dealToProcess.PublishedTimestamp
			existingDeal.ActualDealURL = dealToProcess.ActualDealURL
			existingDeal.LastUpdated = time.Now()

			if err := p.store.UpdateDeal(ctx, *existingDeal); err == nil {
				if updateNeeded {
					updatedDealsCount++
					if existingDeal.DiscordMessageID != "" {
						if time.Since(existingDeal.DiscordLastUpdatedTime) >= discordUpdateInterval {
							if err := p.notifier.Update(ctx, existingDeal.DiscordMessageID, *existingDeal); err == nil {
								existingDeal.DiscordLastUpdatedTime = time.Now()
								if err := p.store.UpdateDeal(ctx, *existingDeal); err != nil {
									log.Printf("Warning: Failed to update deal timestamp after Discord update: %v", err)
								}
							}
						}
					}
				}
			} else {
				log.Printf("Warning: Failed to update existing deal %s: %v", existingDeal.FirestoreID, err)
			}
		}
	}

	log.Printf("Finished processing. New: %d, Updated: %d", newDealsCount, updatedDealsCount)
	if len(errorMessages) > 0 {
		return fmt.Errorf("processed with errors: %s", strings.Join(errorMessages, "; "))
	}
	return nil
}
