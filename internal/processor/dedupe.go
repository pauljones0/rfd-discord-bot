package processor

import (
	"context"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

var (
	// Matches sequences of digits or letters, potentially including a decimal point in between.
	// Covers things like "40", "250gb", "5g", "3.14", "4k", "144hz"
	tokenRegex = regexp.MustCompile(`[a-z0-9]+(?:\.[a-z0-9]+)*`)

	// urlDelimReplacer replaces common URL delimiters with spaces for tokenization.
	urlDelimReplacer = strings.NewReplacer("/", " ", "?", " ", "&", " ", "=", " ", "-", " ", "_", " ", ".", " ")

	// ignorableTokens are common fluff words filtered from deal titles.
	// Kept at package level to avoid re-allocation on every call.
	ignorableTokens = map[string]bool{
		// Original stopwords
		"the": true, "and": true, "for": true, "with": true, "sale": true,
		"plan": true, "month": true, "mo": true, "canada": true, "deal": true,
		"off": true, "discount": true,
		// Expanded deal-common words
		"new": true, "free": true, "best": true, "price": true, "buy": true,
		"get": true, "now": true, "hot": true, "limited": true, "time": true,
		"offer": true, "save": true, "online": true, "only": true, "shop": true,
		"available": true, "from": true, "down": true, "drop": true, "great": true,
	}

	// urlNoiseTokens are domain/TLD/file-extension fragments stripped from URLs.
	urlNoiseTokens = map[string]bool{
		"www": true, "com": true, "ca": true, "org": true, "net": true,
		"html": true, "htm": true, "php": true, "aspx": true,
		"en": true, "fr": true,
	}
)

// extractWords splits a string into alphanumeric lowercased tokens.
func extractWords(text string) []string {
	text = strings.ToLower(text)
	matches := tokenRegex.FindAllString(text, -1)
	return matches
}

// GenerateSearchTokens takes a DealInfo and generates a deduplicated list of important search tokens.
func GenerateSearchTokens(deal *models.DealInfo) []string {
	seen := make(map[string]struct{})
	var tokens []string

	add := func(word string) {
		if _, exists := seen[word]; !exists {
			seen[word] = struct{}{}
			tokens = append(tokens, word)
		}
	}

	// Determine the best title to use.
	title := deal.CleanTitle
	if title == "" {
		title = deal.Title
	}

	// Filter and keep only valuable words (numbers, combined num+unit, brands) from Title
	for _, word := range extractWords(title) {
		if isValuableToken(word) {
			add(word)
		}
	}

	// Tokenize the canonical product URL if available, filtering URL-specific noise.
	for _, word := range extractTokensFromURL(canonicalDealURL(deal.ActualDealURL)) {
		if urlNoiseTokens[word] {
			continue
		}
		if isValuableToken(word) {
			add(word)
		}
	}

	return tokens
}

// isValuableToken decides if a token is useful for fuzzy matching.
func isValuableToken(word string) bool {
	// Skip very short words unless they are numbers
	if len(word) < 2 {
		return false
	}
	if ignorableTokens[word] {
		return false
	}

	// Keep numbers or alphanumeric strings (like 5g, 250gb)
	for _, c := range word {
		if c >= '0' && c <= '9' {
			return true
		}
	}

	// Keep longer standard words (likely brands/products)
	return len(word) >= 3
}

// extractTokensFromURL grabs tokens out of a URL string.
func extractTokensFromURL(urlStr string) []string {
	if urlStr == "" {
		return nil
	}

	if unescaped, err := url.QueryUnescape(urlStr); err == nil {
		urlStr = unescaped
	}

	urlStr = strings.TrimPrefix(urlStr, "https://")
	urlStr = strings.TrimPrefix(urlStr, "http://")

	// Replace typical URL delimiters with spaces
	urlStr = urlDelimReplacer.Replace(urlStr)

	return extractWords(urlStr)
}

func canonicalDealURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	raw = unwrapReferralURL(raw)
	cleaned := strings.TrimSpace(util.CleanProductURL(raw))
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Hostname() == "" {
		return strings.TrimRight(cleaned, "/")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	if strings.HasPrefix(parsed.Host, "www.") {
		parsed.Host = strings.TrimPrefix(parsed.Host, "www.")
	}
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}

	if isCanonicalProductHost(parsed.Hostname()) {
		parsed.RawQuery = ""
	}

	return parsed.String()
}

