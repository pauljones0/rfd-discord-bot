package scraper

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const hotDealsURL = "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"

// knownTwoPartTLDs is a set of common two-part TLDs.
var knownTwoPartTLDs = map[string]bool{
	"co.uk": true, "com.au": true, "co.jp": true, "co.nz": true, "com.br": true,
	"org.uk": true, "gov.uk": true, "ac.uk": true, "com.cn": true, "net.cn": true,
	"org.cn": true, "co.za": true, "com.es": true, "com.mx": true, "com.sg": true,
	"co.in": true, "ltd.uk": true, "plc.uk": true, "net.au": true, "org.au": true,
	"com.pa": true, "net.pa": true, "org.pa": true, "edu.pa": true, "gob.pa": true,
	"com.py": true, "net.py": true, "org.py": true, "edu.py": true, "gov.py": true,
}

// ScrapeHotDealsPage fetches and parses the hot deals page.
func ScrapeHotDealsPage() ([]models.DealInfo, error) {
	log.Println("Fetching RFD Hot Deals page via scraping...")

	// Load selectors from config
	_, err := LoadSelectors("config/selectors.json")
	if err != nil {
		log.Printf("Warning: Failed to load selectors from config: %v. Using defaults.", err)
	}

	// Retry logic with exponential backoff
	maxRetries := 3
	var scrapedDeals []models.DealInfo


	for i := 0; i <= maxRetries; i++ {
		scrapedDeals, err = attemptScrape(hotDealsURL)
		if err == nil {
			break
		}
		if i < maxRetries {
			backoffDuration := time.Duration(1<<i) * time.Second
			log.Printf("[ALERT] Scraping attempt %d failed: %v. Retrying in %v...", i+1, err, backoffDuration)
			time.Sleep(backoffDuration)
		}
	}

	if err != nil {
		log.Printf("[ALERT] Critical error scraping hot deals page after %d attempts: %v", maxRetries+1, err)
		return nil, fmt.Errorf("failed to scrape hot deals page: %w", err)
	}

	return scrapedDeals, nil
}

