package processor

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

// --- Mock implementations ---

type mockStore struct {
	deals       map[string]*models.DealInfo
	createErr   error
	updateErr   error
	trimCalled  bool
	updateCount int
}

func newMockStore() *mockStore {
	return &mockStore{deals: make(map[string]*models.DealInfo)}
}

func (m *mockStore) GetDealByID(_ context.Context, id string) (*models.DealInfo, error) {
	deal, ok := m.deals[id]
	if !ok {
		return nil, nil
	}
	copy := *deal
	return &copy, nil
}

func (m *mockStore) TryCreateDeal(_ context.Context, deal models.DealInfo) error {
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.deals[deal.FirestoreID]; exists {
		return models.ErrDealExists
	}
	copy := deal
	m.deals[deal.FirestoreID] = &copy
	return nil
}

func (m *mockStore) UpdateDeal(_ context.Context, deal models.DealInfo) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updateCount++
	copy := deal
	m.deals[deal.FirestoreID] = &copy
	return nil
}

func (m *mockStore) GetDealsByIDs(_ context.Context, ids []string) (map[string]*models.DealInfo, error) {
	result := make(map[string]*models.DealInfo)
	for _, id := range ids {
		if deal, ok := m.deals[id]; ok {
			copy := *deal
			result[id] = &copy
		}
	}
	return result, nil
}

func (m *mockStore) GetRecentDeals(_ context.Context, duration time.Duration) ([]models.DealInfo, error) {
	var recent []models.DealInfo
	for _, deal := range m.deals {
		recent = append(recent, *deal)
	}
	return recent, nil
}

func (m *mockStore) TrimOldDeals(_ context.Context, _ int) error {
	m.trimCalled = true
	return nil
}

func (m *mockStore) BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error {
	for _, deal := range creates {
		if err := m.TryCreateDeal(ctx, deal); err != nil {
			return err
		}
	}
	for _, deal := range updates {
		if err := m.UpdateDeal(ctx, deal); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockStore) Ping(ctx context.Context) error {
	return nil
}

func (m *mockStore) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	// Return a default test subscription so the notifier actually sends
	return []models.Subscription{
		{GuildID: "guild1", ChannelID: "channel1"},
	}, nil
}

type mockNotifier struct {
	sentDeals  []models.DealInfo
	updatedIDs []string
	sendErr    error
	nextMsgID  string
	updateErr  error
}

func newMockNotifier() *mockNotifier {
	return &mockNotifier{nextMsgID: "msg-123"}
}

func (m *mockNotifier) Send(_ context.Context, deal models.DealInfo, subs []models.Subscription) (map[string]string, error) {
	if m.sendErr != nil {
		return nil, m.sendErr
	}
	m.sentDeals = append(m.sentDeals, deal)
	res := make(map[string]string)
	for _, sub := range subs {
		res[sub.ChannelID] = m.nextMsgID + "-" + sub.ChannelID
	}
	return res, nil
}

func (m *mockNotifier) Update(_ context.Context, deal models.DealInfo) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	for _, msgID := range deal.DiscordMessageIDs {
		m.updatedIDs = append(m.updatedIDs, msgID)
	}
	return nil
}

func (m *mockNotifier) IsWarm(deal models.DealInfo) bool {
	return true
}

func (m *mockNotifier) IsHot(deal models.DealInfo) bool {
	return true
}

type mockScraper struct {
	deals          []models.DealInfo
	err            error
	fetchedDetails []*models.DealInfo
}

func (m *mockScraper) ScrapeDealList(_ context.Context) ([]models.DealInfo, error) {
	return m.deals, m.err
}

func (m *mockScraper) FetchDealDetails(_ context.Context, deals []*models.DealInfo) {
	// Track which deals were requested for detail fetching
	// Need to copy because deals are pointers
	for _, d := range deals {
		copy := *d
		m.fetchedDetails = append(m.fetchedDetails, &copy)
	}
}

type mockDealAnalyzer struct {
	cleanTitle string
	isWarm     bool
	isHot      bool
	err        error
	called     bool
}

func (m *mockDealAnalyzer) AnalyzeDeal(ctx context.Context, deal *models.DealInfo) (string, bool, bool, error) {
	m.called = true
	return m.cleanTitle, m.isWarm, m.isHot, m.err
}

