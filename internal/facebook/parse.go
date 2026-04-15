package facebook

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func processRawAd(adMap map[string]interface{}, cfg *FacebookScrapeConfig) (*models.ScrapedAd, string, bool) {
	id, _ := adMap["id"].(string)
	if id == "" {
		return nil, "", false
	}

	textsInterface, _ := adMap["texts"].([]interface{})
	var texts []string
	for _, t := range textsInterface {
		if str, ok := t.(string); ok {
			texts = append(texts, str)
		}
	}

	if len(texts) < 2 {
		return nil, "", false
	}

	priceIdx := -1
	for i, t := range texts {
		if strings.Contains(t, "$") {
			priceIdx = i
			break
		}
	}
	if priceIdx == -1 {
		return nil, "", false
	}
	priceStr := texts[priceIdx]

	// Skip past any consecutive price-like elements (e.g. reduced-price listings
	// show both the new and old price: ["CA$22,000", "CA$24,500", "1990 Ford ...", ...])
	titleIdx := priceIdx + 1
	for titleIdx < len(texts) && strings.Contains(texts[titleIdx], "$") {
		titleIdx++
	}
	if titleIdx >= len(texts) {
		return nil, "", false
	}
	title := texts[titleIdx]

	if len(cfg.FilterBrands) > 0 {
		lwTitle := strings.ToLower(title)
		found := false
		for _, b := range cfg.FilterBrands {
			if strings.Contains(lwTitle, strings.ToLower(b)) {
				found = true
				break
			}
		}
		if !found {
			return nil, "", false
		}
	}

	var subtitles []string
	mileage := ""
	if len(texts) > titleIdx+1 {
		subtitles = texts[titleIdx+1:]
		if len(subtitles) > 1 {
			mileage = subtitles[1]
		}
	}

	priceStr = strings.ReplaceAll(priceStr, "CA$", "")
	priceStr = strings.ReplaceAll(priceStr, "C$", "")
	priceStr = strings.ReplaceAll(priceStr, "US$", "")
	priceStr = strings.ReplaceAll(priceStr, "$", "")
	priceStr = strings.ReplaceAll(priceStr, ",", "")
	priceStr = strings.TrimSpace(priceStr)

	var price float64
	lowerPrice := strings.ToLower(priceStr)
	if lowerPrice == "free" {
		price = 0
	} else if lowerPrice == "" || lowerPrice == "negotiable" || lowerPrice == "contact for price" {
		// Skip ads without a concrete price — can't evaluate a deal without one
		return nil, "", false
	} else {
		p, err := strconv.ParseFloat(priceStr, 64)
		if err != nil {
			// Unparseable price — skip this ad rather than defaulting to 0
			return nil, "", false
		}
		price = p
	}

	cleanURL := fmt.Sprintf("https://www.facebook.com/marketplace/item/%s/", id)

	return &models.ScrapedAd{
		ListingID: id,
		Title:     strings.TrimSpace(title),
		Price:     price,
		URL:       cleanURL,
		Mileage:   mileage,
		Subtitles: subtitles,
	}, id, true
}
