package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

type Server struct {
	store    *storage.Client
	notifier *notifier.Client
	config   *config.Config
}

func main() {
	log.Println("Starting RFD Hot Deals Bot server...")
	cfg := config.Load()

	ctx := context.Background()
	store, err := storage.New(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Critical error initializing Firestore client: %v", err)
	}
	defer store.Close()

	n := notifier.New(cfg.DiscordWebhookURL)

	srv := &Server{
		store:    store,
		notifier: n,
		config:   cfg,
	}

	http.HandleFunc("/", srv.ProcessDealsHandler)
	http.HandleFunc("/process-deals", srv.ProcessDealsHandler)

	log.Printf("Listening on port %s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("Failed to listen and serve: %v", err)
	}
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	// log.Println("ProcessDealsHandler invoked.")
	ctx := context.Background()
	var errorMessages []string

	// Scrape
	scrapedDeals, err := scraper.ScrapeHotDealsPage()
	if err != nil {
		log.Printf("Critical error scraping hot deals page: %v", err)
		http.Error(w, fmt.Sprintf("Failed to scrape hot deals page: %v", err), http.StatusInternalServerError)
		return
	}
	log.Printf("Successfully scraped %d deals.", len(scrapedDeals))

	var newDealsCount, updatedDealsCount int

	for _, dealToProcess := range scrapedDeals {
		// Validate
		if strings.TrimSpace(dealToProcess.Title) == "" || strings.TrimSpace(dealToProcess.PostURL) == "" {
			log.Printf("Skipping invalid deal: %s", dealToProcess.Title)
			continue
		}

		// Generate ID
		hash := sha256.Sum256([]byte(dealToProcess.PostURL))
		dealToProcess.FirestoreID = hex.EncodeToString(hash[:])
		dealToProcess.LastUpdated = time.Now()

		existingDeal, err := s.store.GetDealByID(ctx, dealToProcess.FirestoreID)
		if err != nil {
			msg := fmt.Sprintf("Error checking Firestore for deal %s: %v", dealToProcess.FirestoreID, err)
			log.Println(msg)
			errorMessages = append(errorMessages, msg)
			continue
		}

		if existingDeal == nil {
			// Create
			err := s.store.TryCreateDeal(ctx, dealToProcess)
			if err != nil {
				if err.Error() == "deal already exists" {
					// Recover from race
					existingDeal, _ = s.store.GetDealByID(ctx, dealToProcess.FirestoreID)
					// Fall through to update logic if we recovered
					if existingDeal == nil {
						continue
					}
				} else {
					msg := fmt.Sprintf("Failed to create deal %s: %v", dealToProcess.Title, err)
					log.Println(msg)
					errorMessages = append(errorMessages, msg)
					continue
				}
			} else {
				// Success
				log.Printf("New deal '%s' added.", dealToProcess.Title)
				newDealsCount++
				s.store.TrimOldDeals(ctx, 50)

				msgID, sendErr := s.notifier.Send(ctx, dealToProcess)
				if sendErr == nil {
					dealToProcess.DiscordMessageID = msgID
					dealToProcess.DiscordLastUpdatedTime = time.Now()
					s.store.UpdateDeal(ctx, dealToProcess)
				} else {
					log.Printf("Error sending to Discord: %v", sendErr)
				}
				continue
			}
		}

		// Update logic
		if existingDeal != nil {
			// Check for recovery of missing discord ID
			if existingDeal.DiscordMessageID == "" {
				msgID, sendErr := s.notifier.Send(ctx, *existingDeal)
				if sendErr == nil {
					existingDeal.DiscordMessageID = msgID
					existingDeal.DiscordLastUpdatedTime = time.Now()
					s.store.UpdateDeal(ctx, *existingDeal)
				}
			}

			// Update fields
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
			existingDeal.ActualDealURL = dealToProcess.ActualDealURL // Ensure this is synced
			existingDeal.LastUpdated = time.Now()

			if err := s.store.UpdateDeal(ctx, *existingDeal); err == nil {
				if updateNeeded {
					updatedDealsCount++
					if existingDeal.DiscordMessageID != "" {
						if time.Since(existingDeal.DiscordLastUpdatedTime) >= 10*time.Minute {
							if err := s.notifier.Update(ctx, existingDeal.DiscordMessageID, *existingDeal); err == nil {
								existingDeal.DiscordLastUpdatedTime = time.Now()
								s.store.UpdateDeal(ctx, *existingDeal)
							}
						}
					}
				}
			}
		}
	}

	log.Printf("Finished processing. New: %d, Updated: %d", newDealsCount, updatedDealsCount)
	if len(errorMessages) > 0 {
		http.Error(w, fmt.Sprintf("Processed with errors: %s", strings.Join(errorMessages, "; ")), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "Deals processed successfully.")
}
