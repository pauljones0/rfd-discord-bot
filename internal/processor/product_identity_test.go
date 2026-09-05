package processor

import (
	"context"
	"log/slog"
	"net/url"
	"reflect"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestProcessDealsKeepsConflictingDetailedProductsSeparate(t *testing.T) {
	for _, againstHistory := range []bool{false, true} {
		name := "same poll"
		if againstHistory {
			name = "recent history"
		}
		t.Run(name, func(t *testing.T) {
			store, notifier := newMockStore(), newMockNotifier()
			a, b := titleFixture(800), titleFixture(801)
			a.Title, b.Title = "Samsung 990 Pro Internal Solid State Drive", "Samsung 990 Pro Internal Solid State Drive"
			a.Retailer, b.Retailer = "Amazon.ca", "Amazon.ca"
			a.ActualDealURL, b.ActualDealURL = "", ""
			urls := map[string]string{
				a.PostURL: "https://www.amazon.ca/dp/B0BHJJ9Y77",
				b.PostURL: "https://www.amazon.ca/dp/B0CHGT1KFJ",
			}
			scraper := &mockScraper{deals: []models.DealInfo{a, b}, mutateDetails: func(deals []*models.DealInfo) {
				for _, deal := range deals {
					deal.ActualDealURL = urls[deal.PostURL]
				}
			}}
			p := newTestProcessor(store, notifier, scraper)
			p.aiClient = nil
			if againstHistory {
				scraper.deals = []models.DealInfo{a}
				if err := p.ProcessDeals(context.Background()); err != nil {
					t.Fatal(err)
				}
				scraper.deals = []models.DealInfo{b}
			}
			if err := p.ProcessDeals(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(store.deals) != 2 || len(notifier.sentDeals) != 2 {
				t.Fatalf("conflicting product links were merged: stored=%d sent=%d", len(store.deals), len(notifier.sentDeals))
			}
		})
	}
}

func TestFuzzyDedupeRespectsRawTitleVariantsWithoutProductLinks(t *testing.T) {
	for _, test := range []struct {
		name, left, right string
		merge             bool
	}{
		{"capacity", "Samsung 990 Pro 2 TB Internal Solid State Drive", "Samsung 990 Pro 4TB Internal Solid State Drive", false},
		{"model", "Samsung Galaxy S24 Ultra 512GB Smartphone", "Samsung Galaxy S25 Ultra 512GB Smartphone", false},
		{"price change", "Samsung Galaxy S24 Ultra 512GB $1099", "Samsung Galaxy S24 Ultra 512GB $999", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			deals := []models.DealInfo{
				{DocumentID: "a", Title: test.left, Retailer: "Samsung"},
				{DocumentID: "b", Title: test.right, Retailer: "Samsung"},
			}
			p := &DealProcessor{}
			result := p.deduplicateDeals(context.Background(), deals, make(map[string]*models.DealInfo), nil, slog.Default())
			if merged := result[0].DocumentID == result[1].DocumentID; merged != test.merge {
				t.Fatalf("merged=%v want=%v for %q / %q", merged, test.merge, test.left, test.right)
			}
		})
	}
}

func TestReconciliationKeepsCanonicalContentAcrossRepeatedDuplicates(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "thread identities"
		if legacy {
			name = "legacy canonical URL"
		}
		t.Run(name, func(t *testing.T) {
			p := newTestProcessor(newMockStore(), newMockNotifier(), &mockScraper{})
			canonical := titleFixture(802)
			canonical.DocumentID = generateDealID(canonical.PublishedTimestamp)
			canonical.Category = "Canonical category"
			canonical.CleanTitle, canonical.AIProcessed = "Cleaned canonical title", true
			canonical.DiscordMessageIDs = map[string]string{"channel": "receipt"}
			canonical.DiscordMessageApplicationIDs = map[string]string{"channel": "imported owner"}
			if !legacy {
				canonical.Threads[0].DocumentID = canonical.DocumentID
			}
			duplicate := titleFixture(803)
			duplicate.DocumentID = canonical.DocumentID
			duplicate.Threads[0].DocumentID = generateDealID(duplicate.PublishedTimestamp)
			duplicate.Threads[0].LikeCount = 200 // Popularity must not change content ownership.
			duplicate.Title, duplicate.Price, duplicate.Category = "Duplicate alternative title", "$40", "Alternative category"
			duplicate.ActualDealURL = canonical.ActualDealURL
			result := &canonical
			for poll := range 3 {
				result = p.reconcileDeal(result, []models.DealInfo{duplicate})
				if result.Title != canonical.Title || result.CleanTitle != canonical.CleanTitle || !result.PublishedTimestamp.Equal(canonical.PublishedTimestamp) || result.Price != canonical.Price || result.Category != canonical.Category || result.PostURL != canonical.PostURL {
					t.Fatalf("poll %d replaced canonical content: %+v", poll, result)
				}
				if !reflect.DeepEqual(result.DiscordMessageIDs, canonical.DiscordMessageIDs) || !reflect.DeepEqual(result.DiscordMessageApplicationIDs, canonical.DiscordMessageApplicationIDs) {
					t.Fatalf("poll %d changed receipt ownership", poll)
				}
			}
			updated := cloneDeal(canonical)
			updated.Title = "Updated canonical title"
			updated.CleanTitle, updated.AIProcessed = "", false
			result = p.reconcileDeal(result, []models.DealInfo{duplicate, updated})
			if result.Title != updated.Title || result.CleanTitle != "" || result.AIProcessed {
				t.Fatalf("canonical edit was lost after grouping a duplicate: %+v", result)
			}
		})
	}
}

