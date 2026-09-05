package scraper

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const (
	rfdListStandardMaxRetries = 3
	rfdListDNSMaxRetries      = 6
	rfdDetailConcurrency      = 2
	rfdDetailMaxRetries       = 2
	rfdPOWMaxAttempts         = 10_000_000
	rfdPOWMaxDifficulty       = 6
	rfdPOWChallengeBodyLimit  = 128 * 1024
)

var rfdPOWFieldPattern = regexp.MustCompile(`(?m)(challenge_nonce|challenge_hmac|difficulty|difficulty_char|issued_at|cookie_duration)\s*:\s*['"]([^'"]+)['"]`)

type rfdPOWChallenge struct {
	nonce          string
	hmac           string
	difficulty     int
	difficultyChar string
	issuedAt       string
	cookieDuration time.Duration
}

type Client struct {
	httpClient *http.Client
	config     *config.Config
	selectors  SelectorConfig
	profile    browserProfile
	baseURL    string // overrides hotDealsURL when set (used for testing)
}

func New(cfg *config.Config, selectors SelectorConfig) *Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		slog.Warn("Could not initialize RFD cookie jar", "error", err)
	}
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second, Jar: jar},
		config:     cfg,
		selectors:  selectors,
		profile:    randomProfile(),
	}
}

// NewWithBaseURL creates a scraper Client that uses the given base URL
// instead of the default RFD URL. Useful for integration tests.
func NewWithBaseURL(cfg *config.Config, selectors SelectorConfig, baseURL string) *Client {
	c := New(cfg, selectors)
	c.baseURL = baseURL
	return c
}

func (c *Client) ScrapeDealList(ctx context.Context) ([]models.DealInfo, error) {
	targetURL := c.config.RFDBaseURL + "/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"
	if c.baseURL != "" {
		targetURL = c.baseURL + "/hot-deals"
	}

	slog.Info("Scraping RFD Hot Deals list...", "processor", "rfd", "url", targetURL)

	var scrapedDeals []models.DealInfo
	start := time.Now()

	err := util.RetryWithBackoff(ctx, rfdListDNSMaxRetries, func(attempt int) error {
		if attempt > 0 {
			slog.Warn("Scraping list attempt failed, retrying", "processor", "rfd", "attempt", attempt)
		}
		var scrapeErr error
		scrapedDeals, scrapeErr = c.attemptScrapeList(ctx, targetURL)
		if shouldStopRFDListRetry(attempt, scrapeErr) {
			return util.PermanentError(scrapeErr)
		}
		if attempt == rfdListStandardMaxRetries && isTransientDNSFailure(scrapeErr) {
			slog.Warn("RFD scrape hit transient DNS failure; extending retry budget",
				"processor", "rfd",
				"attempt", attempt,
				"max_attempt", rfdListDNSMaxRetries,
				"error", scrapeErr,
			)
		}
		return scrapeErr
	})

	if err != nil {
		logger.Critical("All retry attempts failed for ScrapeDealList", "error", err)
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}

	logger.Notice("Scrape completed", "duration", time.Since(start), "deals", len(scrapedDeals))
	return scrapedDeals, nil
}

func shouldStopRFDListRetry(attempt int, err error) bool {
	return err != nil && attempt >= rfdListStandardMaxRetries && !isTransientDNSFailure(err)
}

func isTransientDNSFailure(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTemporary || dnsErr.IsTimeout {
			return true
		}
		dnsText := strings.ToLower(dnsErr.Err + " " + dnsErr.Server)
		return strings.Contains(dnsText, "server misbehaving") ||
			strings.Contains(dnsText, "temporary failure") ||
			strings.Contains(dnsText, "try again")
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "lookup ") && (strings.Contains(errText, "server misbehaving") ||
		strings.Contains(errText, "temporary failure") ||
		strings.Contains(errText, "try again"))
}

// resolveLink finds an <a> element within the selection (or the selection itself),
// returning the href (resolved to absolute if relative) and text content.
func (c *Client) resolveLink(s *goquery.Selection, selector string) (href, text string) {
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
		href = c.config.RFDBaseURL + href
	}
	return href, text
}

