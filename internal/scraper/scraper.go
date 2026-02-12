package scraper

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const (
	hotDealsURL = "https://forums.redflagdeals.com/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"
	rfdBase     = "https://forums.redflagdeals.com"
)

type Scraper interface {
	ScrapeHotDealsPage(ctx context.Context) ([]models.DealInfo, error)
}

type Client struct {
	httpClient *http.Client
	config     *config.Config
	selectors  SelectorConfig
	baseURL    string // overrides hotDealsURL when set (used for testing)
}

func New(cfg *config.Config, selectors SelectorConfig) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		config:     cfg,
		selectors:  selectors,
	}
}

// NewWithBaseURL creates a scraper Client that uses the given base URL
// instead of the default RFD URL. Useful for integration tests.
func NewWithBaseURL(cfg *config.Config, selectors SelectorConfig, baseURL string) *Client {
	c := New(cfg, selectors)
	c.baseURL = baseURL
	return c
}

func (c *Client) ScrapeHotDealsPage(ctx context.Context) ([]models.DealInfo, error) {
	log.Println("Fetching RFD Hot Deals page via scraping...")

	targetURL := hotDealsURL
	if c.baseURL != "" {
		targetURL = c.baseURL + "/hot-deals"
	}

	maxRetries := 3
	var scrapedDeals []models.DealInfo
	var err error

	for i := 0; i <= maxRetries; i++ {
		scrapedDeals, err = c.attemptScrape(ctx, targetURL)
		if err == nil {
			break
		}
		if i < maxRetries {
			backoff := time.Duration(1<<i) * time.Second
			log.Printf("[ALERT] Scraping attempt %d failed: %v. Retrying in %v...", i+1, err, backoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}
	}

	if err != nil {
		log.Printf("[ALERT] Critical error scraping hot deals page after %d attempts: %v", maxRetries+1, err)
		return nil, fmt.Errorf("failed to scrape hot deals page: %w", err)
	}

	return scrapedDeals, nil
}

// resolveLink finds an <a> element within the selection (or the selection itself),
// returning the href (resolved to absolute if relative) and text content.
func resolveLink(s *goquery.Selection, selector string) (href, text string) {
	sel := s.Find(selector)
	if sel.Length() == 0 {
		return "", ""
	}

	link := sel
	if !sel.Is("a") {
		link = sel.Find("a").First()
	}
	if link.Length() == 0 {
		return "", ""
	}

	text = strings.TrimSpace(link.Text())
	rawHref, exists := link.Attr("href")
	if !exists {
		return "", text
	}

	href = rawHref
	if strings.HasPrefix(href, "/") {
		href = rfdBase + href
	}
	return href, text
}

func (c *Client) attemptScrape(ctx context.Context, targetURL string) ([]models.DealInfo, error) {
	log.Printf("Scraping hot deals page: %s", targetURL)
	doc, err := c.fetchHTMLContent(ctx, targetURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch or parse hot deals page %s: %w", targetURL, err)
	}

	ls := c.selectors.HotDealsList

	if doc.Find(ls.Container.Item).Length() == 0 {
		return nil, fmt.Errorf("no '%s' elements found on %s. Potential block or page structure change", ls.Container.Item, targetURL)
	}

	var deals []models.DealInfo
	doc.Find(ls.Container.Item).Each(func(_ int, s *goquery.Selection) {
		if s.Is(ls.Container.IgnoreModifier) {
			return
		}

		deal := c.parseDealFromSelection(s, ls.Elements)
		deals = append(deals, deal)
	})

	// Fetch detail pages concurrently
	c.fetchDealDetails(ctx, deals)

	return deals, nil
}

func (c *Client) parseDealFromSelection(s *goquery.Selection, elems ListElements) models.DealInfo {
	var deal models.DealInfo
	var parseErrors []string

	// Posted Time
	timeSelection := s.Find(elems.PostedTime)
	if timeSelection.Length() > 0 {
		actualTime := timeSelection
		if !timeSelection.Is("time") {
			actualTime = timeSelection.Find("time").First()
		}
		if actualTime.Length() > 0 {
			deal.PostedTime = strings.TrimSpace(actualTime.Text())
			if datetimeStr, exists := actualTime.Attr("datetime"); exists {
				deal.PostedTime = datetimeStr
				if parsed, err := time.Parse(time.RFC3339, datetimeStr); err == nil {
					deal.PublishedTimestamp = parsed
				} else {
					parseErrors = append(parseErrors, fmt.Sprintf("failed to parse datetime '%s': %v", datetimeStr, err))
				}
			}
		} else {
			deal.PostedTime = strings.TrimSpace(timeSelection.Text())
		}
	} else {
		parseErrors = append(parseErrors, "posted time element not found")
	}

	// Title & Post URL
	postURL, title := resolveLink(s, elems.TitleLink)
	if title != "" {
		deal.Title = title
		if postURL != "" {
			normalized, err := util.NormalizeURL(postURL)
			if err == nil {
				postURL = normalized
			}
		}
		deal.PostURL = postURL
	} else {
		parseErrors = append(parseErrors, "title/post URL element not found")
	}

	// Author
	authorURL, _ := resolveLink(s, elems.AuthorLink)
	deal.AuthorURL = authorURL
	if authorURL != "" {
		// Try to find specific author name element within the author link
		authorSel := s.Find(elems.AuthorLink)
		if !authorSel.Is("a") {
			authorSel = authorSel.Find("a").First()
		}
		if authorSel.Length() > 0 {
			nameSel := authorSel.Find(elems.AuthorName)
			if nameSel.Length() > 0 {
				deal.AuthorName = strings.TrimSpace(nameSel.Text())
			} else {
				deal.AuthorName = strings.TrimSpace(authorSel.Text())
			}
		}
	}

	// Thread Image
	imgSelection := s.Find(elems.ThreadImage)
	if imgSelection.Length() > 0 {
		if src, exists := imgSelection.Attr("src"); exists {
			deal.ThreadImageURL = src
		}
	}

	// Like Count
	likeCountSelection := s.Find(elems.LikeCount)
	if likeCountSelection.Length() > 0 {
		deal.LikeCount = util.SafeAtoi(util.ParseSignedNumericString(likeCountSelection.First().Text()))
	}

	// Comment Count (with fallback)
	commentCountSelection := s.Find(elems.CommentCount)
	if commentCountSelection.Length() > 0 {
		deal.CommentCount = util.SafeAtoi(util.CleanNumericString(commentCountSelection.First().Text()))
	} else {
		fallback := s.Find(elems.CommentCountFallback)
		if fallback.Length() > 0 {
			deal.CommentCount = util.SafeAtoi(util.CleanNumericString(fallback.First().Text()))
		}
	}

	// View Count
	viewCountSelection := s.Find(elems.ViewCount)
	if viewCountSelection.Length() > 0 {
		deal.ViewCount = util.SafeAtoi(util.CleanNumericString(viewCountSelection.First().Text()))
	}

	if len(parseErrors) > 0 {
		log.Printf("Parsing issues for deal '%s' (URL: %s): %s", deal.Title, deal.PostURL, strings.Join(parseErrors, "; "))
	}
	return deal
}

func (c *Client) fetchDealDetails(ctx context.Context, deals []models.DealInfo) {
	type detailResult struct {
		index int
		url   string
		err   error
	}

	concurrencyLimit := 5
	semaphore := make(chan struct{}, concurrencyLimit)
	results := make(chan detailResult, len(deals))

	var wg sync.WaitGroup
	for i, d := range deals {
		if d.PostURL == "" {
			continue
		}

		wg.Add(1)
		go func(index int, urlStr string) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- detailResult{index: index, err: ctx.Err()}
				return
			}
			defer func() { <-semaphore }()

			actualURL, err := c.scrapeDealDetailPage(ctx, urlStr)
			results <- detailResult{index: index, url: actualURL, err: err}
		}(i, d.PostURL)
	}

	// Close results channel once all goroutines are done
	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		if res.err != nil {
			log.Printf("Warning: Failed to scrape detail page for %s: %v", deals[res.index].PostURL, res.err)
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
	}
}

func (c *Client) scrapeDealDetailPage(ctx context.Context, dealURL string) (string, error) {
	doc, err := c.fetchHTMLContent(ctx, dealURL)
	if err != nil {
		return "", err
	}

	ds := c.selectors.DealDetails

	// Try primary link first
	if btn := doc.Find(ds.PrimaryLink); btn.Length() > 0 {
		if href, found := btn.Attr("href"); found && strings.TrimSpace(href) != "" {
			return strings.TrimSpace(href), nil
		}
	}

	// Fallback link
	if link := doc.Find(ds.FallbackLink); link.Length() > 0 {
		if href, found := link.Attr("href"); found {
			trimmed := strings.TrimSpace(href)
			if (strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")) &&
				!strings.Contains(trimmed, "redflagdeals.com") {
				return trimmed, nil
			}
		}
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

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

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
