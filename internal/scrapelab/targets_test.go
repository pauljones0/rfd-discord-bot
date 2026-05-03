package scrapelab

import (
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestEbayTargetsFromTrackedItems(t *testing.T) {
	now := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	items := map[string]ebay.TrackedItem{
		"empty": {
			ItemID:     "empty",
			ItemURL:    "",
			LastSeenAt: now.Add(1 * time.Hour),
		},
		"new": {
			ItemID:     "new",
			Seller:     "seller",
			ItemURL:    "https://www.ebay.ca/itm/new",
			LastSeenAt: now.Add(3 * time.Hour),
		},
		"dupe-stale": {
			ItemID:     "dupe-stale",
			Seller:     "seller",
			ItemURL:    "https://www.ebay.ca/itm/dupe",
			LastSeenAt: now.Add(30 * time.Minute),
		},
		"dupe-fresh": {
			ItemID:     "dupe-fresh",
			Seller:     "seller",
			ItemURL:    "https://www.ebay.ca/itm/dupe",
			LastSeenAt: now.Add(2 * time.Hour),
		},
		"old": {
			ItemID:     "old",
			Seller:     "seller",
			ItemURL:    "https://www.ebay.ca/itm/old",
			LastSeenAt: now.Add(1 * time.Hour),
		},
	}

	all := EbayTargetsFromTrackedItems(items, 0)
	if len(all) != 3 {
		t.Fatalf("expected 3 deduped non-empty targets, got %d: %#v", len(all), all)
	}
	wantURLs := []string{
		"https://www.ebay.ca/itm/new",
		"https://www.ebay.ca/itm/dupe",
		"https://www.ebay.ca/itm/old",
	}
	for i, want := range wantURLs {
		if all[i].URL != want {
			t.Fatalf("target %d URL = %q, want %q", i, all[i].URL, want)
		}
	}
	if all[1].Name != "seller-dupe-fresh" {
		t.Fatalf("duplicate should keep freshest item name, got %q", all[1].Name)
	}

	limited := EbayTargetsFromTrackedItems(items, 2)
	if len(limited) != 2 {
		t.Fatalf("expected limit to return 2 targets, got %d", len(limited))
	}
	if limited[0].URL != wantURLs[0] || limited[1].URL != wantURLs[1] {
		t.Fatalf("limited targets = %#v, want first two recent URLs", limited)
	}
}

func TestBestBuySellerPageURL(t *testing.T) {
	explicit := bestbuy.Seller{
		Name:      "Tech Outlet Center",
		SearchURL: "https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center",
	}
	if got := BestBuySellerPageURL(explicit); got != explicit.SearchURL {
		t.Fatalf("explicit SearchURL was not preserved: %q", got)
	}

	builtFromName := bestbuy.Seller{Name: "Parts Search"}
	wantParts := "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search"
	if got := BestBuySellerPageURL(builtFromName); got != wantParts {
		t.Fatalf("built URL = %q, want %q", got, wantParts)
	}

	builtFromPath := bestbuy.Seller{
		Name:       "ignored",
		SearchPath: "sellerName:Tech Outlet Center",
	}
	wantTech := "https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center"
	if got := BestBuySellerPageURL(builtFromPath); got != wantTech {
		t.Fatalf("built path URL = %q, want %q", got, wantTech)
	}
}

func TestBestBuyTargetsFromSellersFallsBackToDefaults(t *testing.T) {
	targets := BestBuyTargetsFromSellers(nil)
	if len(targets) != 2 {
		t.Fatalf("expected 2 default bestbuy targets, got %d", len(targets))
	}
	if targets[0].URL != "https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center" {
		t.Fatalf("unexpected first default URL: %q", targets[0].URL)
	}
	if targets[1].URL != "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search" {
		t.Fatalf("unexpected second default URL: %q", targets[1].URL)
	}
}

func TestMemoryExpressTargetsFromSubscriptions(t *testing.T) {
	subs := []models.Subscription{
		{StoreCode: "SKST"},
		{StoreCode: "skst"},
		{StoreCode: "WpgW"},
		{StoreCode: "not-a-store"},
		{StoreCode: ""},
	}

	targets := MemoryExpressTargetsFromSubscriptions(subs)
	if len(targets) != 2 {
		t.Fatalf("expected 2 deduped store targets, got %d: %#v", len(targets), targets)
	}
	if targets[0].URL != "https://www.memoryexpress.com/Clearance/Store/SKST" {
		t.Fatalf("first target URL = %q", targets[0].URL)
	}
	if targets[1].URL != "https://www.memoryexpress.com/Clearance/Store/WpgW" {
		t.Fatalf("second target URL = %q", targets[1].URL)
	}
}