func (c *Client) attemptScrapeList(ctx context.Context, targetURL string) ([]models.DealInfo, error) {
	slog.Info("Scraping hot deals page", "processor", "rfd", "url", targetURL)
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

		if ls.Elements.TitleText != "" && s.Find(ls.Elements.TitleText).Length() == 0 {
			return
		}

		deal := c.parseDealFromSelection(s, ls.Elements)
		deals = append(deals, deal)
	})

	return deals, nil
}

func (c *Client) parseDealFromSelection(s *goquery.Selection, elems ListElements) models.DealInfo {
	var deal models.DealInfo
	var thread models.ThreadContext
	var parseErrors []string

	// Published Timestamp from <time datetime="...">
	timeSelection := s.Find(elems.PostedTime)
	if timeSelection.Length() > 0 {
		actualTime := timeSelection
		if !timeSelection.Is("time") {
			actualTime = timeSelection.Find("time").First()
		}
		if actualTime.Length() > 0 {
			if datetimeStr, exists := actualTime.Attr("datetime"); exists {
				if parsed, err := time.Parse(time.RFC3339, datetimeStr); err == nil {
					deal.PublishedTimestamp = parsed
				} else {
					parseErrors = append(parseErrors, fmt.Sprintf("failed to parse datetime '%s': %v", datetimeStr, err))
				}
			}
		}
	} else {
		parseErrors = append(parseErrors, "posted time element not found")
	}

	// Title & Post URL
	postURL, title := c.resolveLink(s, elems.TitleLink)
	if elems.TitleText != "" {
		titleSel := s.Find(elems.TitleText)
		if titleSel.Length() > 0 {
			title = strings.TrimSpace(titleSel.Text())
		} else {
			title = ""
		}
	}

	if title != "" {
		deal.Title = title
		if postURL != "" {
			normalized, err := util.NormalizeURL(postURL, c.config.AllowedDomains)
			if err == nil {
				postURL = normalized
			} else {
				slog.Warn("Failed to normalize URL, using raw URL", "processor", "rfd", "url", postURL, "error", err)
			}
		}
		deal.PostURL = postURL
		thread.PostURL = postURL
	} else {
		parseErrors = append(parseErrors, "title/post URL element not found")
	}

	// Retailer (Store)
	retailerSel := s.Find(elems.Retailer)
	if retailerSel.Length() > 0 {
		deal.Retailer = cleanRetailerName(retailerSel.First().Text())
	}
	if deal.Retailer == "" {
		if retailerAttr, exists := s.Attr("data-dealer-name"); exists {
			deal.Retailer = cleanRetailerName(retailerAttr)
		}
	}
	if deal.Retailer == "" {
		if retailerAttr, exists := s.Find("[data-dealer-name]").First().Attr("data-dealer-name"); exists {
			deal.Retailer = cleanRetailerName(retailerAttr)
		}
	}

	// Thread Image — only accept http/https URLs
	imgSelection := s.Find(elems.ThreadImage)
	if imgSelection.Length() > 0 {
		if src, exists := imgSelection.Attr("src"); exists {
			if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
				deal.ThreadImageURL = src
			}
		}
	}

	// Like Count
	likeCountSelection := s.Find(elems.LikeCount)
	if likeCountSelection.Length() > 0 {
		thread.LikeCount = util.SafeAtoi(util.ParseSignedNumericString(likeCountSelection.First().Text()))
	}

	// Comment Count (with fallback)
	commentCountSelection := s.Find(elems.CommentCount)
	if commentCountSelection.Length() > 0 {
		thread.CommentCount = util.SafeAtoi(util.CleanNumericString(commentCountSelection.First().Text()))
	} else {
		fallback := s.Find(elems.CommentCountFallback)
		if fallback.Length() > 0 {
			thread.CommentCount = util.SafeAtoi(util.CleanNumericString(fallback.First().Text()))
		}
	}

	// View Count
	if elems.ViewCount != "" {
		viewCountSelection := s.Find(elems.ViewCount)
		if viewCountSelection.Length() > 0 {
			thread.ViewCount = util.SafeAtoi(util.CleanNumericString(viewCountSelection.First().Text()))
			thread.ViewCountAvailable = true
		}
	}

	// List price/savings fallback (if available on card)
	if priceSel := s.Find(".savings"); priceSel.Length() > 0 {
		cardPrice := strings.TrimSpace(priceSel.First().Contents().Not("span").Text())
		if cardPrice != "" {
			deal.Price = cardPrice
		}
		if savingsSel := priceSel.Find("span"); savingsSel.Length() > 0 {
			deal.OriginalPrice = strings.TrimSpace(savingsSel.Text())
		}
	}

	deal.Threads = []models.ThreadContext{thread}

	if len(parseErrors) > 0 {
		slog.Warn("Parsing issues for deal", "processor", "rfd", "title", deal.Title, "url", deal.PrimaryPostURL(), "errors", strings.Join(parseErrors, "; "))
	}
	return deal
}