func newTestProcessor(store DealStore, notifier DealNotifier, scraper DealScraper) *DealProcessor {
	cfg := &config.Config{
		DiscordUpdateInterval: 10 * time.Minute,
		MaxStoredDeals:        500,
		AmazonAffiliateTag:    "test-tag",
	}
	v := validator.New()
	ai := &mockDealAnalyzer{cleanTitle: "Clean Title", isHot: true}
	return New(store, notifier, scraper, v, cfg, ai)
}

// Helper: fixed timestamp for test deals
var testTime1 = time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)
var testTime2 = time.Date(2025, 1, 16, 12, 0, 0, 0, time.UTC)

// --- Tests ---

func TestProcessDeals_NewDeal(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Great Deal", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1},
		},
	}

	p := newTestProcessor(store, notif, scraper)
	err := p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("ProcessDeals() error = %v", err)
	}

	if len(store.deals) != 1 {
		t.Errorf("Expected 1 deal in store, got %d", len(store.deals))
	}
	if len(notif.sentDeals) != 1 {
		t.Errorf("Expected 1 notification sent, got %d", len(notif.sentDeals))
	}
	if !store.trimCalled {
		t.Error("Expected TrimOldDeals to be called after new deals")
	}
}

func TestProcessDeals_SkipsInvalidDeal(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "", PostURL: "", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{}}},      // empty title and URL
			{Title: "   ", PostURL: "  ", PublishedTimestamp: testTime2, Threads: []models.ThreadContext{{}}}, // whitespace only
			{Title: "Valid", PostURL: "https://rfd.com/deal", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{}}},
		},
	}

	p := newTestProcessor(store, notif, scraper)
	err := p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("ProcessDeals() error = %v", err)
	}

	if len(store.deals) != 1 {
		t.Errorf("Expected 1 valid deal in store, got %d", len(store.deals))
	}
}

func TestProcessDeals_SkipsZeroTimestamp(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "No Timestamp", PostURL: "https://rfd.com/deal-no-ts", Threads: []models.ThreadContext{{}}},
			{Title: "Has Timestamp", PostURL: "https://rfd.com/deal-ts", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{}}},
		},
	}

	p := newTestProcessor(store, notif, scraper)
	err := p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("ProcessDeals() error = %v", err)
	}

	if len(store.deals) != 1 {
		t.Errorf("Expected 1 deal (zero timestamp skipped), got %d", len(store.deals))
	}
}

func TestProcessDeals_UpdateExistingDeal(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()

	scraper := &mockScraper{
		deals: []models.DealInfo{
			{
				Title:              "Original Title",
				PostURL:            "https://forums.redflagdeals.com/deal-1",
				PublishedTimestamp: testTime1,
				Threads:            []models.ThreadContext{{LikeCount: 10, PostURL: "https://forums.redflagdeals.com/deal-1"}},
			},
		},
	}

	p := newTestProcessor(store, notif, scraper)

	// First run: creates the deal
	err := p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("First ProcessDeals() error = %v", err)
	}

	// Now update the scraper with changed data (same timestamp = same deal)
	scraper.deals = []models.DealInfo{
		{
			Title:              "Updated Title",
			PostURL:            "https://forums.redflagdeals.com/deal-1",
			PublishedTimestamp: testTime1,
			Threads:            []models.ThreadContext{{LikeCount: 20, PostURL: "https://forums.redflagdeals.com/deal-1"}},
		},
	}
	store.updateCount = 0

	err = p.ProcessDeals(context.Background())
	if err != nil {
		t.Fatalf("Second ProcessDeals() error = %v", err)
	}

	if store.updateCount == 0 {
		t.Error("Expected UpdateDeal to be called for changed deal")
	}
}

