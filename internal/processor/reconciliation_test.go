package processor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type subscriptionStore struct {
	*mockStore
	subscriptions               []models.Subscription
	historyErr, subscriptionErr error
}

func (s *subscriptionStore) GetAllSubscriptions(context.Context) ([]models.Subscription, error) {
	return s.subscriptions, s.subscriptionErr
}
func (s *subscriptionStore) GetRecentDeals(ctx context.Context, age time.Duration) ([]models.DealInfo, error) {
	if s.historyErr != nil {
		return nil, s.historyErr
	}
	return s.mockStore.GetRecentDeals(ctx, age)
}
func testSubscription(channel string) models.Subscription {
	return models.Subscription{GuildID: "guild1", ChannelID: channel, DealType: dealtypes.RFDAll}
}

type partialNotifier struct {
	calls   [][]string
	failing string
	onSend  func()
	updated []models.DealInfo
}

func (n *partialNotifier) Send(_ context.Context, _ models.DealInfo, subs []models.Subscription) (map[string]string, error) {
	receipts := map[string]string{}
	var failures []error
	var channels []string
	for _, sub := range subs {
		channels = append(channels, sub.ChannelID)
		if sub.ChannelID == n.failing {
			failures = append(failures, errors.New("fixture channel unavailable"))
			continue
		}
		receipts[sub.ChannelID] = "message-" + sub.ChannelID
	}
	n.calls = append(n.calls, channels)
	if n.onSend != nil {
		n.onSend()
	}
	return receipts, errors.Join(failures...)
}
func (n *partialNotifier) Update(_ context.Context, deal models.DealInfo) error {
	n.updated = append(n.updated, cloneDeal(deal))
	return nil
}

func TestDeliveryRetriesUnchangedDealsAndAddsSubscriptions(t *testing.T) {
	store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a"), testSubscription("b")}}
	notifier := &partialNotifier{failing: "b"}
	input := titleFixture(101)
	p := newTestProcessor(store, notifier, &mockScraper{deals: []models.DealInfo{input}})
	p.aiClient = nil
	p.config.DiscordAppID = "owner"
	if err := p.ProcessDeals(context.Background()); err == nil {
		t.Fatal("partial delivery should mark poll unhealthy")
	}
	id := generateDealID(input.PublishedTimestamp)
	saved := store.deals[id]
	if saved == nil || saved.DiscordMessageIDs["a"] != "message-a" || saved.DiscordMessageApplicationIDs["a"] != "owner" || saved.DiscordMessageIDs["b"] != "" {
		t.Fatalf("partial success was not saved with ownership: %+v", saved)
	}
	notifier.failing = ""
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.subscriptions = append(store.subscriptions, testSubscription("c"))
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"a", "b"}, {"b"}, {"c"}}; !reflect.DeepEqual(notifier.calls, want) {
		t.Fatalf("delivery attempts=%v want=%v", notifier.calls, want)
	}
	if len(store.deals[id].DiscordMessageIDs) != 3 {
		t.Fatalf("receipts=%v", store.deals[id].DiscordMessageIDs)
	}
}

func TestTitleCleanupPreservesDiscountEvidenceAcrossPolls(t *testing.T) {
	store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{{GuildID: "guild1", ChannelID: "warm", DealType: dealtypes.RFDWarmHot}}}
	input := titleFixture(102)
	input.Title = "Product ZX9000"
	input.Price, input.OriginalPrice = "", ""
	input.Description = "Clearance offer at the store"
	input.Summary = "Limited stock available"
	scraper := &mockScraper{deals: []models.DealInfo{input}}
	p := newTestProcessor(store, newMockNotifier(), scraper)
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := generateDealID(input.PublishedTimestamp)
	if got := store.deals[id]; !got.HasBeenWarm || got.Description != input.Description || got.Summary != input.Summary || !got.AIProcessed {
		t.Fatalf("cleanup changed discount source evidence: %+v", got)
	}
	// A later card contains only card metadata. Votes change while the description
	// stays cached: title formatting must not erase eligibility on this poll.
	scraper.deals[0].Description, scraper.deals[0].Summary = "", ""
	scraper.deals[0].Threads[0].LikeCount++
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.deals[id]; !got.HasBeenWarm || got.Description != input.Description || !p.isDealEligibleForSubscription(*got, store.subscriptions[0]) {
		t.Fatalf("later poll lost cached discount evidence: %+v", got)
	}
	if len(scraper.fetchedDetails) != 1 {
		t.Fatalf("title cleanup forced redundant detail fetches: %d", len(scraper.fetchedDetails))
	}
}

