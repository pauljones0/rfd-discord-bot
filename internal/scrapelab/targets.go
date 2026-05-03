package scrapelab

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// TargetStore is the read-only storage surface scrape-lab needs.
type TargetStore interface {
	GetTrackedEbayItems(ctx context.Context) (map[string]ebay.TrackedItem, error)
	GetMemExpressSubscriptions(ctx context.Context) ([]models.Subscription, error)
	GetActiveBestBuySellers(ctx context.Context) ([]bestbuy.Seller, error)
}

// DiscoveryOptions controls store-backed target discovery.
type DiscoveryOptions struct {
	Sites     []string
	EbayLimit int
}

// DiscoverStoreTargets builds scrape-lab targets from stored bot state.
func DiscoverStoreTargets(ctx context.Context, store TargetStore, opts DiscoveryOptions) ([]Target, error) {
	sites := discoverySiteSet(opts.Sites)
	var targets []Target

	if sites["ebay"] {
		items, err := store.GetTrackedEbayItems(ctx)
		if err != nil {
			return nil, fmt.Errorf("load ebay targets: %w", err)
		}
		targets = append(targets, EbayTargetsFromTrackedItems(items, opts.EbayLimit)...)
	}

	if sites["memoryexpress"] {
		subs, err := store.GetMemExpressSubscriptions(ctx)
		if err != nil {
			return nil, fmt.Errorf("load memory express targets: %w", err)
		}
		targets = append(targets, MemoryExpressTargetsFromSubscriptions(subs)...)
	}

	if sites["bestbuy"] {
		sellers, err := store.GetActiveBestBuySellers(ctx)
		if err != nil {
			return nil, fmt.Errorf("load best buy targets: %w", err)
		}
		targets = append(targets, BestBuyTargetsFromSellers(sellers)...)
	}

	return targets, nil
}

// EbayTargetsFromTrackedItems returns recent tracked eBay listing-page targets.
func EbayTargetsFromTrackedItems(items map[string]ebay.TrackedItem, limit int) []Target {
	type candidate struct {
		docID string
		item  ebay.TrackedItem
	}

	byURL := make(map[string]candidate, len(items))
	for docID, item := range items {
		item.ItemURL = strings.TrimSpace(item.ItemURL)
		if item.ItemURL == "" {
			continue
		}
		existing, exists := byURL[item.ItemURL]
		if !exists || item.LastSeenAt.After(existing.item.LastSeenAt) {
			byURL[item.ItemURL] = candidate{docID: docID, item: item}
		}
	}

	candidates := make([]candidate, 0, len(byURL))
	for _, candidate := range byURL {
		candidates = append(candidates, candidate)
	}
	sort.Slice(candidates, func(i, j int) bool {
		left := candidates[i].item
		right := candidates[j].item
		if !left.LastSeenAt.Equal(right.LastSeenAt) {
			return left.LastSeenAt.After(right.LastSeenAt)
		}
		return ebayTargetName(candidates[i].docID, left) < ebayTargetName(candidates[j].docID, right)
	})

	if limit > 0 && len(candidates) > limit {
		candidates = candidates[:limit]
	}

	targets := make([]Target, 0, len(candidates))
	for _, candidate := range candidates {
		targets = append(targets, Target{
			Site: "ebay",
			Name: ebayTargetName(candidate.docID, candidate.item),
			URL:  candidate.item.ItemURL,
		})
	}
	return targets
}

// MemoryExpressTargetsFromSubscriptions returns one clearance target per subscribed store.
func MemoryExpressTargetsFromSubscriptions(subs []models.Subscription) []Target {
	storeSet := make(map[string]struct{})
	for _, sub := range subs {
		code := normalizeMemoryExpressStoreCode(sub.StoreCode)
		if code == "" {
			continue
		}
		storeSet[code] = struct{}{}
	}

	storeCodes := make([]string, 0, len(storeSet))
	for code := range storeSet {
		storeCodes = append(storeCodes, code)
	}
	sort.Strings(storeCodes)

	targets := make([]Target, 0, len(storeCodes))
	for _, code := range storeCodes {
		pageURL, err := memoryexpress.ClearanceURL(code)
		if err != nil {
			continue
		}
		targets = append(targets, Target{
			Site: "memoryexpress",
			Name: "memoryexpress-" + code,
			URL:  pageURL,
		})
	}
	return targets
}

// BestBuyTargetsFromSellers returns public seller search-page targets.
func BestBuyTargetsFromSellers(sellers []bestbuy.Seller) []Target {
	if len(sellers) == 0 {
		sellers = bestbuy.DefaultSellers
	}

	targets := make([]Target, 0, len(sellers))
	for _, seller := range sellers {
		pageURL := BestBuySellerPageURL(seller)
		if pageURL == "" {
			continue
		}
		name := strings.TrimSpace(seller.Name)
		if name == "" {
			name = "bestbuy-seller"
		}
		targets = append(targets, Target{
			Site: "bestbuy",
			Name: name,
			URL:  pageURL,
		})
	}
	return targets
}

// BestBuySellerPageURL returns the public BestBuy.ca seller search page.
func BestBuySellerPageURL(seller bestbuy.Seller) string {
	if searchURL := strings.TrimSpace(seller.SearchURL); searchURL != "" {
		return searchURL
	}

	searchPath := strings.TrimSpace(seller.SearchPath)
	if searchPath == "" {
		name := strings.TrimSpace(seller.Name)
		if name == "" {
			return ""
		}
		searchPath = "sellerName:" + name
	}

	params := url.Values{"path": {searchPath}}
	return "https://www.bestbuy.ca/en-ca/search?" + params.Encode()
}

func discoverySiteSet(sites []string) map[string]bool {
	if len(sites) == 0 {
		return map[string]bool{
			"ebay":          true,
			"memoryexpress": true,
			"bestbuy":       true,
		}
	}

	out := make(map[string]bool, len(sites))
	for _, site := range sites {
		site = normalizeDiscoverySite(site)
		if site != "" {
			out[site] = true
		}
	}
	if len(out) == 0 {
		out["ebay"] = true
		out["memoryexpress"] = true
		out["bestbuy"] = true
	}
	return out
}

func normalizeDiscoverySite(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "ebay":
		return "ebay"
	case "memoryexpress", "memexpress", "memory-express":
		return "memoryexpress"
	case "bestbuy", "best-buy":
		return "bestbuy"
	default:
		return ""
	}
}

func ebayTargetName(docID string, item ebay.TrackedItem) string {
	itemID := strings.TrimSpace(item.ItemID)
	if itemID == "" {
		itemID = strings.TrimSpace(docID)
	}
	seller := strings.TrimSpace(item.Seller)
	if seller != "" && itemID != "" {
		return seller + "-" + itemID
	}
	if title := strings.TrimSpace(item.Title); title != "" {
		return title
	}
	if itemID != "" {
		return "ebay-" + itemID
	}
	return "ebay-listing"
}

func normalizeMemoryExpressStoreCode(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if memoryexpress.ValidStoreCode(raw) {
		return raw
	}
	for code := range memoryexpress.Stores {
		if strings.EqualFold(code, raw) {
			return code
		}
	}
	return ""
}