func (c *Client) FetchDealDetails(ctx context.Context, deals []*models.DealInfo) models.DealDetailFetchStats {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(rfdDetailConcurrency)

	var attempted atomic.Int32
	var succeeded atomic.Int32
	var failed atomic.Int32
	var notFound atomic.Int32

	for i := range deals {
		deal := deals[i] // explicit local copy for clarity in the closure
		if deal.PrimaryPostURL() == "" {
			continue
		}
		attempted.Add(1)

		g.Go(func() error {
			detail, err := c.scrapeDealDetailPageWithRetry(ctx, deal.PrimaryPostURL())
			if err != nil {
				if strings.Contains(err.Error(), "status code 404") {
					notFound.Add(1)
					markPrimaryThreadNotFound(deal)
					slog.Info("Failed to fetch detail page (404)", "processor", "rfd", "url", deal.PrimaryPostURL())
				} else {
					failed.Add(1)
					slog.Warn("Failed to fetch detail page", "processor", "rfd", "url", deal.PrimaryPostURL(), "error", err)
				}
				return nil
			}
			succeeded.Add(1)

			deal.ActualDealURL = detail.DealLink
			deal.Description = detail.Description
			deal.Comments = detail.Comments
			deal.Summary = detail.Summary
			deal.Price = detail.Price
			deal.OriginalPrice = detail.OriginalPrice
			deal.Savings = detail.Savings
			if detail.Retailer != "" {
				deal.Retailer = detail.Retailer
			}
			if detail.Category != "" {
				deal.Category = detail.Category
			}

			if deal.ActualDealURL != "" {
				slog.Debug("Original Product URL", "processor", "rfd", "url", deal.ActualDealURL)
				deal.ActualDealURL = util.CleanProductURL(deal.ActualDealURL)
				slog.Debug("Cleaned Product URL", "processor", "rfd", "url", deal.ActualDealURL)
				cleanedURL, changed := util.CleanReferralLink(deal.ActualDealURL, c.config.AmazonAffiliateTag, c.config.BestBuyAffiliatePrefix)
				if changed {
					deal.ActualDealURL = cleanedURL
				}
			} else {
				slog.Info("No external deal link found", "processor", "rfd", "postURL", deal.PrimaryPostURL())
			}
			return nil
		})
	}

	g.Wait()

	stats := models.DealDetailFetchStats{
		Requested: len(deals),
		Attempted: int(attempted.Load()),
		Succeeded: int(succeeded.Load()),
		Failed:    int(failed.Load()),
		NotFound:  int(notFound.Load()),
	}
	if stats.Failed > 0 || stats.NotFound > 0 {
		slog.Warn("FetchDealDetails summary",
			"processor", "rfd",
			"requested", stats.Requested,
			"attempted", stats.Attempted,
			"succeeded", stats.Succeeded,
			"failed", stats.Failed,
			"not_found", stats.NotFound,
		)
	}
	return stats
}

func (c *Client) scrapeDealDetailPageWithRetry(ctx context.Context, dealURL string) (dealDetailResult, error) {
	var detail dealDetailResult
	err := util.RetryWithBackoff(ctx, rfdDetailMaxRetries, func(attempt int) error {
		if attempt > 0 {
			slog.Warn("Retrying RFD detail page fetch", "processor", "rfd", "url", dealURL, "attempt", attempt)
		}
		var scrapeErr error
		detail, scrapeErr = c.scrapeDealDetailPage(ctx, dealURL)
		if scrapeErr == nil {
			return nil
		}
		if !shouldRetryRFDDetailFetch(scrapeErr) {
			return util.PermanentError(scrapeErr)
		}
		return scrapeErr
	})
	return detail, err
}

func shouldRetryRFDDetailFetch(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, status := range []string{
		"status code 429",
		"status code 500",
		"status code 502",
		"status code 503",
		"status code 504",
	} {
		if strings.Contains(text, status) {
			return true
		}
	}
	return strings.Contains(text, "client.timeout") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "server misbehaving") ||
		strings.Contains(text, "temporary failure") ||
		strings.Contains(text, "try again")
}