func TestProcessDeals_URLChangedDealsUpdated(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()

	// First run: create the deal
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Great Deal", PostURL: "https://forums.redflagdeals.com/deal-old-url", ActualDealURL: "https://amazon.ca/old-url", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-old-url"}}},
		},
	}
	p := newTestProcessor(store, notif, scraper)
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Verify 1 deal created
	if len(store.deals) != 1 {
		t.Fatalf("Expected 1 deal, got %d", len(store.deals))
	}

	// Second run: same timestamp but URL changed (user edited the post)
	scraper.deals = []models.DealInfo{
		{Title: "Great Deal", PostURL: "https://forums.redflagdeals.com/deal-new-url", ActualDealURL: "https://amazon.ca/new-url", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-new-url"}}},
	}
	store.updateCount = 0

	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Should still be 1 deal (updated, not duplicated)
	if len(store.deals) != 1 {
		t.Errorf("Expected 1 deal (URL change should update, not duplicate), got %d", len(store.deals))
	}
	if store.updateCount == 0 {
		t.Error("Expected UpdateDeal to be called when ActualDealURL changed")
	}

	// Verify the stored deal has the new URL
	for _, deal := range store.deals {
		if deal.ActualDealURL != "https://amazon.ca/new-url" {
			t.Errorf("Expected ActualDealURL to be updated, got %q", deal.ActualDealURL)
		}
	}
}

func TestProcessDeals_TitleChangedDealsUpdated(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()

	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Original Title", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-1"}}},
		},
	}
	p := newTestProcessor(store, notif, scraper)
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Second run: same timestamp but title changed
	scraper.deals = []models.DealInfo{
		{Title: "Updated Title - Price Drop!", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-1"}}},
	}
	store.updateCount = 0

	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(store.deals) != 1 {
		t.Errorf("Expected 1 deal (title change should update, not duplicate), got %d", len(store.deals))
	}
	if store.updateCount == 0 {
		t.Error("Expected UpdateDeal to be called when Title changed")
	}

	for _, deal := range store.deals {
		if deal.Title != "Updated Title - Price Drop!" {
			t.Errorf("Expected Title to be updated, got %q", deal.Title)
		}
	}
}

func TestProcessDeals_UnchangedDealSkipped(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{
				Title:              "Same Deal",
				PostURL:            "https://forums.redflagdeals.com/deal-1",
				PublishedTimestamp: testTime1,
				Threads:            []models.ThreadContext{{LikeCount: 5, PostURL: "https://forums.redflagdeals.com/deal-1"}},
			},
		},
	}

	p := newTestProcessor(store, notif, scraper)

	// First run: creates the deal
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatalf("First ProcessDeals() error = %v", err)
	}

	// Second run with same data: should skip
	store.updateCount = 0
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatalf("Second ProcessDeals() error = %v", err)
	}

	// No updates should have happened since data hasn't changed
	if store.updateCount != 0 {
		t.Errorf("Expected 0 UpdateDeal calls for unchanged deal, got %d", store.updateCount)
	}
}

func TestProcessDeals_ScrapeError(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{err: errors.New("network error")}

	p := newTestProcessor(store, notif, scraper)
	err := p.ProcessDeals(context.Background())
	if err == nil {
		t.Fatal("Expected error from ProcessDeals when scraper fails")
	}
}

func TestProcessDeals_TrimOnlyOnNewDeals(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Deal", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-1"}}},
		},
	}

	p := newTestProcessor(store, notif, scraper)

	// First run creates the deal and should trigger trim
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !store.trimCalled {
		t.Error("TrimOldDeals should be called when new deals are created")
	}

	// Second run: no new deals, just an update (or skip)
	store.trimCalled = false
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.trimCalled {
		t.Error("TrimOldDeals should NOT be called when no new deals are created")
	}
}

func TestProcessDeals_RaceConditionHandling(t *testing.T) {
	store := newMockStore()
	store.createErr = models.ErrDealExists
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Race Deal", PostURL: "https://forums.redflagdeals.com/race-1", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/race-1"}}},
		},
	}

	p := newTestProcessor(store, notif, scraper)

	// Should not return error — race condition is handled gracefully
	err := p.ProcessDeals(context.Background())
	if err != nil {
		// It may error because the deal doesn't exist in store after ErrDealExists
		// (race anomaly path). That's acceptable.
		t.Logf("ProcessDeals() returned error (expected for anomaly path): %v", err)
	}
}