func attemptScrape(url string) ([]models.DealInfo, error) {
	log.Printf("Scraping hot deals page: %s", url)
	doc, err := fetchHTMLContent(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch or parse hot deals page %s: %w", url, err)
	}

	selectors := GetCurrentSelectors()
	listSelectors := selectors.HotDealsList

	// Find all topic list items on the page
	// CSS Selector: li.topic
	// This selects every list item with class "topic", representing a deal thread
	if doc.Find(listSelectors.Container.Item).Length() == 0 {
		return nil, fmt.Errorf("no '%s' elements found on %s. Potential block or page structure change", listSelectors.Container.Item, url)
	}

	var deals []models.DealInfo
	var allTopics []*goquery.Selection
	doc.Find(listSelectors.Container.Item).Each(func(_ int, s *goquery.Selection) {
		allTopics = append(allTopics, s)
	})

	var nonStickyTopics []*goquery.Selection
	for _, s := range allTopics {
		if !s.Is(listSelectors.Container.IgnoreModifier) {
			nonStickyTopics = append(nonStickyTopics, s)
		}
	}
	// log.Printf("DEBUG: Found %d total 'li.topic' elements, %d non-sticky/non-sponsored.", len(allTopics), len(nonStickyTopics))

	for _, s := range nonStickyTopics {
		var deal models.DealInfo
		var parseErrors []string

		// 1. Posted Time
		timeSelection := s.Find(listSelectors.Elements.PostedTime)
		if timeSelection.Length() > 0 {
			// Find the actual <time> element if the selector is a container
			actualTime := timeSelection
			if !timeSelection.Is("time") {
				actualTime = timeSelection.Find("time").First()
			}

			if actualTime.Length() > 0 {
				deal.PostedTime = strings.TrimSpace(actualTime.Text())
				datetimeStr, exists := actualTime.Attr("datetime")
				if exists {
					deal.PostedTime = datetimeStr
					parsedTime, err := time.Parse(time.RFC3339, datetimeStr)
					if err == nil {
						deal.PublishedTimestamp = parsedTime
					} else {
						parseErrors = append(parseErrors, fmt.Sprintf("failed to parse datetime string '%s': %v", datetimeStr, err))
					}
				}
			} else {
				deal.PostedTime = strings.TrimSpace(timeSelection.Text())
			}
		} else {
			parseErrors = append(parseErrors, "posted time element not found")
		}

		// 2. Thread Title Link & Text
		titleLinkSelection := s.Find(listSelectors.Elements.TitleLink)
		if titleLinkSelection.Length() > 0 {
			// Ensure we have the actual <a> tag
			actualLink := titleLinkSelection
			if !titleLinkSelection.Is("a") {
				actualLink = titleLinkSelection.Find("a").First()
			}

			if actualLink.Length() > 0 {
				deal.Title = strings.TrimSpace(actualLink.Text())
				postURL, exists := actualLink.Attr("href")
				if exists {
					if strings.HasPrefix(postURL, "/") {
						deal.PostURL = "https://forums.redflagdeals.com" + postURL
					} else {
						deal.PostURL = postURL
					}
					if deal.PostURL != "" {
						normalizedURL, normErr := normalizePostURL(deal.PostURL)
						if normErr == nil {
							deal.PostURL = normalizedURL
						}
					}
				}
			} else {
				parseErrors = append(parseErrors, "title link <a> not found within title selection")
			}
		} else {
			parseErrors = append(parseErrors, "title/post URL element not found")
		}

		// 3. Author Profile Link
		authorSelection := s.Find(listSelectors.Elements.AuthorLink)
		if authorSelection.Length() > 0 {
			// Handle cases where selector is the link or a container
			actualLink := authorSelection
			if !authorSelection.Is("a") {
				actualLink = authorSelection.Find("a").First()
			}

			if actualLink.Length() > 0 {
				authorURL, exists := actualLink.Attr("href")
				if exists {
					if strings.HasPrefix(authorURL, "/") {
						deal.AuthorURL = "https://forums.redflagdeals.com" + authorURL
					} else {
						deal.AuthorURL = authorURL
					}
				}

				authorNameSelection := actualLink.Find(listSelectors.Elements.AuthorName)
				if authorNameSelection.Length() > 0 {
					deal.AuthorName = strings.TrimSpace(authorNameSelection.Text())
				} else {
					deal.AuthorName = strings.TrimSpace(actualLink.Text())
				}
			}
		}

		// 5. Thread Image URL
		imgSelection := s.Find(listSelectors.Elements.ThreadImage)
		if imgSelection.Length() > 0 {
			src, exists := imgSelection.Attr("src")
			if exists {
				deal.ThreadImageURL = src
			}
		}

		// 6. Like Count
		likeCountSelection := s.Find(listSelectors.Elements.LikeCount)
		if likeCountSelection.Length() > 0 {
			deal.LikeCount = safeAtoi(parseSignedNumericString(likeCountSelection.Text()))
		}

		// 7. Comment Count
		commentCountSelection := s.Find(listSelectors.Elements.CommentCount)
		if commentCountSelection.Length() > 0 {
			deal.CommentCount = safeAtoi(cleanNumericString(commentCountSelection.Text()))
		} else {
			// Try fallback
			fallbackCommentCountSelection := s.Find(listSelectors.Elements.CommentCountFallback)
			if fallbackCommentCountSelection.Length() > 0 {
				deal.CommentCount = safeAtoi(cleanNumericString(fallbackCommentCountSelection.Text()))
			}
		}

		// 8. View Count
		viewCountSelection := s.Find(listSelectors.Elements.ViewCount)
		if viewCountSelection.Length() > 0 {
			deal.ViewCount = safeAtoi(cleanNumericString(viewCountSelection.Text()))
		}

		if deal.PostURL != "" {
			actualURL, detailErr := scrapeDealDetailPage(deal.PostURL)
			if detailErr == nil {
				deal.ActualDealURL = actualURL
				if deal.ActualDealURL != "" {
					cleanedURL, changed := cleanReferralLink(deal.ActualDealURL)
					if changed {
						deal.ActualDealURL = cleanedURL
					}
				}
				// NOTE: Rick Roll fallback removed here as per user request
				if deal.ActualDealURL == "" {
					log.Printf("ActualDealURL for %s was empty after parsing.", deal.PostURL)
				}
			}
		}

		if len(parseErrors) > 0 {
			log.Printf("Encountered %d parsing issues for deal '%s' (URL: %s): %s", len(parseErrors), deal.Title, deal.PostURL, strings.Join(parseErrors, "; "))
		}
		deals = append(deals, deal)
	}

	return deals, nil
}

func scrapeDealDetailPage(dealURL string) (string, error) {
	doc, err := fetchHTMLContent(dealURL)
	if err != nil {
		return "", err
	}

	selectors := GetCurrentSelectors()
	detailSelectors := selectors.DealDetails

	var urlA, urlB string
	var existsA, existsB bool

	// Selector A
	// CSS Selector: .get-deal-button
	// Looks for the explicit "Get Deal" button class
	getDealButton := doc.Find(detailSelectors.PrimaryLink)
	if getDealButton.Length() > 0 {
		href, found := getDealButton.Attr("href")
		if found && strings.TrimSpace(href) != "" {
			urlA = strings.TrimSpace(href)
			existsA = true
		}
	}

	// Selector B
	// CSS Selector: a.autolinker_link:nth-child(1)
	// Fallback looking for auto-linked URLs if the button is missing
	autolinkerLink := doc.Find(detailSelectors.FallbackLink)
	if autolinkerLink.Length() > 0 {
		href, found := autolinkerLink.Attr("href")
		if found && strings.TrimSpace(href) != "" {
			trimmedHref := strings.TrimSpace(href)
			if (strings.HasPrefix(trimmedHref, "http://") || strings.HasPrefix(trimmedHref, "https://")) &&
				!strings.Contains(trimmedHref, "redflagdeals.com") {
				urlB = trimmedHref
				existsB = true
			}
		}
	}

	if existsA && existsB {
		if urlA == urlB {
			return urlA, nil
		}
		return urlA, nil // Prefer A
	} else if existsA {
		return urlA, nil
	} else if existsB {
		return urlB, nil
	}

	return "", nil
}

