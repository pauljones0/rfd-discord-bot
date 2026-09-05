package processor

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type recordingTitleAnalyzer struct {
	batches [][]models.TitleRequest
	respond func([]models.TitleRequest) (map[int]string, error)
}

func (a *recordingTitleAnalyzer) CleanTitles(_ context.Context, requests []models.TitleRequest) (map[int]string, error) {
	a.batches = append(a.batches, append([]models.TitleRequest(nil), requests...))
	if a.respond != nil {
		return a.respond(requests)
	}
	results := make(map[int]string, len(requests))
	for _, request := range requests {
		results[request.Index] = "Cleaned: " + request.Title
	}
	return results, nil
}

func (*recordingTitleAnalyzer) DrainTokens() (int, int) { return 0, 0 }

type waitingTitleAnalyzer struct{ calls int }

func (a *waitingTitleAnalyzer) CleanTitles(ctx context.Context, _ []models.TitleRequest) (map[int]string, error) {
	a.calls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func (*waitingTitleAnalyzer) DrainTokens() (int, int) { return 0, 0 }

func titleFixture(index int) models.DealInfo {
	postURL := fmt.Sprintf("https://forums.redflagdeals.com/product-%d", 70000+index)
	return models.DealInfo{
		Title:              fmt.Sprintf("Product SKU%d for $50", 70000+index),
		PostURL:            postURL,
		ActualDealURL:      fmt.Sprintf("https://retailer.example/product-%d", 70000+index),
		Price:              "$50",
		OriginalPrice:      "$100",
		Description:        "Fixture product details",
		PublishedTimestamp: testTime1.Add(time.Duration(index) * time.Minute),
		Threads:            []models.ThreadContext{{PostURL: postURL, LikeCount: 100}},
	}
}

func assertCleanedTitle(t *testing.T, stored *models.DealInfo, input models.DealInfo) {
	t.Helper()
	if stored == nil || stored.Title != input.Title || stored.CleanTitle != "Cleaned: "+input.Title || !stored.AIProcessed {
		t.Fatalf("cleanup did not match its source deal %q: %+v", input.Title, stored)
	}
}

func TestTitleCleaningPersistsShortPollsAndKeepsReorderedDealsMapped(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	analyzer := &recordingTitleAnalyzer{}
	first, second := titleFixture(0), titleFixture(1)
	scraper := &mockScraper{deals: []models.DealInfo{first}}
	p := newTestProcessor(store, notifications, scraper)
	p.aiClient = analyzer
	p.config.DiscordAppID = "200"
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCleanedTitle(t, store.deals[generateDealID(first.PublishedTimestamp)], first)
	if len(analyzer.batches) != 1 || len(analyzer.batches[0]) != 1 {
		t.Fatalf("short poll was not cleaned immediately: %+v", analyzer.batches)
	}

	// The next poll reuses position zero for a different deal. Its output must
	// never overwrite the earlier deal or get lost in a previous poll's slice.
	scraper.deals = []models.DealInfo{second, first}
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCleanedTitle(t, store.deals[generateDealID(first.PublishedTimestamp)], first)
	assertCleanedTitle(t, store.deals[generateDealID(second.PublishedTimestamp)], second)
	if len(analyzer.batches) != 2 || len(analyzer.batches[1]) != 1 || analyzer.batches[1][0].Title != second.Title {
		t.Fatalf("already-cleaned deal was repeated or next title was mismatched: %+v", analyzer.batches)
	}
	scraper.deals = []models.DealInfo{first, second}
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(analyzer.batches) != 2 || len(notifications.sentDeals) != 2 {
		t.Fatalf("unchanged poll repeated cleanup or notifications: batches=%d sends=%d", len(analyzer.batches), len(notifications.sentDeals))
	}
	for _, sent := range notifications.sentDeals {
		assertCleanedTitle(t, &sent, sent)
	}
	if len(p.titleQueue) != 0 || len(p.titleQueueDeals) != 0 || !p.titleQueueStart.IsZero() {
		t.Fatal("poll retained title requests or deal pointers")
	}
}

func TestTitleCleaningBackfillsUnchangedHistoryOnceAndKeepsReceipts(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	analyzer := &recordingTitleAnalyzer{}
	input := titleFixture(2)
	stored := input
	stored.DocumentID = generateDealID(input.PublishedTimestamp)
	stored.DiscordMessageIDs = map[string]string{"channel1": "300"}
	stored.DiscordMessageApplicationIDs = map[string]string{"channel1": "100"}
	store.deals[stored.DocumentID] = &stored
	p := newTestProcessor(store, notifications, &mockScraper{deals: []models.DealInfo{input}})
	p.aiClient = analyzer
	p.config.DiscordAppID = "200"
	for poll := range 2 {
		if err := p.ProcessDeals(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := store.deals[stored.DocumentID]
		assertCleanedTitle(t, got, input)
		if got.DiscordMessageIDs["channel1"] != "300" || got.DiscordMessageApplicationIDs["channel1"] != "100" {
			t.Fatalf("poll %d changed imported receipt ownership: %+v", poll, got)
		}
	}
	if len(analyzer.batches) != 1 || store.updateCount != 1 || len(notifications.sentDeals) != 0 {
		t.Fatalf("history was not cleaned exactly once: batches=%d updates=%d sends=%d", len(analyzer.batches), store.updateCount, len(notifications.sentDeals))
	}
}

func TestTitleCleaningBatchesAreBoundedAndIndexesIdentifyCorrectDeals(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	analyzer := &recordingTitleAnalyzer{}
	scraper := &mockScraper{}
	for index := range titleBatchSize*2 + 3 {
		scraper.deals = append(scraper.deals, titleFixture(index+10))
	}
	p := newTestProcessor(store, newMockNotifier(), scraper)
	p.aiClient = analyzer
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(analyzer.batches) != 3 {
		t.Fatalf("want three bounded batches, got %d", len(analyzer.batches))
	}
	for _, batch := range analyzer.batches {
		if len(batch) > titleBatchSize {
			t.Fatalf("batch has %d requests, limit %d", len(batch), titleBatchSize)
		}
		seen := map[int]bool{}
		for _, request := range batch {
			if seen[request.Index] {
				t.Fatalf("batch reused index %d: %+v", request.Index, batch)
			}
			seen[request.Index] = true
		}
	}
	for _, input := range scraper.deals {
		assertCleanedTitle(t, store.deals[generateDealID(input.PublishedTimestamp)], input)
	}
}

func TestTitleCleaningFailureInvalidatesEditedTitleAndRetriesWithoutReplay(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	analyzer := &recordingTitleAnalyzer{respond: func([]models.TitleRequest) (map[int]string, error) {
		return nil, errors.New("fixture Gemini unavailable")
	}}
	input := titleFixture(40)
	stored := input
	stored.DocumentID = generateDealID(input.PublishedTimestamp)
	stored.CleanTitle = "Old cleaned product title"
	stored.AIProcessed = true
	stored.DiscordMessageIDs = map[string]string{"channel1": "300"}
	stored.DiscordMessageApplicationIDs = map[string]string{"channel1": "100"}
	store.deals[stored.DocumentID] = &stored
	input.Title += " now $40"
	input.Price = "$40"
	p := newTestProcessor(store, notifications, &mockScraper{deals: []models.DealInfo{input}})
	p.aiClient = analyzer
	p.config.DiscordAppID = "200"
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatalf("optional cleanup blocked processing: %v", err)
	}
	got := store.deals[stored.DocumentID]
	if got.Title != input.Title || got.Price != "$40" || got.CleanTitle != "" || got.AIProcessed {
		t.Fatalf("failed cleanup retained stale output or lost source changes: %+v", got)
	}
	if got.DiscordMessageIDs["channel1"] != "300" || got.DiscordMessageApplicationIDs["channel1"] != "100" {
		t.Fatalf("failed cleanup changed receipt: %+v", got)
	}
	analyzer.respond = nil
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCleanedTitle(t, store.deals[stored.DocumentID], input)
	if len(analyzer.batches) != 2 || len(notifications.sentDeals) != 0 {
		t.Fatalf("recovery did not retry exactly once without replay: batches=%d sends=%d", len(analyzer.batches), len(notifications.sentDeals))
	}
}

func TestTitleCleaningPartialOutputKeepsMissingDealAndRetriesOnlyMissingTitle(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	first, second := titleFixture(50), titleFixture(51)
	analyzer := &recordingTitleAnalyzer{respond: func(requests []models.TitleRequest) (map[int]string, error) {
		return map[int]string{requests[0].Index: "Cleaned: " + requests[0].Title}, nil
	}}
	p := newTestProcessor(store, notifications, &mockScraper{deals: []models.DealInfo{first, second}})
	p.aiClient = analyzer
	p.config.DiscordAppID = "200"
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCleanedTitle(t, store.deals[generateDealID(first.PublishedTimestamp)], first)
	missing := store.deals[generateDealID(second.PublishedTimestamp)]
	if missing == nil || missing.Title != second.Title || missing.CleanTitle != "" || missing.AIProcessed || missing.DiscordMessageIDs["channel1"] == "" {
		t.Fatalf("missing AI result lost source data, receipt, or retry eligibility: %+v", missing)
	}
	analyzer.respond = nil
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertCleanedTitle(t, store.deals[generateDealID(second.PublishedTimestamp)], second)
	if len(analyzer.batches) != 2 || len(analyzer.batches[1]) != 1 || analyzer.batches[1][0].Title != second.Title || len(notifications.sentDeals) != 2 {
		t.Fatalf("partial retry repeated completed work or alerts: batches=%+v sends=%d", analyzer.batches, len(notifications.sentDeals))
	}
}

func TestTitleCleaningTimeoutStillPersistsAndNotifiesWithinPoll(t *testing.T) {
	store := &noAISubscriptionStore{newMockStore()}
	notifications := newMockNotifier()
	analyzer := &waitingTitleAnalyzer{}
	scraper := &mockScraper{}
	for index := range titleBatchSize + 2 {
		scraper.deals = append(scraper.deals, titleFixture(index+60))
	}
	// This existing title comes after the hanging batch. Preserve its cleaned
	// output and foreign receipt while the other new deals fall back to raw.
	previous := titleFixture(80)
	previous.DocumentID = generateDealID(previous.PublishedTimestamp)
	previous.CleanTitle = "Previously cleaned product"
	previous.AIProcessed = true
	previous.DiscordMessageIDs = map[string]string{"channel1": "300"}
	previous.DiscordMessageApplicationIDs = map[string]string{"channel1": "100"}
	store.deals[previous.DocumentID] = &previous
	scrapedPrevious := titleFixture(80)
	scraper.deals = append(scraper.deals, scrapedPrevious)
	p := newTestProcessor(store, notifications, scraper)
	p.aiClient = analyzer
	p.config.DiscordAppID = "200"
	p.titleCleanupTimeout = 10 * time.Millisecond
	pollCtx, cancelPoll := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelPoll()
	if err := p.ProcessDeals(pollCtx); err != nil {
		t.Fatalf("optional cleanup timeout failed the poll: %v", err)
	}
	if pollCtx.Err() != nil {
		t.Fatalf("optional cleanup exhausted the original poll context: %v", pollCtx.Err())
	}
	if analyzer.calls != 1 {
		t.Fatalf("cleanup continued making calls after its deadline: %d", analyzer.calls)
	}
	if len(store.deals) != len(scraper.deals) || len(notifications.sentDeals) != titleBatchSize+2 {
		t.Fatalf("cleanup timeout dropped new deals or alerts: deals=%d sends=%d", len(store.deals), len(notifications.sentDeals))
	}
	for _, input := range scraper.deals[:titleBatchSize+2] {
		got := store.deals[generateDealID(input.PublishedTimestamp)]
		if got == nil || got.Title != input.Title || got.CleanTitle != "" || got.AIProcessed || got.DiscordMessageIDs["channel1"] == "" || got.DiscordMessageApplicationIDs["channel1"] != "200" {
			t.Fatalf("timeout lost source title, retry eligibility, or new receipt: %+v", got)
		}
	}
	for _, sent := range notifications.sentDeals {
		if sent.CleanTitle != "" || sent.AIProcessed {
			t.Fatalf("timeout notification did not fall back to raw title: %+v", sent)
		}
	}
	retained := store.deals[previous.DocumentID]
	if retained.CleanTitle != previous.CleanTitle || !retained.AIProcessed || retained.DiscordMessageIDs["channel1"] != "300" || retained.DiscordMessageApplicationIDs["channel1"] != "100" {
		t.Fatalf("timeout lost existing cleanup or receipt ownership: %+v", retained)
	}
	if len(p.titleQueue) != 0 || len(p.titleQueueDeals) != 0 || !p.titleQueueStart.IsZero() {
		t.Fatal("cleanup timeout retained a stale title batch")
	}
}
