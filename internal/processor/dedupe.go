package processor

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"net/url"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
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

	// Tokenize ActualDealURL if available, filtering URL-specific noise
	for _, word := range extractTokensFromURL(deal.ActualDealURL) {
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
	minLen := len(tokensA)
	if len(tokensB) < len(tokensA) {
		minLen = len(tokensB)
	}

	if minLen == 0 {
		return 0.0
	}

	return float64(matchCount) / float64(minLen)
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}

// deduplicateDeals merges valid scraped deals with existing recent deals or other scraped deals.
func (p *DealProcessor) deduplicateDeals(ctx context.Context, scrapedDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, recentDeals []models.DealInfo, logger *slog.Logger) []models.DealInfo {
	var dedupedScraped []models.DealInfo

	// Map to keep track of matched scraped deals so we don't process them twice.
	matchedScrapedIndices := make(map[int]bool)

	for i := range scrapedDeals {
		if matchedScrapedIndices[i] {
			continue // Already merged into another scraped deal earlier in loop
		}

		dealA := &scrapedDeals[i]

		// Layer 1: Exact ID match — same PublishedTimestamp means same post, skip silently.
		// This is the normal case: the same deal appears on the page every scrape cycle.
		if _, alreadyKnown := existingDeals[dealA.FirestoreID]; alreadyKnown {
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

			// Exact URL match (if not empty)
			if dealA.ActualDealURL != "" && dealA.ActualDealURL == rDeal.ActualDealURL {
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
			// We point DealA's FirestoreID to the existing one.
			dealA.FirestoreID = matchedExisting.FirestoreID

			if _, ok := existingDeals[matchedExisting.FirestoreID]; !ok {
				existingDeals[matchedExisting.FirestoreID] = matchedExisting
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
			if dealA.ActualDealURL != "" && dealA.ActualDealURL == dealB.ActualDealURL {
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
				dealB.FirestoreID = dealA.FirestoreID
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