func unwrapReferralURL(raw string) string {
	for range 3 {
		parsed, err := url.Parse(raw)
		if err != nil {
			return raw
		}

		host := strings.ToLower(parsed.Hostname())
		var target string
		switch {
		case host == "click.linksynergy.com":
			target = parsed.Query().Get("murl")
		case host == "go.redirectingat.com":
			target = parsed.Query().Get("url")
		case host == "bestbuyca.o93x.net" && strings.HasPrefix(parsed.Path, "/c/"):
			target = parsed.Query().Get("u")
		default:
			return raw
		}

		target = strings.TrimSpace(target)
		if target == "" {
			return raw
		}
		if decoded, err := url.QueryUnescape(target); err == nil {
			target = decoded
		}
		if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
			return raw
		}
		raw = target
	}
	return raw
}

func isCanonicalProductHost(host string) bool {
	host = strings.ToLower(host)
	return strings.Contains(host, "amazon.") ||
		host == "ebay.ca" ||
		host == "ebay.com" ||
		strings.HasSuffix(host, ".ebay.ca") ||
		strings.HasSuffix(host, ".ebay.com") ||
		strings.Contains(host, "bestbuy.ca")
}

func sameCanonicalDealURL(left, right string) bool {
	leftKey := canonicalDealURL(left)
	rightKey := canonicalDealURL(right)
	return leftKey != "" && leftKey == rightKey
}

func recentDealsByCanonicalURL(recentDeals []models.DealInfo) map[string]*models.DealInfo {
	byURL := make(map[string]*models.DealInfo)
	for i := range recentDeals {
		deal := &recentDeals[i]
		key := canonicalDealURL(deal.ActualDealURL)
		if key == "" {
			continue
		}
		if current := byURL[key]; current == nil || preferCanonicalDeal(deal, current) {
			byURL[key] = deal
		}
	}
	return byURL
}

func preferCanonicalDeal(candidate, current *models.DealInfo) bool {
	if current == nil {
		return true
	}

	candidateHasMessage := len(candidate.DiscordMessageIDs) > 0
	currentHasMessage := len(current.DiscordMessageIDs) > 0
	if candidateHasMessage != currentHasMessage {
		return candidateHasMessage
	}

	if !candidate.PublishedTimestamp.Equal(current.PublishedTimestamp) {
		if candidate.PublishedTimestamp.IsZero() {
			return false
		}
		if current.PublishedTimestamp.IsZero() {
			return true
		}
		return candidate.PublishedTimestamp.Before(current.PublishedTimestamp)
	}

	candidateLikes, candidateComments, _ := candidate.Stats()
	currentLikes, currentComments, _ := current.Stats()
	if candidateLikes != currentLikes {
		return candidateLikes > currentLikes
	}
	if candidateComments != currentComments {
		return candidateComments > currentComments
	}
	return candidate.DocumentID < current.DocumentID
}

// calculateSimilarity returns a score between 0.0 and 1.0 based on token overlap.
func calculateSimilarity(tokensA, tokensB []string) float64 {
	if len(tokensA) == 0 || len(tokensB) == 0 {
		return 0.0
	}

	matchCount := 0
	for _, a := range tokensA {
		for _, b := range tokensB {
			if a == b {
				matchCount++
				break // Only count each token from A matched once
			}
		}
	}

	// Calculate Jaccard-like similarity or simple intersection over min length
	// We use intersection over min length because one deal might have extra fluff tokens
	// "Freedom 40 250gb" vs "Freedom 40 250gb roam beyond bonus"
	minLen := min(len(tokensA), len(tokensB))

	if minLen == 0 {
		return 0.0
	}

	return float64(matchCount) / float64(minLen)
}

