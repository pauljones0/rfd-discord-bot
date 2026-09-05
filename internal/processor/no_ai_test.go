package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type noAISubscriptionStore struct{ *mockStore }

func (s *noAISubscriptionStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return []models.Subscription{{GuildID: "guild1", ChannelID: "channel1", SubscriptionType: "rfd", DealType: dealtypes.RFDWarmHot}}, nil
}

func TestProcessDealsWithoutAIKeepsTitlesAndReceiptsAcrossPolls(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	scraper := &mockScraper{}
	// More than one title batch reproduced the crash when no optional AI
	// credentials were configured. Every fixture is independently eligible.
	for i := range titleBatchSize + 2 {
		postURL := fmt.Sprintf("https://forums.redflagdeals.com/product-%d", i)
		scraper.deals = append(scraper.deals, models.DealInfo{
			Title:              fmt.Sprintf("Product SKU%d for $50", 10000+i),
			PostURL:            postURL,
			ActualDealURL:      fmt.Sprintf("https://retailer.example/product-%d", i),
			Price:              "$50",
			OriginalPrice:      "$100",
			Description:        "Fixture product details",
			PublishedTimestamp: testTime1.Add(time.Duration(i) * time.Minute),
			Threads:            []models.ThreadContext{{PostURL: postURL, LikeCount: 100}},
		})
	}
	p := newTestProcessor(store, notifications, scraper)
	p.aiClient = nil
	p.config.DiscordAppID = "200"
	for poll := range 2 {
		if err := p.ProcessDeals(context.Background()); err != nil {
			t.Fatalf("poll %d: %v", poll, err)
		}
		if len(notifications.sentDeals) != len(scraper.deals) || len(store.deals) != len(scraper.deals) {
			t.Fatalf("poll %d: want %d total sends and saved deals, got %d sends and %d deals", poll, len(scraper.deals), len(notifications.sentDeals), len(store.deals))
		}
		for _, input := range scraper.deals {
			stored := store.deals[generateDealID(input.PublishedTimestamp)]
			if stored == nil || stored.Title != input.Title || stored.CleanTitle != "" || stored.AIProcessed {
				t.Fatalf("poll %d changed the original title or falsely recorded AI processing: %+v", poll, stored)
			}
			if stored.DiscordMessageIDs["channel1"] == "" || stored.DiscordMessageApplicationIDs["channel1"] != "200" {
				t.Fatalf("poll %d lost the notification receipt or ownership: %+v", poll, stored)
			}
		}
	}
	for _, sent := range notifications.sentDeals {
		if sent.CleanTitle != "" || sent.AIProcessed {
			t.Fatalf("notification should use its original title: %+v", sent)
		}
	}
}

func TestProcessDealsWithoutAIInvalidatesOnlyChangedCleanTitles(t *testing.T) {
	for _, titleChanged := range []bool{false, true} {
		t.Run(fmt.Sprintf("title_changed_%t", titleChanged), func(t *testing.T) {
			store := newMockStore()
			notifications := newMockNotifier()
			published := time.Now().Add(-time.Hour)
			original := models.DealInfo{
				DocumentID:                   generateDealID(published),
				Title:                        "Original title",
				CleanTitle:                   "Previously cleaned title",
				AIProcessed:                  true,
				PostURL:                      "https://forums.redflagdeals.com/original-deal",
				ActualDealURL:                "https://retailer.example/original-product",
				Price:                        "$50",
				OriginalPrice:                "$100",
				PublishedTimestamp:           published,
				DiscordLastUpdatedTime:       time.Now().Add(-20 * time.Minute),
				DiscordMessageIDs:            map[string]string{"channel1": "300"},
				DiscordMessageApplicationIDs: map[string]string{"channel1": "200"},
			}
			store.deals[original.DocumentID] = &original
			input := models.DealInfo{
				Title:              original.Title,
				PostURL:            original.PostURL,
				ActualDealURL:      original.ActualDealURL,
				Price:              "$40", // Also update content with an unchanged title.
				OriginalPrice:      original.OriginalPrice,
				PublishedTimestamp: original.PublishedTimestamp,
			}
			if titleChanged {
				input.Title = "Updated original title"
			}
			p := newTestProcessor(store, notifications, &mockScraper{deals: []models.DealInfo{input}})
			p.aiClient = nil
			p.config.DiscordAppID = "200"
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			stored := store.deals[original.DocumentID]
			if stored.Title != input.Title || stored.Price != input.Price {
				t.Fatalf("changed source content was not saved: %+v", stored)
			}
			if titleChanged {
				if stored.CleanTitle != "" || stored.AIProcessed {
					t.Fatalf("changed title retained stale AI metadata: %+v", stored)
				}
			} else if stored.CleanTitle != original.CleanTitle || !stored.AIProcessed {
				t.Fatalf("unchanged title lost existing AI metadata: %+v", stored)
			}
			if len(notifications.sentDeals) != 0 || len(notifications.updatedIDs) != 1 || stored.DiscordMessageIDs["channel1"] != "300" || stored.DiscordMessageApplicationIDs["channel1"] != "200" {
				t.Fatalf("existing notification should be updated with its receipt preserved: sends=%d updates=%v deal=%+v", len(notifications.sentDeals), notifications.updatedIDs, stored)
			}
		})
	}
}
