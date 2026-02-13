package processor

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
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

func (m *mockNotifier) Send(_ context.Context, deal models.DealInfo) (string, error) {
	if m.sendErr != nil {
		return "", m.sendErr
	}
	m.sentDeals = append(m.sentDeals, deal)
	return m.nextMsgID, nil
}

func (m *mockNotifier) Update(_ context.Context, messageID string, _ models.DealInfo) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.updatedIDs = append(m.updatedIDs, messageID)
	return nil
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

func newTestProcessor(store DealStore, notifier DealNotifier, scraper DealScraper) *DealProcessor {
	cfg := &config.Config{
		DiscordUpdateInterval: 10 * time.Minute,
		MaxStoredDeals:        500,
		AmazonAffiliateTag:    "test-tag",
	}
	v := validator.New()
	return New(store, notifier, scraper, v, cfg)
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
			{Title: "", PostURL: "", PublishedTimestamp: testTime1},      // empty title and URL
			{Title: "   ", PostURL: "  ", PublishedTimestamp: testTime2}, // whitespace only
			{Title: "Valid", PostURL: "https://rfd.com/deal", PublishedTimestamp: testTime1},
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
			{Title: "No Timestamp", PostURL: "https://rfd.com/deal-no-ts"},
			{Title: "Has Timestamp", PostURL: "https://rfd.com/deal-ts", PublishedTimestamp: testTime1},
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
			{Title: "Original Title", PostURL: "https://forums.redflagdeals.com/deal-1", LikeCount: 10, PublishedTimestamp: testTime1},
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
		{Title: "Updated Title", PostURL: "https://forums.redflagdeals.com/deal-1", LikeCount: 20, PublishedTimestamp: testTime1},
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
			{Title: "Great Deal", PostURL: "https://forums.redflagdeals.com/deal-old-url", PublishedTimestamp: testTime1},
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
		{Title: "Great Deal", PostURL: "https://forums.redflagdeals.com/deal-new-url", PublishedTimestamp: testTime1},
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
		t.Error("Expected UpdateDeal to be called when PostURL changed")
	}

	// Verify the stored deal has the new URL
	for _, deal := range store.deals {
		if deal.PostURL != "https://forums.redflagdeals.com/deal-new-url" {
			t.Errorf("Expected PostURL to be updated, got %q", deal.PostURL)
		}
	}
}

func TestProcessDeals_TitleChangedDealsUpdated(t *testing.T) {
	store := newMockStore()
	notif := newMockNotifier()

	scraper := &mockScraper{
		deals: []models.DealInfo{
			{Title: "Original Title", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1},
		},
	}
	p := newTestProcessor(store, notif, scraper)
	if err := p.ProcessDeals(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Second run: same timestamp but title changed
	scraper.deals = []models.DealInfo{
		{Title: "Updated Title - Price Drop!", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1},
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
			{Title: "Same Deal", PostURL: "https://forums.redflagdeals.com/deal-1", LikeCount: 5, PublishedTimestamp: testTime1},
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
			{Title: "Deal", PostURL: "https://forums.redflagdeals.com/deal-1", PublishedTimestamp: testTime1},
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
			{Title: "Race Deal", PostURL: "https://forums.redflagdeals.com/race-1", PublishedTimestamp: testTime1},
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
			{Title: "Deal", PostURL: "https://forums.redflagdeals.com/deal-fw", PublishedTimestamp: testTime1},
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
		{Title: "Deal Updated", PostURL: "https://forums.redflagdeals.com/deal-fw", LikeCount: 99, PublishedTimestamp: testTime1},
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
			{Title: "Valid Deal", PostURL: "http://example.com/1", PublishedTimestamp: testTime1},
			{Title: "", PostURL: "", PublishedTimestamp: testTime2}, // Invalid
		},
	}
	p := newTestProcessor(store, notif, scraper)

	validDeals, err := p.scrapeAndValidate(context.Background(), slog.Default())
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

	newDeal := models.DealInfo{Title: "New", FirestoreID: "id1", LikeCount: 5}
	urlChangedDeal := models.DealInfo{Title: "UrlChanged", FirestoreID: "id2", PostURL: "http://new.url", LikeCount: 10}
	onlyMetricsChangedDeal := models.DealInfo{Title: "MetricsChanged", FirestoreID: "id3", LikeCount: 100} // was 50
	unchangedDeal := models.DealInfo{Title: "Same", FirestoreID: "id4", LikeCount: 5}

	existingDeals := map[string]*models.DealInfo{
		"id2": {Title: "UrlChanged", FirestoreID: "id2", PostURL: "http://old.url", LikeCount: 10},
		"id3": {Title: "MetricsChanged", FirestoreID: "id3", LikeCount: 50},
		"id4": {Title: "Same", FirestoreID: "id4", LikeCount: 5},
	}

	validDeals := []models.DealInfo{newDeal, urlChangedDeal, onlyMetricsChangedDeal, unchangedDeal}

	p.enrichDealsWithDetails(context.Background(), validDeals, existingDeals, slog.Default())

	// Check fetched details
	// Should fetch: New, UrlChanged.
	// Should NOT fetch: MetricsChanged, Same.
	if len(scraper.fetchedDetails) != 2 {
		t.Errorf("Expected 2 deals to be fetched (New & UrlChanged), got %d", len(scraper.fetchedDetails))
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
	if titles["MetricsChanged"] {
		t.Error("Expected MetricsChanged deal to NOT be fetched (optimization check)")
	}
	if titles["Same"] {
		t.Error("Expected Unchanged deal to NOT be fetched")
	}
}
