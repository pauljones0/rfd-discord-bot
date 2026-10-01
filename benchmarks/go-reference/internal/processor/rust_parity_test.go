package processor

import (
	"context"
	"encoding/json"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/dealquality"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"testing"
	"time"
)

func TestExportRustParity(t *testing.T) {
	if os.Getenv("RUST_PARITY_OUTPUT") == "" {
		t.Skip("offline golden export requires RUST_PARITY_OUTPUT")
	}

	now := time.Date(2026, 9, 29, 12, 0, 0, 123400000, time.FixedZone("fixture", -6*3600))
	rng := rand.New(rand.NewSource(73))
	out := []map[string]any{}
	p := New(nil, nil, nil, nil, &config.Config{}, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clone := func(d models.DealInfo) models.DealInfo { return cloneDeal(d) }
	for n := 0; n < 240; n++ {
		d := models.DealInfo{DocumentID: "canonical", Title: []string{"Example SSD 1TB $100 sale", "Example SSD 2TB $120", "Example Galaxy S23 256GB", "Example Galaxy S24 256GB", "not a deal full price", "100% cotton shirt"}[n%6], PostURL: "https://forums.redflagdeals.com/original-title-12345/", ActualDealURL: "https://www.amazon.ca/dp/B0FIXTURE?tag=old-20", Retailer: "Amazon.ca", Category: "Computers & Electronics", PublishedTimestamp: now.Add(-time.Hour), Price: []string{"$100", "", "$50"}[n%3], OriginalPrice: []string{"$120", "", "$50"}[n%3], Savings: []string{"5%", "$3", ""}[n%3], Description: "Canonical description", Summary: "", Threads: []models.ThreadContext{{DocumentID: "canonical", PostURL: "https://forums.redflagdeals.com/original-title-12345/", LikeCount: rng.Intn(80) - 10, CommentCount: rng.Intn(40), ViewCount: 1000, ViewCountAvailable: n%2 == 0}}, HasBeenWarm: n%2 == 0, HasBeenHot: n%3 == 0}
		if n%3 == 0 {
			d.DiscordMessageIDs = map[string]string{"42": "99"}
			d.DiscordMessageApplicationIDs = map[string]string{"42": "1001"}
		}
		if n%8 == 0 {
			d.CleanTitle = "Clean example SSD"
			d.AIProcessed = true
		}
		obs := clone(d)
		obs.Title = obs.Title + " updated"
		obs.PostURL = "https://forums.redflagdeals.com/updated-title-12345/"
		obs.Threads[0].PostURL = obs.PostURL
		obs.Threads[0].LikeCount += 3
		obs.Threads[0].ViewCountAvailable = n%3 == 0
		obs.PublishedTimestamp = now.Add(-time.Minute)
		obs.ActualDealURL = ""
		obs.Description = ""
		obs.CleanTitle = ""
		obs.AIProcessed = false
		obs.Threads[0].NotFound = n%7 == 0
		duplicate := clone(obs)
		duplicate.DocumentID = "duplicate"
		duplicate.Threads[0].DocumentID = "duplicate"
		duplicate.PostURL = "https://forums.redflagdeals.com/duplicate-23456"
		duplicate.Threads[0].PostURL = duplicate.PostURL
		duplicate.Threads[0].LikeCount += 30
		duplicate.Title = []string{"Example SSD 1TB $90 sale", "Example SSD 2TB $90 sale", "Example Galaxy S23 256GB", "Example Galaxy S24 256GB"}[n%4]
		duplicate.PublishedTimestamp = now
		duplicate.ActualDealURL = obs.ActualDealURL
		duplicate.Threads[0].NotFound = n%5 == 0
		if n%4 == 0 {
			duplicate.ActualDealURL = d.ActualDealURL
		}
		if n%9 == 0 {
			duplicate.ActualDealURL = "https://amazon.ca/dp/B0OTHER"
		}
		if n%6 == 0 {
			duplicate.Retailer = "Best Buy"
		}
		observations := []models.DealInfo{obs, duplicate}
		var existing *models.DealInfo
		if n%5 != 0 {
			existing = &d
		}
		result := p.reconcileDeal(existing, observations)
		quality := dealquality.EvaluateRFDWarmHotDiscount(d)
		heated := clone(d)
		p.applyRFDWarmHotState(&heated)
		eligibility := map[string]bool{}
		for _, filter := range []string{"rfd_all", "rfd_tech", "rfd_warm_hot", "rfd_warm_hot_tech", "rfd_hot", "rfd_hot_tech"} {
			eligibility[filter] = p.isDealEligibleForSubscription(d, models.Subscription{DealType: filter})
		}
		recent := []models.DealInfo{clone(d)}
		existingMap := map[string]*models.DealInfo{}
		if n%2 == 0 {
			existingMap[d.DocumentID] = &d
		}
		scraped := []models.DealInfo{clone(obs), clone(duplicate)}
		dedup := p.deduplicateDeals(context.Background(), scraped, existingMap, recent, logger)
		dedup = p.deduplicateDealsByDetailedURL(context.Background(), dedup, existingMap, recent, logger)
		originalExisting := map[string]models.DealInfo{}
		if n%2 == 0 {
			originalExisting[d.DocumentID] = d
		}
		out = append(out, map[string]any{"existing": existing, "observations": observations, "reconciled": result, "deal": d, "discount": quality, "heated": heated, "eligible": eligibility, "tokens": GenerateSearchTokens(&d), "canonical_url": canonicalDealURL(d.ActualDealURL), "dedupe_existing": originalExisting, "recent": []models.DealInfo{d}, "deduped": dedup})
	}
	raw, e := json.Marshal(out)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(os.Getenv("RUST_PARITY_OUTPUT"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
