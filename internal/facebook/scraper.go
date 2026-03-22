package facebook

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/playwright-community/playwright-go"
)

const (
	// jsExtractListingDetail dismisses the login modal, clicks "See more" if present,
	// and extracts the full seller description and structured vehicle data.
	jsExtractListingDetail = `async () => {
		// Dismiss login modal
		const dialogs = document.querySelectorAll('[role="dialog"]');
		dialogs.forEach(d => {
			if (d.innerText && d.innerText.includes('See more on Facebook')) {
				d.remove();
			}
		});

		// Click "See more" in the description
		const seeMoreBtn = Array.from(document.querySelectorAll('span'))
			.find(s => s.innerText && s.innerText.trim() === 'See more' && s.closest('[role="button"]'));
		if (seeMoreBtn) {
			seeMoreBtn.closest('[role="button"]').click();
			await new Promise(r => setTimeout(r, 500));
		}

		// Extract the expanded description by finding "See less" or the description heading
		let description = '';
		const seeLess = Array.from(document.querySelectorAll('span'))
			.find(s => s.innerText && s.innerText.trim() === 'See less');
		if (seeLess) {
			let el = seeLess;
			for (let i = 0; i < 10; i++) {
				if (el.parentElement) el = el.parentElement;
				if (el.innerText && el.innerText.length > 50 && el.innerText.includes('See less')) {
					description = el.innerText.replace(/\s*See less\s*$/, '').trim();
					break;
				}
			}
		}
		if (!description) {
			const descSpans = Array.from(document.querySelectorAll('span[dir="auto"]'));
			const descHeading = descSpans.findIndex(s => s.innerText === "Seller's description");
			if (descHeading !== -1 && descHeading + 1 < descSpans.length) {
				description = descSpans[descHeading + 1].innerText;
			}
		}

		// Extract structured "About this vehicle" data
		const aboutItems = Array.from(document.querySelectorAll('span[dir="auto"]'))
			.filter(s => {
				const t = s.innerText;
				return t && (t.includes('Driven ') || t.includes(' transmission') ||
					t.includes('Exterior color') || t.includes('Fuel type') || t.includes(' owners'));
			})
			.map(s => s.innerText);

		return { description, aboutItems };
	}`

	jsScrapeMarketplace = `() => {
		const ads = [];
		const seen = new Set();
		const links = document.querySelectorAll("a[href^='/marketplace/item/']");
		links.forEach(link => {
			const url = link.href;
			const match = url.match(/\/item\/(\d+)/);
			if (!match) return;
			const id = match[1];

			if (seen.has(id)) return;
			seen.add(id);

			const texts = Array.from(link.querySelectorAll('span[dir="auto"]'))
				.map(span => span.innerText)
				.filter(t => t && t.trim() !== '');

			if (texts.length >= 2) {
				ads.push({
					id: id,
					url: url,
					texts: texts
				});
			}
		});
		return ads;
	}`
)

// FacebookScrapeConfig holds the parameters for a Facebook Marketplace scrape.
type FacebookScrapeConfig struct {
	City         string
	Category     string
	RadiusKm     int
	FilterBrands []string
}

// ScrapeListingDetail visits an individual Facebook Marketplace listing page to
// extract the full seller description (including "See more" expansion) and
// structured vehicle data that isn't available in the feed view.
func ScrapeListingDetail(ctx context.Context, logger *slog.Logger, page playwright.Page, adURL string) (string, error) {
	_, err := page.Goto(adURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(20000),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		return "", fmt.Errorf("failed to navigate to listing: %w", err)
	}

	// Wait for the description area to load
	_, _ = page.WaitForSelector("span[dir='auto']", playwright.PageWaitForSelectorOptions{
		Timeout: playwright.Float(10000),
		State:   playwright.WaitForSelectorStateVisible,
	})

	result, err := page.Evaluate(jsExtractListingDetail)
	if err != nil {
		return "", fmt.Errorf("failed to extract listing detail: %w", err)
	}

	data, ok := result.(map[string]interface{})
	if !ok {
		return "", nil
	}

	description, _ := data["description"].(string)

	// Append structured data as context
	if items, ok := data["aboutItems"].([]interface{}); ok {
		var aboutParts []string
		for _, item := range items {
			if s, ok := item.(string); ok {
				aboutParts = append(aboutParts, s)
			}
		}
		if len(aboutParts) > 0 {
			description += "\n" + strings.Join(aboutParts, ". ")
		}
	}

	return strings.TrimSpace(description), nil
}

// ScrapeMarketplace navigates to Facebook Marketplace for the given config
// and extracts ad data using JavaScript evaluation on the rendered page.
func ScrapeMarketplace(ctx context.Context, logger *slog.Logger, pm *BrowserManager, cfg *FacebookScrapeConfig) ([]models.ScrapedAd, error) {
	if cfg.City == "" {
		return nil, fmt.Errorf("scrape config has no city configured")
	}

	category := cfg.Category
	if category == "" {
		category = "Vehicles"
	}
	radiusKm := cfg.RadiusKm
	if radiusKm <= 0 {
		radiusKm = 500
	}

	bCtx, err := pm.NewContext()
	if err != nil {
		return nil, fmt.Errorf("failed to create playwright context: %w", err)
	}
	defer bCtx.Close()

	page, err := bCtx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("failed to create playwright page: %w", err)
	}

	var allAds []models.ScrapedAd
	seenIDs := make(map[string]bool)

	targetURL, err := BuildMarketplaceURL(cfg.City, category, radiusKm)
	if err != nil {
		return nil, fmt.Errorf("failed to build URL for %q: %w", cfg.City, err)
	}

	logger.Info("Navigating to marketplace", "city", cfg.City, "url", targetURL)

	_, err = page.Goto(targetURL, playwright.PageGotoOptions{
		Timeout:   playwright.Float(30000),
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to navigate for %s: %w", cfg.City, err)
	}

	adLinksLoc := page.Locator("a[href^='/marketplace/item/']")
	err = adLinksLoc.First().WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(20000),
		State:   playwright.WaitForSelectorStateVisible,
	})
	if err != nil {
		currentURL := page.URL()
		if strings.Contains(currentURL, "login") || strings.Contains(currentURL, "checkpoint") {
			return nil, fmt.Errorf("soft block detected for %s: redirected to %s", cfg.City, currentURL)
		}
		logger.Warn("Timeout waiting for ads", "city", cfg.City, "url", currentURL)
		return allAds, nil
	}

	closeBtn := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Close"})
	if count, _ := closeBtn.Count(); count > 0 {
		_ = closeBtn.First().Click(playwright.LocatorClickOptions{
			Timeout: playwright.Float(2000),
		})
	}

	result, err := page.Evaluate(jsScrapeMarketplace)
	if err != nil {
		return nil, fmt.Errorf("failed to evaluate JS on marketplace page: %w", err)
	}

	rawAds, ok := result.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected return type from JS evaluation")
	}

	for _, item := range rawAds {
		adMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		scrapedAd, id, ok := processRawAd(adMap, cfg)
		if !ok || seenIDs[id] {
			continue
		}
		seenIDs[id] = true
		allAds = append(allAds, *scrapedAd)
	}

	logger.Info("Extracted ads", "count", len(allAds), "city", cfg.City)

	return allAds, nil
}

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
