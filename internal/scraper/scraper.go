package scraper

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const hotDealsURL = "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"

type Scraper interface {
	ScrapeHotDealsPage(ctx context.Context) ([]models.DealInfo, error)
}

type Client struct {
	httpClient *http.Client
	config     *config.Config
}

func New(cfg *config.Config) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

func (c *Client) ScrapeHotDealsPage(ctx context.Context) ([]models.DealInfo, error) {
	log.Println("Fetching RFD Hot Deals page via scraping...")

	// Retry logic with exponential backoff
	maxRetries := 3
	var scrapedDeals []models.DealInfo
	var err error

	for i := 0; i <= maxRetries; i++ {
		scrapedDeals, err = c.attemptScrape(ctx, hotDealsURL)
		if err == nil {
			break
		}
		if i < maxRetries {
			backoffDuration := time.Duration(1<<i) * time.Second
			log.Printf("[ALERT] Scraping attempt %d failed: %v. Retrying in %v...", i+1, err, backoffDuration)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoffDuration):
			}
		}
	}

	if err != nil {
		log.Printf("[ALERT] Critical error scraping hot deals page after %d attempts: %v", maxRetries+1, err)
		return nil, fmt.Errorf("failed to scrape hot deals page: %w", err)
	}

	return scrapedDeals, nil
}

func (c *Client) attemptScrape(ctx context.Context, url string) ([]models.DealInfo, error) {
	log.Printf("Scraping hot deals page: %s", url)
	doc, err := c.fetchHTMLContent(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch or parse hot deals page %s: %w", url, err)
	}

	selectors := GetCurrentSelectors()
	listSelectors := selectors.HotDealsList

	if doc.Find(listSelectors.Container.Item).Length() == 0 {
		return nil, fmt.Errorf("no '%s' elements found on %s. Potential block or page structure change", listSelectors.Container.Item, url)
	}

	var deals []models.DealInfo
	// Phase 1: Parse the list page synchronously
	doc.Find(listSelectors.Container.Item).Each(func(_ int, s *goquery.Selection) {
		if s.Is(listSelectors.Container.IgnoreModifier) {
			return
		}

		var deal models.DealInfo
		var parseErrors []string

		// 1. Posted Time
		timeSelection := s.Find(listSelectors.Elements.PostedTime)
		if timeSelection.Length() > 0 {
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
						normalizedURL, normErr := util.NormalizeURL(deal.PostURL)
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
			deal.LikeCount = util.SafeAtoi(util.ParseSignedNumericString(likeCountSelection.Text()))
		}

		// 7. Comment Count
		commentCountSelection := s.Find(listSelectors.Elements.CommentCount)
		if commentCountSelection.Length() > 0 {
			deal.CommentCount = util.SafeAtoi(util.CleanNumericString(commentCountSelection.Text()))
		} else {
			fallbackCommentCountSelection := s.Find(listSelectors.Elements.CommentCountFallback)
			if fallbackCommentCountSelection.Length() > 0 {
				deal.CommentCount = util.SafeAtoi(util.CleanNumericString(fallbackCommentCountSelection.Text()))
			}
		}

		// 8. View Count
		viewCountSelection := s.Find(listSelectors.Elements.ViewCount)
		if viewCountSelection.Length() > 0 {
			deal.ViewCount = util.SafeAtoi(util.CleanNumericString(viewCountSelection.Text()))
		}

		if len(parseErrors) > 0 {
			log.Printf("Encountered %d parsing issues for deal '%s' (URL: %s): %s", len(parseErrors), deal.Title, deal.PostURL, strings.Join(parseErrors, "; "))
		}
		deals = append(deals, deal)
	})

	// Phase 2: Parallelize detail fetching
	type detailResult struct {
		index int
		url   string
		err   error
	}

	// Buffered channel for semaphore pattern to limit concurrency
	concurrencyLimit := 5
	semaphore := make(chan struct{}, concurrencyLimit)
	// Channel to collect results
	results := make(chan detailResult, len(deals))

	// Launch goroutines
	activeGoroutines := 0
	for i, d := range deals {
		if d.PostURL == "" {
			continue // Skip deals without URLs
		}

		activeGoroutines++
		go func(index int, urlStr string) {
			// Acquire semaphore
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- detailResult{index: index, err: ctx.Err()}
				return
			}
			defer func() { <-semaphore }() // Release

			actualURL, err := c.scrapeDealDetailPage(ctx, urlStr)
			results <- detailResult{index: index, url: actualURL, err: err}
		}(i, d.PostURL)
	}

	// Phase 3: Collect results
	for i := 0; i < activeGoroutines; i++ {
		select {
		case res := <-results:
			if res.err != nil {
				// Don't fail the whole batch, just log
				log.Printf("Warning: Failed to scrape detail page for deal %s: %v", deals[res.index].PostURL, res.err)
				continue
			}
			deals[res.index].ActualDealURL = res.url
			if deals[res.index].ActualDealURL != "" {
				cleanedURL, changed := util.CleanReferralLink(deals[res.index].ActualDealURL, c.config.AmazonAffiliateTag)
				if changed {
					deals[res.index].ActualDealURL = cleanedURL
				}
			}
			if deals[res.index].ActualDealURL == "" {
				log.Printf("ActualDealURL for %s was empty after parsing.", deals[res.index].PostURL)
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return deals, nil
}

func (c *Client) scrapeDealDetailPage(ctx context.Context, dealURL string) (string, error) {
	doc, err := c.fetchHTMLContent(ctx, dealURL)
	if err != nil {
		return "", err
	}

	selectors := GetCurrentSelectors()
	detailSelectors := selectors.DealDetails

	var urlA, urlB string
	var existsA, existsB bool

	getDealButton := doc.Find(detailSelectors.PrimaryLink)
	if getDealButton.Length() > 0 {
		href, found := getDealButton.Attr("href")
		if found && strings.TrimSpace(href) != "" {
			urlA = strings.TrimSpace(href)
			existsA = true
		}
	}

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

	if existsA {
		return urlA, nil
	} else if existsB {
		return urlB, nil
	}

	return "", nil
}

func (c *Client) fetchHTMLContent(ctx context.Context, urlStr string) (*goquery.Document, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL %s: %w", urlStr, err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid URL scheme %s: only http and https allowed", parsedURL.Scheme)
	}

	hostname := parsedURL.Hostname()
	allowed := false
	for _, domain := range c.config.AllowedDomains {
		if hostname == domain {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("security violation: URL hostname %s is not in allowlist", hostname)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for URL %s: %w", urlStr, err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", urlStr, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status code %d", urlStr, res.StatusCode)
	}

	return goquery.NewDocumentFromReader(res.Body)
}