func TestNewGroupedDealRetainsAvailableDiscountEvidence(t *testing.T) {
	p := newTestProcessor(newMockStore(), newMockNotifier(), &mockScraper{})
	first, second := titleFixture(804), titleFixture(805)
	first.DocumentID = "canonical"
	first.ActualDealURL, first.Description, first.Price, first.OriginalPrice = "", "", "", ""
	second.DocumentID = first.DocumentID
	second.Summary, second.Savings = "Fixture summary", "Save $50"
	result := p.reconcileDeal(nil, []models.DealInfo{first, second})
	if result.ActualDealURL != second.ActualDealURL || result.Description != second.Description || result.Price != second.Price || result.OriginalPrice != second.OriginalPrice || result.Summary != second.Summary || result.Savings != second.Savings {
		t.Fatalf("successful duplicate details were discarded: %+v", result)
	}
	if result.DocumentID != first.DocumentID || result.Title != first.Title || !result.PublishedTimestamp.Equal(first.PublishedTimestamp) || len(result.Threads) != 2 {
		t.Fatalf("filling missing details changed canonical identity or lost threads: %+v", result)
	}
	if !p.isDealEligibleForSubscription(*result, models.Subscription{DealType: "rfd_warm_hot"}) {
		t.Fatal("available discount evidence did not qualify the engaged grouped deal")
	}
}

func TestCanonicalDealURLPreservesSearchIdentity(t *testing.T) {
	for _, urls := range [][2]string{
		{"https://www.amazon.ca/s?k=coffee", "https://www.amazon.ca/s?k=monitor"},
		{"https://www.ebay.ca/sch/i.html?_nkw=coffee", "https://www.ebay.ca/sch/i.html?_nkw=monitor"},
		{"https://www.bestbuy.ca/en-ca/search?search=coffee", "https://www.bestbuy.ca/en-ca/search?search=monitor"},
	} {
		if sameCanonicalDealURL(urls[0], urls[1]) {
			t.Errorf("unrelated searches share canonical identity: %q / %q", urls[0], urls[1])
		}
	}
}

func TestCanonicalReferralTargetIsDecodedOnlyOnce(t *testing.T) {
	direct := "https://retailer.example/product?query=a%2Bb%26c%3Dd"
	wrapped := "https://go.redirectingat.com/?url=" + url.QueryEscape(direct)
	if !sameCanonicalDealURL(direct, wrapped) {
		t.Fatalf("referral decoding changed destination identity: direct=%q wrapped=%q", canonicalDealURL(direct), canonicalDealURL(wrapped))
	}
}