func markPrimaryThreadNotFound(deal *models.DealInfo) {
	if len(deal.Threads) == 0 {
		return
	}

	primaryURL := deal.PrimaryPostURL()
	for i := range deal.Threads {
		if deal.Threads[i].PostURL == primaryURL {
			deal.Threads[i].NotFound = true
			return
		}
	}
	deal.Threads[0].NotFound = true
}

func isExternalDealLink(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return false
	}
	if parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}

	return !strings.Contains(strings.ToLower(parsed.Hostname()), "redflagdeals.com")
}

// dealDetailResult holds the fields scraped from an RFD deal detail page.
type dealDetailResult struct {
	DealLink      string
	Description   string
	Comments      string
	Summary       string
	Price         string
	OriginalPrice string
	Savings       string
	Retailer      string
	Category      string
}

func (c *Client) scrapeDealDetailPage(ctx context.Context, dealURL string) (dealDetailResult, error) {
	doc, err := c.fetchHTMLContent(ctx, dealURL)
	if err != nil {
		return dealDetailResult{}, err
	}

	// 1. Get Deal Link
	ds := c.selectors.DealDetails
	var dealLink string

	// Try primary link first
	if btn := doc.Find(ds.PrimaryLink); btn.Length() > 0 {
		if href, found := btn.Attr("href"); found && strings.TrimSpace(href) != "" {
			trimmed := strings.TrimSpace(href)
			if isExternalDealLink(trimmed) {
				dealLink = trimmed
			}
		}
	}

	// Fallback link
	if dealLink == "" {
		if link := doc.Find(ds.FallbackLink); link.Length() > 0 {
			if href, found := link.Attr("href"); found {
				trimmed := strings.TrimSpace(href)
				if isExternalDealLink(trimmed) {
					dealLink = trimmed
				}
			}
		}
	}

	// No early return — continue extracting metadata (description, category, etc.)
	// even when no external deal link exists. Many RFD posts (coupons, in-store deals,
	// discussions) don't have external links but still have useful metadata.

	var retailer, category string

	// 2. Extract JSON-LD for Description and Comments
	var description, commentsStr string
	var ldPrice, ldRetailer string

	doc.Find("script[type='application/ld+json']").Each(func(i int, s *goquery.Selection) {
		text := s.Text()
		var postings []JSONLDDiscussionForumPosting
		// Try parsing as array first
		if err := json.Unmarshal([]byte(text), &postings); err == nil && len(postings) > 0 {
			for _, p := range postings {
				if p.Type == "DiscussionForumPosting" { // Case sensitive check might be needed, usually PascalCase
					description = cleanHTMLText(p.Text)

					var commentTexts []string
					for _, c := range p.Comment {
						commentTexts = append(commentTexts, fmt.Sprintf("- %s", cleanHTMLText(c.Text)))
					}
					// Truncate comments to avoid huge tokens
					maxCommentsLen := 2000
					fullComments := strings.Join(commentTexts, "\n")
					if len(fullComments) > maxCommentsLen {
						fullComments = fullComments[:maxCommentsLen] + "...(truncated)"
					}
					commentsStr = fullComments

					// Fallback from Product schema in JSON-LD
					if p.About != nil {
						if p.About.Offers != nil && p.About.Offers.Price != "" {
							ldPrice = p.About.Offers.Price
							if p.About.Offers.PriceCurrency == "CAD" {
								ldPrice = "$" + ldPrice
							}
						}
						if p.About.Brand != nil && p.About.Brand.Name != "" {
							ldRetailer = p.About.Brand.Name
						}
					}
					return // Found the main posting
				}
			}
		}
	})

	// 3. Extract Summary (if available)
	// Try finding the element by ID even if it's dynamic, sometimes it's SSR.
	summary := strings.TrimSpace(doc.Find("#rfd_topic_summary").Text())

	// 4. Extract Price and Retailer
	var price, originalPrice, savings string

	// Extract Price
	doc.Find("dt").Each(func(i int, s *goquery.Selection) {
		text := strings.TrimSpace(s.Text())
		if text == "Price:" {
			price = strings.TrimSpace(s.Next().Text())
		} else if text == "Original Price:" {
			originalPrice = strings.TrimSpace(s.Next().Text())
		} else if text == "Savings:" {
			savings = strings.TrimSpace(s.Next().Text())
		}
	})

	// JSON-LD Fallback for Price
	if price == "" && ldPrice != "" {
		price = ldPrice
	}

	// Extract Retailer and Category
	if badge := doc.Find(".retailer_badge"); badge.Length() > 0 {
		retailer = cleanRetailerName(badge.First().Text())
	}
	if retailer == "" {
		doc.Find("dt").Each(func(i int, s *goquery.Selection) {
			if strings.TrimSpace(s.Text()) == "Retailer:" {
				retailer = cleanRetailerName(s.Next().Text())
			}
		})
	}

	// JSON-LD Fallback for Retailer
	if retailer == "" && ldRetailer != "" {
		retailer = cleanRetailerName(ldRetailer)
	}

	// Extract Category
	if categoryBtn := doc.Find(ds.Category); categoryBtn.Length() > 0 {
		category = strings.TrimSpace(categoryBtn.Text())
		// Strip "Category:" prefix if present
		category = strings.TrimPrefix(category, "Category:")
		category = strings.TrimSpace(category)
	}
	if category == "" {
		doc.Find("dt").Each(func(i int, s *goquery.Selection) {
			if strings.TrimSpace(s.Text()) == "Category:" {
				category = strings.TrimSpace(s.Next().Text())
			}
		})
	}

	return dealDetailResult{
		DealLink:      dealLink,
		Description:   description,
		Comments:      commentsStr,
		Summary:       summary,
		Price:         price,
		OriginalPrice: originalPrice,
		Savings:       savings,
		Retailer:      retailer,
		Category:      category,
	}, nil
}

