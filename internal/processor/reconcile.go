package processor

import (
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/dealquality"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

// reconcileDeal combines observations without changing the stored record. A
// duplicate thread contributes engagement and missing details, never a different
// product's canonical title, publication time, or notification ownership.
func (p *DealProcessor) reconcileDeal(existing *models.DealInfo, observations []models.DealInfo) *models.DealInfo {
	live := liveScrapedDeals(observations)
	if existing == nil {
		if len(live) == 0 {
			return nil
		}
		deal := cloneDeal(live[0])
		for _, duplicate := range live[1:] {
			fillMissingDetails(&deal, duplicate)
			for _, thread := range duplicate.Threads {
				p.mergeThread(&deal, thread)
			}
		}
		p.sortThreads(&deal)
		return &deal
	}
	deal := cloneDeal(*existing)
	deduplicateThreadsByKey(&deal)
	removed := removeNotFoundThreads(&deal, observations)
	base := contentBaseForExistingDeal(&deal, live)
	for _, observation := range live {
		for _, thread := range observation.Threads {
			p.mergeThread(&deal, thread)
		}
	}
	if deal.Title != base.Title {
		deal.CleanTitle, deal.AIProcessed = "", false
	}
	deal.Title = base.Title
	deal.PostURL = base.PostURL
	deal.Retailer = base.Retailer
	deal.Category = base.Category
	deal.Price = base.Price
	deal.OriginalPrice = base.OriginalPrice
	deal.Savings = base.Savings
	deal.ThreadImageURL = base.ThreadImageURL
	deal.PublishedTimestamp = base.PublishedTimestamp
	deal.ActualDealURL = base.ActualDealURL
	deal.Description = base.Description
	deal.Comments = base.Comments
	deal.Summary = base.Summary
	deal.SearchTokens = slices.Clone(base.SearchTokens)
	if base.AIProcessed && base.CleanTitle != "" {
		deal.CleanTitle, deal.AIProcessed = base.CleanTitle, true
	}
	p.sortThreads(&deal)
	if removed {
		syncPrimaryPostURL(&deal)
	}
	return &deal
}

func cloneDeal(deal models.DealInfo) models.DealInfo {
	deal.Threads = slices.Clone(deal.Threads)
	deal.SearchTokens = slices.Clone(deal.SearchTokens)
	deal.DiscordMessageIDs = maps.Clone(deal.DiscordMessageIDs)
	deal.DiscordMessageApplicationIDs = maps.Clone(deal.DiscordMessageApplicationIDs)
	return deal
}

func liveScrapedDeals(scrapedDeals []models.DealInfo) []models.DealInfo {
	liveDeals := make([]models.DealInfo, 0, len(scrapedDeals))
	for _, deal := range scrapedDeals {
		if len(deal.Threads) == 0 {
			liveDeals = append(liveDeals, deal)
			continue
		}

		filtered := deal
		filtered.Threads = nil
		for _, thread := range deal.Threads {
			if !thread.NotFound {
				filtered.Threads = append(filtered.Threads, thread)
			}
		}
		if len(filtered.Threads) == 0 {
			continue
		}
		if removedPrimaryThread(deal, filtered) {
			filtered.PostURL = filtered.Threads[0].PostURL
		}
		liveDeals = append(liveDeals, filtered)
	}
	return liveDeals
}

func removedPrimaryThread(original, filtered models.DealInfo) bool {
	if len(original.Threads) == 0 || len(filtered.Threads) == 0 {
		return false
	}
	originalPrimaryKey := threadKey(original.PrimaryPostURL())
	return originalPrimaryKey != "" && originalPrimaryKey != threadKey(filtered.Threads[0].PostURL)
}

func removeNotFoundThreads(existing *models.DealInfo, scrapedDeals []models.DealInfo) bool {
	notFoundKeys := make(map[string]struct{})
	for _, deal := range scrapedDeals {
		for _, thread := range deal.Threads {
			if !thread.NotFound {
				continue
			}
			if key := threadKey(thread.PostURL); key != "" {
				notFoundKeys[key] = struct{}{}
			}
		}
	}
	if len(notFoundKeys) == 0 {
		return false
	}

	filtered := existing.Threads[:0]
	changed := false
	for _, thread := range existing.Threads {
		if _, ok := notFoundKeys[threadKey(thread.PostURL)]; ok {
			changed = true
			continue
		}
		filtered = append(filtered, thread)
	}
	if changed {
		existing.Threads = filtered
	}
	return changed
}

func syncPrimaryPostURL(deal *models.DealInfo) {
	if len(deal.Threads) == 0 {
		deal.PostURL = ""
		return
	}
	deal.PostURL = deal.Threads[0].PostURL
}

func contentBaseForExistingDeal(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) models.DealInfo {
	if len(scrapedDuplicates) == 0 {
		return *existing
	}

	if sameThread := scrapeForExistingThread(existing, scrapedDuplicates); sameThread != nil {
		base := *sameThread
		preserveExistingDetails(&base, existing)
		return base
	}
	if sameDocument := scrapeForExistingDocument(existing, scrapedDuplicates); sameDocument != nil {
		base := *sameDocument
		preserveExistingDetails(&base, existing)
		return base
	}

	base := *existing
	for _, scraped := range scrapedDuplicates {
		fillMissingDetails(&base, scraped)
	}
	return base
}

func scrapeForExistingThread(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) *models.DealInfo {
	// A grouped duplicate is a known thread after its first poll, but it must
	// never become the source of the canonical title, price, or publication time.
	canonicalKeys := make(map[string]struct{})
	for _, thread := range existing.Threads {
		if existing.DocumentID != "" && thread.DocumentID == existing.DocumentID {
			if key := threadKey(thread.PostURL); key != "" {
				canonicalKeys[key] = struct{}{}
			}
		}
	}
	// Older imports can lack per-thread identities. The canonical post URL is
	// retained separately from the thread popularity order.
	if len(canonicalKeys) == 0 {
		canonicalURL := existing.PostURL
		if canonicalURL == "" && len(existing.Threads) > 0 {
			canonicalURL = existing.Threads[0].PostURL
		}
		if key := threadKey(canonicalURL); key != "" {
			canonicalKeys[key] = struct{}{}
		}
	}

	for i := range scrapedDuplicates {
		for _, thread := range scrapedDuplicates[i].Threads {
			if _, ok := canonicalKeys[threadKey(thread.PostURL)]; ok {
				return &scrapedDuplicates[i]
			}
		}
	}
	return nil
}

func scrapeForExistingDocument(existing *models.DealInfo, scrapedDuplicates []models.DealInfo) *models.DealInfo {
	for i := range scrapedDuplicates {
		if scrapedDuplicates[i].DocumentID != existing.DocumentID {
			continue
		}
		if scrapeWasRemappedFromAnotherDocument(scrapedDuplicates[i], existing.DocumentID) {
			continue
		}
		return &scrapedDuplicates[i]
	}
	return nil
}

func scrapeWasRemappedFromAnotherDocument(scraped models.DealInfo, documentID string) bool {
	for _, thread := range scraped.Threads {
		if thread.DocumentID != "" && thread.DocumentID != documentID {
			return true
		}
	}
	return false
}

func preserveExistingDetails(scraped *models.DealInfo, existing *models.DealInfo) {
	if existing.ActualDealURL != "" && sameCanonicalDealURL(existing.ActualDealURL, scraped.ActualDealURL) {
		scraped.ActualDealURL = existing.ActualDealURL
	}
	fillMissingDetails(scraped, *existing)
}

func fillMissingDetails(base *models.DealInfo, candidate models.DealInfo) {
	if base.PostURL == "" {
		base.PostURL = candidate.PostURL
	}
	if base.Retailer == "" {
		base.Retailer = candidate.Retailer
	}
	if base.Category == "" {
		base.Category = candidate.Category
	}
	if base.Price == "" {
		base.Price = candidate.Price
	}
	if base.OriginalPrice == "" {
		base.OriginalPrice = candidate.OriginalPrice
	}
	if base.Savings == "" {
		base.Savings = candidate.Savings
	}
	if base.ActualDealURL == "" {
		base.ActualDealURL = candidate.ActualDealURL
	}
	if base.ThreadImageURL == "" {
		base.ThreadImageURL = candidate.ThreadImageURL
	}
	if base.Description == "" {
		base.Description = candidate.Description
	}
	if base.Comments == "" {
		base.Comments = candidate.Comments
	}
	if base.Summary == "" {
		base.Summary = candidate.Summary
	}
	if len(base.SearchTokens) == 0 {
		base.SearchTokens = candidate.SearchTokens
	}
}

// mergeThread updates the stats for an existing thread or appends a new one.
// Returns true if anything actually changed (stats or URL).
func (p *DealProcessor) mergeThread(deal *models.DealInfo, newThread models.ThreadContext) bool {
	if newThread.NotFound {
		return false
	}

	newKey := threadKey(newThread.PostURL)
	for i := range deal.Threads {
		if threadKey(deal.Threads[i].PostURL) == newKey {
			viewChanged := false
			if newThread.ViewCountAvailable {
				viewChanged = deal.Threads[i].ViewCount != newThread.ViewCount ||
					!deal.Threads[i].ViewCountAvailable
			} else {
				viewChanged = deal.Threads[i].ViewCount != 0 ||
					deal.Threads[i].ViewCountAvailable
			}

			changed := deal.Threads[i].LikeCount != newThread.LikeCount ||
				deal.Threads[i].CommentCount != newThread.CommentCount ||
				viewChanged ||
				deal.Threads[i].PostURL != newThread.PostURL

			deal.Threads[i].LikeCount = newThread.LikeCount
			deal.Threads[i].CommentCount = newThread.CommentCount
			if newThread.ViewCountAvailable {
				deal.Threads[i].ViewCount = newThread.ViewCount
				deal.Threads[i].ViewCountAvailable = true
			} else {
				deal.Threads[i].ViewCount = 0
				deal.Threads[i].ViewCountAvailable = false
			}
			deal.Threads[i].PostURL = newThread.PostURL // keep latest URL slug
			return changed
		}
	}
	// New thread duplicate found
	deal.Threads = append(deal.Threads, newThread)
	return true
}

// deduplicateThreadsByKey collapses threads that share the same threadKey,
// keeping the entry with the highest LikeCount. This cleans up historical data
// where slug-variant duplicates were stored before threadKey used the thread ID.
func deduplicateThreadsByKey(deal *models.DealInfo) bool {
	seen := make(map[string]int) // key -> index in deduped slice
	var deduped []models.ThreadContext
	changed := false
	for _, t := range deal.Threads {
		key := threadKey(t.PostURL)
		if idx, exists := seen[key]; exists {
			changed = true
			// Keep the one with higher likes
			if t.LikeCount > deduped[idx].LikeCount {
				deduped[idx] = t
			}
		} else {
			seen[key] = len(deduped)
			deduped = append(deduped, t)
		}
	}
	if changed {
		deal.Threads = deduped
	}
	return changed
}

// threadKey normalizes a PostURL for deduplication.
// For RFD URLs it extracts the numeric thread ID (e.g. "rfd:2806520") so that
// slug variations of the same thread (caused by title edits) collapse to one key.
// Non-RFD URLs fall back to the full URL stripped of fragments and trailing slashes.
func threadKey(rawURL string) string {
	// Strip fragment
	if idx := strings.Index(rawURL, "#"); idx != -1 {
		rawURL = rawURL[:idx]
	}
	rawURL = strings.TrimRight(rawURL, "/")

	// For RFD URLs, extract the numeric thread ID as the canonical key.
	// RFD thread URLs end with -{numeric_id}, e.g. /firehouse-subs-deal-2806520
	if parsed, err := url.Parse(rawURL); err == nil && strings.Contains(strings.ToLower(parsed.Hostname()), "redflagdeals.com") {
		path := strings.TrimRight(parsed.Path, "/")
		lastSlash := strings.LastIndex(path, "/")
		if lastSlash >= 0 {
			slug := path[lastSlash+1:]
			lastHyphen := strings.LastIndex(slug, "-")
			if lastHyphen >= 0 && lastHyphen < len(slug)-1 {
				candidate := slug[lastHyphen+1:]
				if isAllDigits(candidate) {
					return "rfd:" + candidate
				}
			}
		}
	}

	return rawURL
}

func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// sortThreads sorts a deal's threads array descending by LikeCount, then by CommentCount
func (p *DealProcessor) sortThreads(deal *models.DealInfo) {
	sort.Slice(deal.Threads, func(i, j int) bool {
		if deal.Threads[i].LikeCount != deal.Threads[j].LikeCount {
			return deal.Threads[i].LikeCount > deal.Threads[j].LikeCount
		}
		return deal.Threads[i].CommentCount > deal.Threads[j].CommentCount
	})
}

func (p *DealProcessor) isDealEligibleForSubscription(deal models.DealInfo, sub models.Subscription) bool {
	isTech := deal.Category != "" && util.IsTechCategory(deal.Category)
	discountBacked := dealquality.RFDWarmHotEligible(deal)
	isWarm := discountBacked && (deal.HasBeenWarm || dealquality.IsWarm(deal))
	isHot := discountBacked && (deal.HasBeenHot || dealquality.IsHot(deal))
	return dealtypes.RFDEligible(sub.DealType, isTech, isWarm, isHot)
}

func (p *DealProcessor) applyRFDWarmHotState(deal *models.DealInfo) {
	if deal == nil {
		return
	}
	if !dealquality.RFDWarmHotEligible(*deal) {
		deal.HasBeenWarm = false
		deal.HasBeenHot = false
		return
	}
	if dealquality.IsWarm(*deal) {
		deal.HasBeenWarm = true
	}
	if dealquality.IsHot(*deal) {
		deal.HasBeenHot = true
	}
}
