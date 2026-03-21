package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

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

func (c *Client) ScrapeDealList(ctx context.Context) ([]models.DealInfo, error) {
	targetURL := c.config.RFDBaseURL + "/hot-deals-f9/?sk=tt&rfd_sk=tt&sd=d"
	if c.baseURL != "" {
		targetURL = c.baseURL + "/hot-deals"
	}

	slog.Info("Scraping RFD Hot Deals list...", "url", targetURL)

	var scrapedDeals []models.DealInfo
	start := time.Now()

	err := util.RetryWithBackoff(ctx, 3, func(attempt int) error {
		if attempt > 0 {
			slog.Warn("Scraping list attempt failed, retrying", "attempt", attempt)
		}
		var scrapeErr error
		scrapedDeals, scrapeErr = c.attemptScrapeList(ctx, targetURL)
		return scrapeErr
	})

	if err != nil {
		logger.Critical("All retry attempts failed for ScrapeDealList", "error", err)
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}

	logger.Notice("Scrape completed", "duration", time.Since(start), "deals", len(scrapedDeals))
	return scrapedDeals, nil
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
	slog.Info("Scraping hot deals page", "url", targetURL)
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
		retailer := strings.TrimSpace(retailerSel.First().Text())
		// Clean up "at " prefix
		retailer = strings.TrimPrefix(retailer, "at ")
		deal.Retailer = strings.TrimSpace(retailer)
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
	viewCountSelection := s.Find(elems.ViewCount)
	if viewCountSelection.Length() > 0 {
		thread.ViewCount = util.SafeAtoi(util.CleanNumericString(viewCountSelection.First().Text()))
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
		slog.Warn("Parsing issues for deal", "title", deal.Title, "url", deal.PrimaryPostURL(), "errors", strings.Join(parseErrors, "; "))
	}
	return deal
}

func (c *Client) FetchDealDetails(ctx context.Context, deals []*models.DealInfo) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5) // Limit concurrency

	for i := range deals {
		deal := deals[i] // explicit local copy for clarity in the closure
		if deal.PrimaryPostURL() == "" {
			continue
		}

		g.Go(func() error {
			detail, err := c.scrapeDealDetailPage(ctx, deal.PrimaryPostURL())
			if err != nil {
				if strings.Contains(err.Error(), "status code 404") {
					slog.Info("Failed to fetch detail page (404)", "url", deal.PrimaryPostURL())
				} else {
					slog.Warn("Failed to fetch detail page", "url", deal.PrimaryPostURL(), "error", err)
				}
				return nil
			}

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
				slog.Debug("Original Product URL", "url", deal.ActualDealURL)
				deal.ActualDealURL = util.CleanProductURL(deal.ActualDealURL)
				slog.Debug("Cleaned Product URL", "url", deal.ActualDealURL)
				cleanedURL, changed := util.CleanReferralLink(deal.ActualDealURL, c.config.AmazonAffiliateTag, c.config.BestBuyAffiliatePrefix)
				if changed {
					deal.ActualDealURL = cleanedURL
				}
			} else {
				slog.Info("No external deal link found", "postURL", deal.PrimaryPostURL())
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		slog.Error("FetchDealDetails: errgroup error", "error", err)
	}
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
			dealLink = strings.TrimSpace(href)
		}
	}

	// Fallback link
	if dealLink == "" {
		if link := doc.Find(ds.FallbackLink); link.Length() > 0 {
			if href, found := link.Attr("href"); found {
				trimmed := strings.TrimSpace(href)
				if (strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "https://")) &&
					!strings.Contains(trimmed, "redflagdeals.com") {
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
		retailer = strings.TrimSpace(badge.Text())
	}
	if retailer == "" {
		doc.Find("dt").Each(func(i int, s *goquery.Selection) {
			if strings.TrimSpace(s.Text()) == "Retailer:" {
				retailer = strings.TrimSpace(s.Next().Text())
			}
		})
	}

	// JSON-LD Fallback for Retailer
	if retailer == "" && ldRetailer != "" {
		retailer = ldRetailer
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

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for URL %s: %w", urlStr, err)
	}

	profile := randomProfile()
	applyStealthHeaders(req, profile)

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
