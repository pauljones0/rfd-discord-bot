package ebay

import (
	"fmt"
	"html"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

type SoldListing struct {
	Title string
	Price float64
}

func SoldSearchURL(query string) string {
	values := url.Values{}
	values.Set("_nkw", query)
	values.Set("LH_Sold", "1")
	values.Set("LH_Complete", "1")
	values.Set("LH_BIN", "1")
	values.Set("_sop", "13")
	values.Set("rt", "nc")
	return "https://www.ebay.ca/sch/i.html?" + values.Encode()
}

func ParseSoldListings(pageHTML string) ([]SoldListing, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(pageHTML))
	if err != nil {
		return nil, err
	}
	var listings []SoldListing
	seen := make(map[string]bool)
	addListing := func(title, priceText string) {
		title = CleanSoldTitle(title)
		if title == "" || strings.Contains(strings.ToLower(title), "shop on ebay") {
			return
		}
		price, ok := ParseSoldPrice(priceText)
		if !ok || price <= 0 {
			return
		}
		key := fmt.Sprintf("%s|%.2f", strings.ToLower(title), price)
		if seen[key] {
			return
		}
		seen[key] = true
		listings = append(listings, SoldListing{Title: title, Price: price})
	}

	doc.Find("li.s-item").Each(func(_ int, sel *goquery.Selection) {
		priceText := strings.TrimSpace(sel.Find(".s-item__price").First().Text())
		addListing(sel.Find(".s-item__title").First().Text(), priceText)
	})
	doc.Find(".s-card__title").Each(func(_ int, sel *goquery.Selection) {
		title := sel.Find(".su-styled-text.primary").First().Text()
		if strings.TrimSpace(title) == "" {
			title = sel.Text()
		}
		card := sel.Closest("li")
		if card.Length() == 0 {
			card = sel.Closest(".su-card-container")
		}
		priceText := strings.TrimSpace(card.Find(".s-card__price").First().Text())
		addListing(title, priceText)
	})
	return listings, nil
}

func CleanSoldTitle(title string) string {
	title = html.UnescapeString(strings.TrimSpace(title))
	title = strings.TrimSpace(strings.TrimPrefix(title, "New Listing"))
	title = regexp.MustCompile(`(?i)\s+opens in a new window or tab\b.*$`).ReplaceAllString(title, "")
	return strings.TrimSpace(title)
}

func ParseSoldPrice(text string) (float64, bool) {
	text = html.UnescapeString(strings.TrimSpace(text))
	lower := strings.ToLower(text)
	if strings.Contains(lower, " to ") || strings.Contains(lower, "shipping") {
		return 0, false
	}
	if strings.Contains(lower, "us $") || strings.Contains(lower, "eur") || strings.Contains(lower, "gbp") {
		return 0, false
	}
	match := regexp.MustCompile(`(?i)(?:c\s*)?\$\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`).FindStringSubmatch(text)
	if len(match) < 2 {
		return 0, false
	}
	value, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", ""), 64)
	return value, err == nil
}

type DealVerification struct {
	MedianPrice float64
	Count       int
	IsGoodDeal  bool
	Query       string
}

func VerifyDeal(title string, currentPrice float64, listings []SoldListing) DealVerification {
	if currentPrice <= 0 || len(listings) == 0 {
		return DealVerification{}
	}

	var matchingPrices []float64
	for _, listing := range listings {
		if soldListingMatches(title, listing.Title) {
			matchingPrices = append(matchingPrices, listing.Price)
		}
	}

	if len(matchingPrices) < 3 {
		return DealVerification{Count: len(matchingPrices)}
	}

	// Calculate median
	median := calculateMedian(matchingPrices)
	
	// A good deal is at least 10% below median
	isGood := currentPrice < (median * 0.9)

	return DealVerification{
		MedianPrice: median,
		Count:       len(matchingPrices),
		IsGoodDeal:  isGood,
	}
}

func soldListingMatches(target, candidate string) bool {
	target = strings.ToLower(target)
	candidate = strings.ToLower(candidate)
	
	// Simple token-based overlap
	targetTokens := strings.Fields(target)
	candidateTokens := strings.Fields(candidate)
	
	if len(targetTokens) == 0 {
		return false
	}

	matches := 0
	for _, t := range targetTokens {
		if len(t) < 3 {
			continue
		}
		for _, c := range candidateTokens {
			if t == c {
				matches++
				break
			}
		}
	}
	
	overlap := float64(matches) / float64(len(targetTokens))
	return overlap > 0.4
}

func calculateMedian(prices []float64) float64 {
	if len(prices) == 0 {
		return 0
	}
	sorted := make([]float64, len(prices))
	copy(sorted, prices)
	sort.Float64s(sorted)
	
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}