// deduplicateDeals merges valid scraped deals with existing recent deals or other scraped deals.
func (p *DealProcessor) deduplicateDeals(ctx context.Context, scrapedDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, recentDeals []models.DealInfo, logger *slog.Logger) []models.DealInfo {
	var dedupedScraped []models.DealInfo
	canonicalRecentByURL := recentDealsByCanonicalURL(recentDeals)

	// Map to keep track of matched scraped deals so we don't process them twice.
	matchedScrapedIndices := make(map[int]bool)

	for i := range scrapedDeals {
		if matchedScrapedIndices[i] {
			continue // Already merged into another scraped deal earlier in loop
		}

		dealA := &scrapedDeals[i]

		if canonical := canonicalRecentByURL[canonicalDealURL(dealA.ActualDealURL)]; canonical != nil && canonical.DocumentID != dealA.DocumentID {
			logger.Info("Deal deduplicated with canonical product record", "scrapedTitle", dealA.Title, "existingTitle", canonical.Title)
			dealA.DocumentID = canonical.DocumentID
			if _, ok := existingDeals[canonical.DocumentID]; !ok {
				existingDeals[canonical.DocumentID] = canonical
			}
			dedupedScraped = append(dedupedScraped, *dealA)
			continue
		}

		// Layer 1: Exact ID match — same PublishedTimestamp means same post, skip silently.
		// This is the normal case: the same deal appears on the page every scrape cycle.
		if _, alreadyKnown := existingDeals[dealA.DocumentID]; alreadyKnown {
			dedupedScraped = append(dedupedScraped, *dealA)
			continue
		}

		// 1. Ensure DealA has tokens.
		dealA.SearchTokens = GenerateSearchTokens(dealA)

		// Layer 2: Fuzzy match against recent deals — catches different RFD threads
		// about the same product (different users posting the same deal).
		var matchedExisting *models.DealInfo
		for rIdx := range recentDeals {
			rDeal := &recentDeals[rIdx]
			if len(rDeal.SearchTokens) == 0 {
				rDeal.SearchTokens = GenerateSearchTokens(rDeal)
			}

			// Exact URL match (if not empty)
			if sameCanonicalDealURL(dealA.ActualDealURL, rDeal.ActualDealURL) {
				matchedExisting = rDeal
				break
			}

			// Fuzzy Match
			similarity := calculateSimilarity(dealA.SearchTokens, rDeal.SearchTokens)
			if similarity >= 0.80 { // 80% overlap of valuable tokens
				matchedExisting = rDeal
				break
			}
		}

		if matchedExisting != nil {
			logger.Info("Deal deduplicated with existing recent deal", "scrapedTitle", dealA.Title, "existingTitle", matchedExisting.Title)
			// Ensure it's in the existingDeals map so the rest of the pipeline updates it.
			// Point DealA's document ID to the existing record.
			dealA.DocumentID = matchedExisting.DocumentID

			if _, ok := existingDeals[matchedExisting.DocumentID]; !ok {
				existingDeals[matchedExisting.DocumentID] = matchedExisting
			}
			dedupedScraped = append(dedupedScraped, *dealA)
			continue
		}

		// 3. Check against other scraped deals (that haven't been matched yet)
		merged := false
		for j := i + 1; j < len(scrapedDeals); j++ {
			if matchedScrapedIndices[j] {
				continue
			}
			dealB := &scrapedDeals[j]
			dealB.SearchTokens = GenerateSearchTokens(dealB)

			isMatch := false
			if sameCanonicalDealURL(dealA.ActualDealURL, dealB.ActualDealURL) {
				isMatch = true
			} else {
				sim := calculateSimilarity(dealA.SearchTokens, dealB.SearchTokens)
				if sim >= 0.80 {
					isMatch = true
				}
			}

			if isMatch {
				logger.Info("Scraped deal deduplicated with another scraped deal", "titleA", dealA.Title, "titleB", dealB.Title)
				// Merge DealB's thread into DealA.
				dealB.DocumentID = dealA.DocumentID
				matchedScrapedIndices[j] = true
				if !merged {
					// First match: emit A once, then B.
					dedupedScraped = append(dedupedScraped, *dealA)
					merged = true
				}
				dedupedScraped = append(dedupedScraped, *dealB)
				// Continue scanning — there may be more duplicates of A in this batch.
			}
		}

		if !merged {
			dedupedScraped = append(dedupedScraped, *dealA)
		}
	}

	return dedupedScraped
}

// deduplicateDealsByDetailedURL runs after detail pages are fetched, when
// ActualDealURL is finally available for new RFD posts. The initial list-page
// dedupe can only use title/thread metadata, so this pass catches same-product
// duplicates whose titles were too different to fuzzy match.
func (p *DealProcessor) deduplicateDealsByDetailedURL(ctx context.Context, deals []models.DealInfo, existingDeals map[string]*models.DealInfo, recentDeals []models.DealInfo, logger *slog.Logger) []models.DealInfo {
	if ctx.Err() != nil || len(deals) == 0 {
		return deals
	}

	recentByURL := recentDealsByCanonicalURL(recentDeals)
	firstScrapedByURL := make(map[string]string)
	for i := range deals {
		key := canonicalDealURL(deals[i].ActualDealURL)
		if key == "" {
			continue
		}

		if recent := recentByURL[key]; recent != nil && recent.DocumentID != deals[i].DocumentID {
			logger.Info("Deal deduplicated by product URL after detail fetch",
				"scrapedTitle", deals[i].Title,
				"existingTitle", recent.Title,
				"url", key,
			)
			deals[i].DocumentID = recent.DocumentID
			if _, ok := existingDeals[recent.DocumentID]; !ok {
				existingDeals[recent.DocumentID] = recent
			}
			firstScrapedByURL[key] = recent.DocumentID
			continue
		}

		if documentID, exists := firstScrapedByURL[key]; exists && documentID != deals[i].DocumentID {
			logger.Info("Scraped deal deduplicated by product URL after detail fetch",
				"title", deals[i].Title,
				"url", key,
			)
			deals[i].DocumentID = documentID
			continue
		}
		firstScrapedByURL[key] = deals[i].DocumentID
	}

	return deals
}