func TestHistoryAndSubscriptionFailuresPreventDelivery(t *testing.T) {
	for _, failure := range []string{"history", "subscriptions"} {
		t.Run(failure, func(t *testing.T) {
			store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a")}}
			if failure == "history" {
				store.historyErr = errors.New("fixture history unavailable")
			} else {
				store.subscriptionErr = errors.New("fixture subscriptions unavailable")
			}
			notifier := &partialNotifier{}
			p := newTestProcessor(store, notifier, &mockScraper{deals: []models.DealInfo{titleFixture(103)}})
			if err := p.ProcessDeals(context.Background()); err == nil {
				t.Fatal("failed prerequisite reported healthy")
			}
			if len(notifier.calls) != 0 || len(store.deals) != 0 {
				t.Fatal("failed prerequisites caused side effects")
			}
		})
	}
}

func TestStorageFailureStopsFurtherDeliveries(t *testing.T) {
	store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a")}}
	store.createErr = errors.New("fixture disk full")
	notifier := &partialNotifier{}
	p := newTestProcessor(store, notifier, &mockScraper{deals: []models.DealInfo{titleFixture(105), titleFixture(104)}})
	p.aiClient = nil
	if err := p.ProcessDeals(context.Background()); err == nil {
		t.Fatal("storage failure reported healthy")
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("continued sending after receipt storage failed: %v", notifier.calls)
	}
}

type cancellationAwareStore struct {
	*subscriptionStore
	savedWhilePollCancelled bool
}

func (s *cancellationAwareStore) BatchWrite(ctx context.Context, creates, updates []models.DealInfo) error {
	if ctx.Err() != nil {
		return fmt.Errorf("cancelled receipt persistence: %w", ctx.Err())
	}
	s.savedWhilePollCancelled = true
	return s.mockStore.BatchWrite(ctx, creates, updates)
}
func TestSuccessfulReceiptSavedWhenPollCancelledDuringSend(t *testing.T) {
	store := &cancellationAwareStore{subscriptionStore: &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a")}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notifier := &partialNotifier{onSend: cancel}
	input := titleFixture(106)
	p := newTestProcessor(store, notifier, &mockScraper{deals: []models.DealInfo{input}})
	p.aiClient = nil
	_ = p.ProcessDeals(ctx)
	saved := store.deals[generateDealID(input.PublishedTimestamp)]
	if !store.savedWhilePollCancelled || saved == nil || saved.DiscordMessageIDs["a"] != "message-a" {
		t.Fatalf("cancellation lost successful receipt: %+v", saved)
	}
}

func TestReconciliationDoesNotMutateStoredSlicesOrReceipts(t *testing.T) {
	original := titleFixture(107)
	original.DocumentID = generateDealID(original.PublishedTimestamp)
	original.DiscordMessageIDs = map[string]string{"old": "receipt"}
	snapshot := cloneDeal(original)
	observation := cloneDeal(original)
	observation.Threads[0].LikeCount++
	p := newTestProcessor(newMockStore(), newMockNotifier(), &mockScraper{})
	reconciled := p.reconcileDeal(&original, []models.DealInfo{observation})
	reconciled.DiscordMessageIDs["new"] = "new receipt"
	if !reflect.DeepEqual(original, snapshot) {
		t.Fatalf("reconciliation mutated input: before=%+v after=%+v", snapshot, original)
	}
	if reconciled.Threads[0].LikeCount == original.Threads[0].LikeCount {
		t.Fatal("reconciliation lost new votes")
	}
}

func TestPendingEditRetriesAfterCooldownWithoutRepeatingUnchangedEdits(t *testing.T) {
	store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a")}}
	notifier := newMockNotifier()
	input := titleFixture(108)
	input.PublishedTimestamp = time.Now().Add(-time.Hour)
	scraper := &mockScraper{deals: []models.DealInfo{input}}
	p := newTestProcessor(store, notifier, scraper)
	p.aiClient = nil
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := generateDealID(input.PublishedTimestamp)
	// A new observation is saved during the edit cooldown. It must be published
	// once when the cooldown is over even if the source stops changing.
	scraper.deals[0].Price = "$40"
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.updatedIDs) != 0 {
		t.Fatal("edited inside cooldown")
	}
	p.updateInterval = 0
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.updatedIDs) != 1 {
		t.Fatalf("pending update lost: %v", notifier.updatedIDs)
	}
	if !store.deals[id].DiscordLastUpdatedTime.After(store.deals[id].LastUpdated) {
		t.Fatal("pending change was not acknowledged")
	}
	for range 2 {
		if err := p.ProcessDeals(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(notifier.updatedIDs) != 1 {
		t.Fatalf("unchanged content edited repeatedly: %v", notifier.updatedIDs)
	}
}

func TestAllGoneThreadsDoNotAlertNewSubscriptions(t *testing.T) {
	store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("new")}}
	input := titleFixture(109)
	input.PublishedTimestamp = time.Now().Add(-time.Hour)
	input.LastUpdated = time.Now().Add(-30 * time.Minute)
	input.DiscordLastUpdatedTime = time.Now().Add(-20 * time.Minute)
	input.DocumentID = generateDealID(input.PublishedTimestamp)
	input.DiscordMessageIDs = map[string]string{"old": "receipt"}
	store.deals[input.DocumentID] = &input
	observed := cloneDeal(input)
	observed.Title += " updated" // force a detail request
	scraper := &mockScraper{deals: []models.DealInfo{observed}, mutateDetails: func(deals []*models.DealInfo) {
		for _, deal := range deals {
			deal.Threads[0].NotFound = true
		}
	}}
	notifier := &partialNotifier{}
	p := newTestProcessor(store, notifier, scraper)
	p.aiClient = nil
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("gone deal generated alert: %v", notifier.calls)
	}
	if len(notifier.updated) != 1 || len(notifier.updated[0].Threads) != 0 || notifier.updated[0].PostURL != "" {
		t.Fatalf("old Discord card retained gone thread links: %+v", notifier.updated)
	}
	got := store.deals[input.DocumentID]
	if len(got.Threads) != 0 || got.PostURL != "" || got.DiscordMessageIDs["old"] != "receipt" {
		t.Fatalf("gone thread cleanup lost durable receipt: %+v", got)
	}
}