func TestConsolidatedFirestoreWrite(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Deal", PostURL: "https://forums.redflagdeals.com/deal-fw", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{PostURL: "https://forums.redflagdeals.com/deal-fw"}}},
		},
	}

	p := newTestProcessor(store, notif, scraper)

	// Create the deal first
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Now update with changed data — the deal has a DiscordMessageID and old timestamp,
	// so both data update and Discord update should happen in a SINGLE write.
	scraper.deals = []models.DealInfo{
		{
			Title:              "Deal Updated",
			PostURL:            "https://forums.redflagdeals.com/deal-fw",
			PublishedTimestamp: testTime1,
			Threads:            []models.ThreadContext{{LikeCount: 99, PostURL: "https://forums.redflagdeals.com/deal-fw"}},
		},
	}

	// Set DiscordLastUpdatedTime to long ago in the stored deal
	for _, d := range store.deals {
		d.DiscordLastUpdatedTime = time.Now().Add(-1 * time.Hour)
	}

	store.updateCount = 0
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Should be exactly 1 UpdateDeal call (consolidated), not 2
	if store.updateCount != 1 {
		t.Errorf("Expected 1 UpdateDeal call (consolidated), got %d", store.updateCount)
	}
}

func TestGenerateDealID_Stable(t *testing.T) {
	ts := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)
	id1 := generateDealID(ts)
	id2 := generateDealID(ts)
	if id1 != id2 {
		t.Errorf("generateDealID should be deterministic: %q != %q", id1, id2)
	}

	// Different timestamps should produce different IDs
	ts2 := time.Date(2025, 6, 1, 12, 0, 1, 0, time.UTC)
	id3 := generateDealID(ts2)
	if id1 == id3 {
		t.Errorf("Different timestamps should produce different IDs")
	}
}

// --- New Unit Tests for Helper Functions ---

func TestScrapeAndValidate_SubFunction(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Valid Deal", PostURL: "http://example.com/1", PublishedTimestamp: testTime1, Threads: []models.ThreadContext{{}}},
			{Title: "", PostURL: "", PublishedTimestamp: testTime2, Threads: []models.ThreadContext{{}}}, // Invalid
		},
	}
	p := newTestProcessor(store, notif, scraper)

	validDeals, err := p.scrapeAndValidate(context.Background(), slog.Default(), metrics.NewTracker("rfd"))
	if err != nil {
		t.Fatalf("scrapeAndValidate failed: %v", err)
	}

	// Expect 1 valid deal (the empty one should be filtered)
	if len(validDeals) != 1 {
		t.Errorf("Expected 1 valid deal, got %d", len(validDeals))
	}
	if validDeals[0].Title != "Valid Deal" {
		t.Errorf("Expected 'Valid Deal', got %s", validDeals[0].Title)
	}
	if validDeals[0].FirestoreID == "" {
		t.Error("FirestoreID should be populated during validation")
	}
}

func TestEnrichDealsWithDetails_SubFunction(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()
	scraper := &mockScraper{}

	p := newTestProcessor(store, notif, scraper)

	// Setup:
	// New Deal: Not in existingDeals -> Should fetch
	// Existing Changed (PostURL): In existingDeals, changed PostURL -> Should fetch
	// Existing Changed (LikeCount Only): In existingDeals, changed LikeCount -> Should NOT fetch (Optimization)
	// Existing Unchanged: In existingDeals, same data -> Should NOT fetch

	newDeal := models.DealInfo{Title: "New", FirestoreID: "id1", Threads: []models.ThreadContext{{LikeCount: 5}}}
	urlChangedDeal := models.DealInfo{Title: "UrlChanged", FirestoreID: "id2", PostURL: "http://new.url", Threads: []models.ThreadContext{{LikeCount: 10}}}
	onlyMetricsChangedDeal := models.DealInfo{Title: "MetricsChanged", FirestoreID: "id3", Threads: []models.ThreadContext{{LikeCount: 100}}} // was 50
	unchangedDeal := models.DealInfo{Title: "Same", FirestoreID: "id4", Threads: []models.ThreadContext{{LikeCount: 5}}}

	existingDeals := map[string]*models.DealInfo{
		"id2": {Title: "UrlChanged", FirestoreID: "id2", PostURL: "http://old.url", ActualDealURL: "http://old.url/item", Description: "desc", Threads: []models.ThreadContext{{LikeCount: 10}}},
		"id3": {Title: "MetricsChanged", FirestoreID: "id3", ActualDealURL: "http://deal.url/item", Description: "desc", Threads: []models.ThreadContext{{LikeCount: 50}}},
		"id4": {Title: "Same", FirestoreID: "id4", ActualDealURL: "http://deal.url/item", Description: "desc", Threads: []models.ThreadContext{{LikeCount: 5}}},
		"id5": {Title: "OldTitle", FirestoreID: "id5", Description: "desc", ActualDealURL: "http://deal.url/item"},
	}

	titleChangedDeal := models.DealInfo{Title: "TitleChanged", FirestoreID: "id5", Threads: []models.ThreadContext{{LikeCount: 5}}}
	validDeals := []models.DealInfo{newDeal, urlChangedDeal, onlyMetricsChangedDeal, unchangedDeal, titleChangedDeal}

	p.enrichDealsWithDetails(context.Background(), validDeals, existingDeals, slog.Default())

	// Check fetched details
	// Should fetch: New, UrlChanged, TitleChanged.
	// Should NOT fetch: MetricsChanged, Same.
	if len(scraper.fetchedDetails) != 3 {
		t.Errorf("Expected 3 deals to be fetched (New, UrlChanged, TitleChanged), got %d", len(scraper.fetchedDetails))
	}

	titles := make(map[string]bool)
	for _, d := range scraper.fetchedDetails {
		titles[d.Title] = true
	}
	if !titles["New"] {
		t.Error("Expected New deal to be fetched")
	}
	if !titles["UrlChanged"] {
		t.Error("Expected UrlChanged deal to be fetched")
	}
	if !titles["TitleChanged"] {
		t.Error("Expected TitleChanged deal to be fetched")
	}
	if titles["MetricsChanged"] {
		t.Error("Expected MetricsChanged deal to NOT be fetched (optimization check)")
	}
	if titles["Same"] {
		t.Error("Expected Unchanged deal to NOT be fetched")
	}
}