func cleanRetailerName(raw string) string {
	retailer := strings.TrimSpace(raw)
	retailer = strings.Join(strings.Fields(retailer), " ")
	for {
		if !strings.HasPrefix(strings.ToLower(retailer), "at ") {
			break
		}
		retailer = strings.TrimSpace(retailer[3:])
	}
	return collapseRepeatedRetailer(retailer)
}

func collapseRepeatedRetailer(retailer string) string {
	if retailer == "" {
		return ""
	}

	if len(retailer)%2 == 0 {
		half := len(retailer) / 2
		if strings.EqualFold(retailer[:half], retailer[half:]) {
			return strings.TrimSpace(retailer[:half])
		}
	}

	parts := strings.Fields(retailer)
	if len(parts)%2 == 0 {
		half := len(parts) / 2
		left := strings.Join(parts[:half], " ")
		right := strings.Join(parts[half:], " ")
		if strings.EqualFold(left, right) {
			return left
		}
	}

	return retailer
}

// cleanHTMLText allows stripping HTML tags from a string.
// It uses goquery to parse the fragment and return text.
func cleanHTMLText(htmlStr string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlStr))
	if err != nil {
		return htmlStr // fallback
	}
	return strings.TrimSpace(doc.Text())
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
	if c.httpClient == nil {
		return nil, errors.New("RFD HTTP client is not configured")
	}
	if c.httpClient.Jar == nil {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return nil, fmt.Errorf("initialize RFD cookie jar: %w", jarErr)
		}
		c.httpClient.Jar = jar
	}

	profile := c.profile
	if profile.UserAgent == "" {
		profile = randomProfile()
		c.profile = profile
	}
	res, err := c.fetchHTMLResponse(ctx, urlStr, profile)
	if err != nil {
		return nil, err
	}

	if res.StatusCode == http.StatusAccepted {
		body, readErr := io.ReadAll(io.LimitReader(res.Body, rfdPOWChallengeBodyLimit))
		res.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read RFD proof-of-work challenge from %s: %w", urlStr, readErr)
		}

		challenge, parseErr := parseRFDPOWChallenge(string(body))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to fetch URL %s: status code %d: %w", urlStr, res.StatusCode, parseErr)
		}
		solveStart := time.Now()
		cookieValue, attempts, solveErr := solveRFDPOWChallenge(ctx, challenge)
		if solveErr != nil {
			return nil, fmt.Errorf("solve RFD proof-of-work challenge for %s: %w", urlStr, solveErr)
		}
		c.httpClient.Jar.SetCookies(parsedURL, []*http.Cookie{{
			Name:     "pow_bypass",
			Value:    cookieValue,
			Path:     "/",
			MaxAge:   int(challenge.cookieDuration / time.Second),
			Secure:   parsedURL.Scheme == "https",
			SameSite: http.SameSiteLaxMode,
		}})
		slog.Info("Solved RFD proof-of-work challenge",
			"processor", "rfd",
			"attempts", attempts,
			"duration", time.Since(solveStart).Round(time.Millisecond).String(),
		)

		res, err = c.fetchHTMLResponse(ctx, urlStr, profile)
		if err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status code %d", urlStr, res.StatusCode)
	}

	return goquery.NewDocumentFromReader(res.Body)
}