func TestNewChannelReceiptDoesNotAcknowledgePendingEditsInExistingChannels(t *testing.T) {
	for _, dueImmediately := range []bool{false, true} {
		t.Run(fmt.Sprintf("edit_due_%t", dueImmediately), func(t *testing.T) {
			store := &subscriptionStore{mockStore: newMockStore(), subscriptions: []models.Subscription{testSubscription("a")}}
			notifier := newMockNotifier()
			input := titleFixture(110)
			input.PublishedTimestamp = time.Now().Add(-time.Hour)
			scraper := &mockScraper{deals: []models.DealInfo{input}}
			p := newTestProcessor(store, notifier, scraper)
			p.aiClient = nil
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			id := generateDealID(input.PublishedTimestamp)
			originalAck := store.deals[id].DiscordLastUpdatedTime
			if dueImmediately {
				p.updateInterval = 0
			}
			scraper.deals[0].Price = "$35"
			store.subscriptions = append(store.subscriptions, testSubscription("b"))
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			saved := store.deals[id]
			if saved.DiscordMessageIDs["b"] == "" {
				t.Fatal("new channel receipt lost")
			}
			if dueImmediately {
				if want := []string{"msg-123-a"}; !reflect.DeepEqual(notifier.updatedIDs, want) {
					t.Fatalf("same-poll edit must update old channel only: %v", notifier.updatedIDs)
				}
			} else {
				if !saved.DiscordLastUpdatedTime.Equal(originalAck) {
					t.Fatal("new channel acknowledged unsent old-channel edit")
				}
				if len(notifier.updatedIDs) != 0 {
					t.Fatal("edit bypassed cooldown")
				}
			}
			p.updateInterval = 0
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			foundA := false
			for _, message := range notifier.updatedIDs {
				foundA = foundA || message == "msg-123-a"
			}
			if !foundA {
				t.Fatalf("existing channel never received pending price update: %v", notifier.updatedIDs)
			}
			updates := len(notifier.updatedIDs)
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(notifier.updatedIDs) != updates {
				t.Fatal("acknowledged edit repeated")
			}
		})
	}
}
