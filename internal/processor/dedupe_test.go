package processor

import (
	"context"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestExtractWords(t *testing.T) {
	words := extractWords("250GB 5G+ $40/mo. w/ Digital Discount and $40 ongoing credit")
	expected := []string{"250gb", "5g", "40", "mo", "w", "digital", "discount", "and", "40", "ongoing", "credit"}

	if len(words) != len(expected) {
		t.Fatalf("Expected %d words, got %d. Words: %v", len(expected), len(words), words)
	}

	for i, w := range expected {
		if words[i] != w {
			t.Errorf("Word at index %d mismatch. Expected %s, got %s", i, w, words[i])
		}
	}
}

func TestGenerateSearchTokens(t *testing.T) {
	// A typical Freedom mobile deal
	deal := &models.DealInfo{
		Title:         "Freedom Mobile 250GB 5G+ $40/mo.",
		CleanTitle:    "250GB 5G+ $40/mo. w/ Digital Discount",
		ActualDealURL: "https://shop.freedommobile.ca/en-CA/plans?planSku=Freedom%2050GB",
	}

	tokens := GenerateSearchTokens(deal)

	// Valuables expected from clean title: "250gb", "5g", "40", "digital"
	// Valuables expected from actual url: "plans", "planSku" -> "plansku", "Freedom", "50gb"
	// Wait, the url tokenizer gives ["plans", "planSku", "Freedom", "50GB"]
	// Lowercase: plans, plansku, freedom, 50gb
	// Let's just check for specific important tokens being present.

	expectedImportant := []string{"250gb", "5g", "40", "digital", "plans", "plansku", "freedom", "50gb"}

	for _, req := range expectedImportant {
		if !slices.Contains(tokens, req) {
			t.Errorf("Expected token %q to be in search tokens: %v", req, tokens)
		}
	}

	// Ensure common words are filtered out (including newly-expanded stopwords)
	notExpected := []string{"w", "and", "mo", "the", "with", "discount"}
	for _, ne := range notExpected {
		if slices.Contains(tokens, ne) {
			t.Errorf("Did not expect token %q to be in search tokens: %v", ne, tokens)
		}
	}

	// Ensure no duplicate tokens exist
	seen := make(map[string]bool)
	for _, tok := range tokens {
		if seen[tok] {
			t.Errorf("Duplicate token %q found in search tokens: %v", tok, tokens)
		}
		seen[tok] = true
	}
}

func TestCalculateSimilarity(t *testing.T) {
	// Perfect match
	sim := calculateSimilarity([]string{"a", "b", "c"}, []string{"c", "b", "a"})
	if sim != 1.0 {
		t.Errorf("Expected similarity 1.0, got %f", sim)
	}

	// Subset match (should be 1.0 because intersection / min_len is 2 / 2 = 1.0)
	sim = calculateSimilarity([]string{"freedom", "50gb", "40", "bonus", "roam"}, []string{"freedom", "50gb", "40"})
	if sim != 1.0 {
		t.Errorf("Expected similarity 1.0 for subset, got %f", sim)
	}

	// Partial match (2 out of 3 = 0.66)
	sim = calculateSimilarity([]string{"telus", "50gb", "40"}, []string{"freedom", "50gb", "40"})
	if sim < 0.66 || sim > 0.67 {
		t.Errorf("Expected similarity ~0.66, got %f", sim)
	}

	// No match
	sim = calculateSimilarity([]string{"a", "b"}, []string{"x", "y", "z"})
	if sim != 0.0 {
		t.Errorf("Expected similarity 0.0, got %f", sim)
	}
}

func TestDeduplicateDeals_ScrapedWithExisting(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	p := &DealProcessor{} // minimal struct for testing this func

	existingDeals := make(map[string]*models.DealInfo)

	recentDeals := []models.DealInfo{
		{
			FirestoreID:   "existing-1",
			Title:         "Samsung Galaxy S24 Ultra 512GB - $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			SearchTokens:  []string{"samsung", "galaxy", "s24", "ultra", "512gb", "1099"},
		},
	}

	scrapedDeals := []models.DealInfo{
		{
			FirestoreID:   "scraped-new-id-1",
			Title:         "[Samsung] Galaxy S24 Ultra 512gb (Price Drop)",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "url1"}},
		},
		{
			FirestoreID:   "scraped-new-id-2", // different URL, but very similar title
			Title:         "Galaxy S24 Ultra 512GB - $1099 (SPC/Perkopolis)",
			ActualDealURL: "",
			Threads:       []models.ThreadContext{{PostURL: "url2"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, recentDeals, logger)

	// Since both scraped deals match the recent deal (first by exact URL, second by fuzzy title),
	// they should both be mapped to the existing deal ID.
	if len(deduped) != 2 {
		t.Fatalf("Expected 2 deduplicated deals (mapped), got %d", len(deduped))
	}

	if deduped[0].FirestoreID != "existing-1" {
		t.Errorf("Expected first scraped deal to map to existing-1, got %s", deduped[0].FirestoreID)
	}

	if deduped[1].FirestoreID != "existing-1" {
		t.Errorf("Expected second scraped deal to map to existing-1, got %s", deduped[1].FirestoreID)
	}
}

func TestDeduplicateDeals_ScrapedWithScraped(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := &DealProcessor{}
	existingDeals := make(map[string]*models.DealInfo)

	scrapedDeals := []models.DealInfo{
		{
			FirestoreID:   "id-1",
			Title:         "250GB 5G+ $40/mo. w/ Digital Discount and $40 ongoing credit",
			ActualDealURL: "https://shop.freedommobile.ca/en-CA/plans",
			Threads:       []models.ThreadContext{{PostURL: "thread-1"}},
		},
		{
			FirestoreID:   "id-2",
			Title:         "Freedom Mobile $40/mo 250GB 5G+ Canada/US/Mexico With Roam Beyond",
			ActualDealURL: "https://shop.freedommobile.ca/en-CA/plans",
			Threads:       []models.ThreadContext{{PostURL: "thread-2"}},
		},
		{
			FirestoreID:   "id-3", // completely different deal
			Title:         "Apple AirPods Pro 2 - $249",
			ActualDealURL: "https://amazon.ca/airpods",
			Threads:       []models.ThreadContext{{PostURL: "thread-3"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, nil, logger)

	// Expect 3 deals total, but first two should share the same FirestoreID!
	if len(deduped) != 3 {
		t.Fatalf("Expected 3 valid deduplicated items, got %d", len(deduped))
	}

	if deduped[0].FirestoreID != deduped[1].FirestoreID {
		t.Errorf("Expected deal 1 and deal 2 to merge and share ID. IDs: %s, %s", deduped[0].FirestoreID, deduped[1].FirestoreID)
	}

	if deduped[1].FirestoreID == deduped[2].FirestoreID {
		t.Errorf("Expected deal 3 to have different ID.")
	}
}

func TestGenerateSearchTokens_NoDuplicates(t *testing.T) {
	deal := &models.DealInfo{
		Title: "$40/mo Plan + $40 Ongoing Credit - Best Deal",
	}

	tokens := GenerateSearchTokens(deal)

	// Count occurrences of "40" — should be exactly 1
	count := 0
	for _, tok := range tokens {
		if tok == "40" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("Expected token '40' to appear exactly once, appeared %d times in %v", count, tokens)
	}

	// "best" should be filtered as a stopword
	if slices.Contains(tokens, "best") {
		t.Errorf("Expected 'best' to be filtered as a stopword, got %v", tokens)
	}
}

func TestGenerateSearchTokens_URLDomainNoise(t *testing.T) {
	deal := &models.DealInfo{
		Title:         "AirPods Pro",
		ActualDealURL: "https://www.amazon.ca/dp/B0D1XD1ZV3/ref=cm_sw_r_cp_api",
	}

	tokens := GenerateSearchTokens(deal)

	// "www", "ca", "ref", "cm" should be stripped as URL/TLD noise
	noiseTokens := []string{"www", "com", "ca", "html", "htm"}
	for _, noise := range noiseTokens {
		if slices.Contains(tokens, noise) {
			t.Errorf("URL noise token %q should be filtered, got tokens: %v", noise, tokens)
		}
	}

	// "amazon" should be KEPT (it's a valuable retailer discriminator)
	if !slices.Contains(tokens, "amazon") {
		t.Errorf("Expected retailer name 'amazon' to be kept, got tokens: %v", tokens)
	}
}

func TestDeduplicateDeals_Layer1_ExactIDSkipsSilently(t *testing.T) {
	// Layer 1: When a scraped deal's FirestoreID already exists in existingDeals,
	// it should be passed through without fuzzy matching or logging "deduplicated".
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := &DealProcessor{}

	// Simulate deals already in Firestore (loaded by loadExistingDeals)
	existingDeals := map[string]*models.DealInfo{
		"known-id-1": {
			FirestoreID:   "known-id-1",
			Title:         "Samsung Galaxy S24 Ultra 512GB - $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			SearchTokens:  []string{"samsung", "galaxy", "s24", "ultra", "512gb", "1099"},
		},
		"known-id-2": {
			FirestoreID:   "known-id-2",
			Title:         "Apple AirPods Pro 2 - $249",
			ActualDealURL: "https://amazon.ca/airpods",
			SearchTokens:  []string{"apple", "airpods", "pro", "249"},
		},
	}

	// Scraped deals have the same FirestoreIDs (same PublishedTimestamp = same posts)
	scrapedDeals := []models.DealInfo{
		{
			FirestoreID:   "known-id-1",
			Title:         "Samsung Galaxy S24 Ultra 512GB - $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-1"}},
		},
		{
			FirestoreID:   "known-id-2",
			Title:         "Apple AirPods Pro 2 - $249",
			ActualDealURL: "https://amazon.ca/airpods",
			Threads:       []models.ThreadContext{{PostURL: "thread-2"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, nil, logger)

	// Both should pass through, keeping their original FirestoreIDs
	if len(deduped) != 2 {
		t.Fatalf("Expected 2 deals, got %d", len(deduped))
	}
	if deduped[0].FirestoreID != "known-id-1" {
		t.Errorf("Expected known-id-1, got %s", deduped[0].FirestoreID)
	}
	if deduped[1].FirestoreID != "known-id-2" {
		t.Errorf("Expected known-id-2, got %s", deduped[1].FirestoreID)
	}

	// SearchTokens should NOT be generated (Layer 1 skips before token generation)
	if len(deduped[0].SearchTokens) > 0 {
		t.Errorf("Layer 1 should skip token generation, but tokens were set: %v", deduped[0].SearchTokens)
	}
}

func TestDeduplicateDeals_Layer2_FuzzyMatchForDifferentPosts(t *testing.T) {
	// Layer 2: When a scraped deal has a NEW FirestoreID (not in existingDeals),
	// but fuzzy-matches a recent deal, it should be remapped to the existing ID.
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := &DealProcessor{}

	existingDeals := make(map[string]*models.DealInfo)

	recentDeals := []models.DealInfo{
		{
			FirestoreID:   "original-post-id",
			Title:         "Samsung Galaxy S24 Ultra 512GB - $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			SearchTokens:  []string{"samsung", "galaxy", "s24", "ultra", "512gb", "1099"},
		},
	}

	// Different user posted a very similar deal (different timestamp = different FirestoreID)
	scrapedDeals := []models.DealInfo{
		{
			FirestoreID:   "new-post-different-timestamp",
			Title:         "[Samsung] Galaxy S24 Ultra 512GB Price Drop $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-new"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, recentDeals, logger)

	if len(deduped) != 1 {
		t.Fatalf("Expected 1 deal, got %d", len(deduped))
	}
	// Should be remapped to the original post's ID
	if deduped[0].FirestoreID != "original-post-id" {
		t.Errorf("Expected FirestoreID to be remapped to original-post-id, got %s", deduped[0].FirestoreID)
	}
}

func TestDeduplicateDeals_MixedLayers(t *testing.T) {
	// Mix of Layer 1 (exact ID match) and Layer 2 (fuzzy match) in same batch
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := &DealProcessor{}

	existingDeals := map[string]*models.DealInfo{
		"known-id": {
			FirestoreID:  "known-id",
			Title:        "AirPods Pro 2 - $249",
			SearchTokens: []string{"airpods", "pro", "249"},
		},
	}

	recentDeals := []models.DealInfo{
		{
			FirestoreID:   "recent-samsung",
			Title:         "Samsung Galaxy S24 Ultra 512GB - $1099",
			ActualDealURL: "https://samsung.com/ca/s24",
			SearchTokens:  []string{"samsung", "galaxy", "s24", "ultra", "512gb", "1099"},
		},
	}

	scrapedDeals := []models.DealInfo{
		{
			// Layer 1: exact ID match, should skip fuzzy matching
			FirestoreID:   "known-id",
			Title:         "AirPods Pro 2 - $249",
			ActualDealURL: "https://amazon.ca/airpods",
			Threads:       []models.ThreadContext{{PostURL: "thread-1"}},
		},
		{
			// Layer 2: new post, should fuzzy match against recentDeals
			FirestoreID:   "brand-new-id",
			Title:         "Samsung Galaxy S24 Ultra 512GB on sale",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-2"}},
		},
		{
			// Brand new deal, no match anywhere
			FirestoreID:   "totally-new",
			Title:         "Costco Kirkland Batteries 48pk $15",
			ActualDealURL: "https://costco.ca/batteries",
			Threads:       []models.ThreadContext{{PostURL: "thread-3"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, recentDeals, logger)

	if len(deduped) != 3 {
		t.Fatalf("Expected 3 deals, got %d", len(deduped))
	}

	// Deal 1: Layer 1 pass-through, keeps original ID
	if deduped[0].FirestoreID != "known-id" {
		t.Errorf("Deal 1 should keep known-id, got %s", deduped[0].FirestoreID)
	}
	// Deal 2: Layer 2 remapped to recent deal
	if deduped[1].FirestoreID != "recent-samsung" {
		t.Errorf("Deal 2 should be remapped to recent-samsung, got %s", deduped[1].FirestoreID)
	}
	// Deal 3: No match, keeps its own ID
	if deduped[2].FirestoreID != "totally-new" {
		t.Errorf("Deal 3 should keep totally-new, got %s", deduped[2].FirestoreID)
	}
}

func TestDeduplicateDeals_ThreeWayMerge(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	p := &DealProcessor{}
	existingDeals := make(map[string]*models.DealInfo)

	// Three deals that are all duplicates of each other (same URL)
	scrapedDeals := []models.DealInfo{
		{
			FirestoreID:   "id-a",
			Title:         "Samsung Galaxy S24 Ultra 512GB",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-a"}},
		},
		{
			FirestoreID:   "id-b",
			Title:         "[Samsung] Galaxy S24 Ultra 512gb - Price Drop",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-b"}},
		},
		{
			FirestoreID:   "id-c",
			Title:         "Galaxy S24 Ultra 512GB SPC Offer",
			ActualDealURL: "https://samsung.com/ca/s24",
			Threads:       []models.ThreadContext{{PostURL: "thread-c"}},
		},
	}

	deduped := p.deduplicateDeals(context.Background(), scrapedDeals, existingDeals, nil, logger)

	// All 3 should appear in output but share the same FirestoreID
	if len(deduped) != 3 {
		t.Fatalf("Expected 3 deduplicated items, got %d", len(deduped))
	}

	sharedID := deduped[0].FirestoreID
	for i, d := range deduped {
		if d.FirestoreID != sharedID {
			t.Errorf("Deal %d has FirestoreID %q, expected all to share %q", i, d.FirestoreID, sharedID)
		}
	}
}