func fetchHTMLContent(urlStr string) (*goquery.Document, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL %s: %w", urlStr, err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid URL scheme %s: only http and https allowed", parsedURL.Scheme)
	}

	hostname := parsedURL.Hostname()
	allowed := false
	allowedDomains := []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com"}
	for _, domain := range allowedDomains {
		if hostname == domain {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("security violation: URL hostname %s is not in allowlist", hostname)
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	res, err := client.Get(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", urlStr, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status code %d", urlStr, res.StatusCode)
	}

	return goquery.NewDocumentFromReader(res.Body)
}

func normalizePostURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}
	parsedURL.Scheme = "https"
	if strings.HasPrefix(parsedURL.Host, "www.") {
		if parsedURL.Host == "www.forums.redflagdeals.com" || parsedURL.Host == "www.redflagdeals.com" {
			parsedURL.Host = strings.TrimPrefix(parsedURL.Host, "www.")
		}
	}
	if parsedURL.Host == "redflagdeals.com" {
		parsedURL.Host = "forums.redflagdeals.com"
	}
	if len(parsedURL.Path) > 1 && strings.HasSuffix(parsedURL.Path, "/") {
		parsedURL.Path = parsedURL.Path[:len(parsedURL.Path)-1]
	}
	queryParams := parsedURL.Query()
	utmParams := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "rfd_sk", "sd", "sk"}
	for _, param := range utmParams {
		if queryParams.Has(param) {
			queryParams.Del(param)
		}
	}
	parsedURL.RawQuery = queryParams.Encode()
	return parsedURL.String(), nil
}

func cleanReferralLink(rawUrl string) (string, bool) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl, false
	}
	// Simplified logic for brevity, copying the robust one is better
	// Best Buy specific constants
	const newBestBuyPrefix = "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
	bestBuyRegex := regexp.MustCompile(`^https://bestbuyca\.o93x\.net/c/\d+/\d+/\d+\?u=`)

	switch {
	case parsedUrl.Host == "click.linksynergy.com":
		murlParam := parsedUrl.Query().Get("murl")
		if murlParam != "" {
			decodedMURL, decodeErr := url.QueryUnescape(murlParam)
			if decodeErr == nil {
				return decodedMURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "go.redirectingat.com":
		urlParam := parsedUrl.Query().Get("url")
		if urlParam != "" {
			decodedDestURL, decodeErr := url.QueryUnescape(urlParam)
			if decodeErr == nil {
				return decodedDestURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "bestbuyca.o93x.net" && bestBuyRegex.MatchString(rawUrl):
		uIndex := strings.Index(rawUrl, "?u=")
		if uIndex == -1 {
			return rawUrl, false
		}
		productURLPart := rawUrl[uIndex+len("?u="):]
		cleanedURL := newBestBuyPrefix + productURLPart
		return cleanedURL, true

	case strings.Contains(parsedUrl.Host, "amazon."):
		queryParams := parsedUrl.Query()
		originalTag := queryParams.Get("tag")
		const newTag = "beauahrens0d-20"
		tagModified := false

		if queryParams.Has("tag") {
			if originalTag != newTag {
				queryParams.Del("tag")
				queryParams.Set("tag", newTag)
				tagModified = true
			}
		} else {
			queryParams.Set("tag", newTag)
			tagModified = true
		}
		if tagModified {
			parsedUrl.RawQuery = queryParams.Encode()
			return parsedUrl.String(), true
		}
		return parsedUrl.String(), tagModified

	default:
		return rawUrl, false
	}
}

func safeAtoi(s string) int {
	s = strings.ReplaceAll(s, ",", "")
	i, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return i
}

var nonNumericRegex = regexp.MustCompile(`[^\d]`)

func cleanNumericString(s string) string {
	return nonNumericRegex.ReplaceAllString(s, "")
}

var extractSignedNumberRegex = regexp.MustCompile(`-?\d+`)

func parseSignedNumericString(s string) string {
	return extractSignedNumberRegex.FindString(s)
}