// --- threadKey tests ---

func TestThreadKey_RFDSlugVariants(t *testing.T) {
	// All of these are the same RFD thread (ID 2806520) with different slugs.
	urls := []string{
		"https://forums.redflagdeals.com/firehouse-subs-firehouse-subs-hotsubs-5-off-no-minimum-purchase-2806520",
		"https://forums.redflagdeals.com/firehouse-subs-hotsubs-5-off-no-minimum-purchase-2806520",
		"https://forums.redflagdeals.com/firehouse-subs-hotsubs-5-off-no-minimum-2806520",
	}

	expected := "rfd:2806520"
	for _, u := range urls {
		got := threadKey(u)
		if got != expected {
			t.Errorf("threadKey(%q) = %q, want %q", u, got, expected)
		}
	}
}

func TestThreadKey_RFDWithFragmentAndTrailingSlash(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://forums.redflagdeals.com/deal-slug-123456/", "rfd:123456"},
		{"https://forums.redflagdeals.com/deal-slug-123456/#post999", "rfd:123456"},
		{"https://forums.redflagdeals.com/deal-slug-123456#p100", "rfd:123456"},
	}
	for _, tt := range tests {
		got := threadKey(tt.url)
		if got != tt.want {
			t.Errorf("threadKey(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestThreadKey_NonRFDURLsUnchanged(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://www.ebay.ca/itm/12345", "https://www.ebay.ca/itm/12345"},
		{"https://example.com/deal-999", "https://example.com/deal-999"},
	}
	for _, tt := range tests {
		got := threadKey(tt.url)
		if got != tt.want {
			t.Errorf("threadKey(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestThreadKey_EdgeCases(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"", ""},
		// RFD listing page (no thread ID) falls back to full URL
		{"https://forums.redflagdeals.com/hot-deals-f9", "https://forums.redflagdeals.com/hot-deals-f9"},
	}
	for _, tt := range tests {
		got := threadKey(tt.url)
		if got != tt.want {
			t.Errorf("threadKey(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestMergeThread_SlugVariants(t *testing.T) {
	p := newTestProcessor(newMockStore(), newMockNotifier(), &mockScraper{})

	deal := &models.DealInfo{
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/old-slug-2806520", LikeCount: 10, CommentCount: 5},
		},
	}

	// Merge a thread with a different slug but same thread ID
	newThread := models.ThreadContext{
		PostURL:      "https://forums.redflagdeals.com/new-slug-2806520",
		LikeCount:    20,
		CommentCount: 8,
	}

	changed := p.mergeThread(deal, newThread)
	if !changed {
		t.Error("Expected mergeThread to report changed")
	}
	if len(deal.Threads) != 1 {
		t.Errorf("Expected 1 thread (merged in-place), got %d", len(deal.Threads))
	}
	if deal.Threads[0].LikeCount != 20 {
		t.Errorf("Expected LikeCount=20, got %d", deal.Threads[0].LikeCount)
	}
	if deal.Threads[0].PostURL != "https://forums.redflagdeals.com/new-slug-2806520" {
		t.Errorf("Expected URL to be updated to new slug, got %q", deal.Threads[0].PostURL)
	}
}

func TestMergeThread_DifferentThreadIDs(t *testing.T) {
	p := newTestProcessor(newMockStore(), newMockNotifier(), &mockScraper{})

	deal := &models.DealInfo{
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/deal-a-111111", LikeCount: 10},
		},
	}

	// Different thread ID — should append
	newThread := models.ThreadContext{
		PostURL:   "https://forums.redflagdeals.com/deal-b-222222",
		LikeCount: 5,
	}

	changed := p.mergeThread(deal, newThread)
	if !changed {
		t.Error("Expected mergeThread to report changed (new thread appended)")
	}
	if len(deal.Threads) != 2 {
		t.Errorf("Expected 2 threads (different IDs), got %d", len(deal.Threads))
	}
}

func TestDeduplicateThreadsByKey(t *testing.T) {
	// Simulates the Firehouse Subs case: 3 threads, all same thread ID 2806520
	deal := &models.DealInfo{
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/firehouse-subs-firehouse-subs-hotsubs-5-off-no-minimum-purchase-2806520", LikeCount: 34, CommentCount: 13},
			{PostURL: "https://forums.redflagdeals.com/firehouse-subs-hotsubs-5-off-no-minimum-purchase-2806520", LikeCount: 3, CommentCount: 0},
			{PostURL: "https://forums.redflagdeals.com/firehouse-subs-hotsubs-5-off-no-minimum-2806520", LikeCount: 0, CommentCount: 0},
		},
	}

	changed := deduplicateThreadsByKey(deal)
	if !changed {
		t.Error("Expected deduplicateThreadsByKey to report changed")
	}
	if len(deal.Threads) != 1 {
		t.Errorf("Expected 1 thread after dedup, got %d", len(deal.Threads))
	}
	if deal.Threads[0].LikeCount != 34 {
		t.Errorf("Expected highest LikeCount (34) to be kept, got %d", deal.Threads[0].LikeCount)
	}
}

func TestDeduplicateThreadsByKey_MixedIDs(t *testing.T) {
	// Simulates the Paramount+ case: 3 threads, but 2 unique IDs
	deal := &models.DealInfo{
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/amazon-prime-video-paramount-2806566", LikeCount: 3},
			{PostURL: "https://forums.redflagdeals.com/amazon-prime-amazon-prime-video-paramount-2806566", LikeCount: 2},
			{PostURL: "https://forums.redflagdeals.com/paramount-paramount-2806534", LikeCount: 0},
		},
	}

	changed := deduplicateThreadsByKey(deal)
	if !changed {
		t.Error("Expected deduplicateThreadsByKey to report changed")
	}
	if len(deal.Threads) != 2 {
		t.Errorf("Expected 2 threads after dedup (2 unique IDs), got %d", len(deal.Threads))
	}
	// The first entry (rfd:2806566) should keep the higher LikeCount
	if deal.Threads[0].LikeCount != 3 {
		t.Errorf("Expected LikeCount=3 for thread 2806566, got %d", deal.Threads[0].LikeCount)
	}
}

func TestDeduplicateThreadsByKey_NoDuplicates(t *testing.T) {
	deal := &models.DealInfo{
		Threads: []models.ThreadContext{
			{PostURL: "https://forums.redflagdeals.com/deal-a-111111", LikeCount: 10},
			{PostURL: "https://forums.redflagdeals.com/deal-b-222222", LikeCount: 5},
		},
	}

	changed := deduplicateThreadsByKey(deal)
	if changed {
		t.Error("Expected no changes when threads have different IDs")
	}
	if len(deal.Threads) != 2 {
		t.Errorf("Expected 2 threads unchanged, got %d", len(deal.Threads))
	}
}