func (c *Client) fetchHTMLResponse(ctx context.Context, urlStr string, profile browserProfile) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for URL %s: %w", urlStr, err)
	}

	applyStealthHeaders(req, profile)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", urlStr, err)
	}
	return res, nil
}

func parseRFDPOWChallenge(body string) (rfdPOWChallenge, error) {
	if !strings.Contains(body, "POW_CHALLENGE_DATA") {
		return rfdPOWChallenge{}, errors.New("HTTP 202 response was not a recognized RFD proof-of-work challenge")
	}

	fields := make(map[string]string)
	for _, match := range rfdPOWFieldPattern.FindAllStringSubmatch(body, -1) {
		fields[match[1]] = match[2]
	}
	for _, name := range []string{"challenge_nonce", "challenge_hmac", "difficulty", "difficulty_char", "issued_at"} {
		if strings.TrimSpace(fields[name]) == "" {
			return rfdPOWChallenge{}, fmt.Errorf("RFD proof-of-work challenge is missing %s", name)
		}
	}
	for _, name := range []string{"challenge_nonce", "challenge_hmac", "issued_at"} {
		if strings.ContainsAny(fields[name], "|;\r\n") {
			return rfdPOWChallenge{}, fmt.Errorf("RFD proof-of-work challenge has invalid %s", name)
		}
	}

	difficulty, err := strconv.Atoi(fields["difficulty"])
	if err != nil || difficulty < 1 || difficulty > rfdPOWMaxDifficulty {
		return rfdPOWChallenge{}, fmt.Errorf("unsupported RFD proof-of-work difficulty %q", fields["difficulty"])
	}
	difficultyChar := strings.ToLower(fields["difficulty_char"])
	if len(difficultyChar) != 1 || !strings.Contains("0123456789abcdef", difficultyChar) {
		return rfdPOWChallenge{}, fmt.Errorf("invalid RFD proof-of-work difficulty character %q", fields["difficulty_char"])
	}

	cookieSeconds := 3600
	if fields["cookie_duration"] != "" {
		parsed, parseErr := strconv.Atoi(fields["cookie_duration"])
		if parseErr != nil || parsed < 1 || parsed > 24*60*60 {
			return rfdPOWChallenge{}, fmt.Errorf("invalid RFD proof-of-work cookie duration %q", fields["cookie_duration"])
		}
		cookieSeconds = parsed
	}

	return rfdPOWChallenge{
		nonce:          fields["challenge_nonce"],
		hmac:           fields["challenge_hmac"],
		difficulty:     difficulty,
		difficultyChar: difficultyChar,
		issuedAt:       fields["issued_at"],
		cookieDuration: time.Duration(cookieSeconds) * time.Second,
	}, nil
}

func solveRFDPOWChallenge(ctx context.Context, challenge rfdPOWChallenge) (string, int, error) {
	prefix := strings.Repeat(challenge.difficultyChar, challenge.difficulty)
	for attempt := 1; attempt <= rfdPOWMaxAttempts; attempt++ {
		if attempt%1024 == 0 {
			select {
			case <-ctx.Done():
				return "", attempt, ctx.Err()
			default:
			}
		}

		counter := strconv.Itoa(attempt)
		hash := sha256.Sum256([]byte(challenge.nonce + challenge.issuedAt + counter))
		hashText := fmt.Sprintf("%x", hash)
		if strings.HasPrefix(hashText, prefix) {
			value := strings.Join([]string{challenge.nonce, challenge.issuedAt, counter, hashText, challenge.hmac}, "|")
			return value, attempt, nil
		}
	}
	return "", rfdPOWMaxAttempts, fmt.Errorf("no solution after %d attempts", rfdPOWMaxAttempts)
}
