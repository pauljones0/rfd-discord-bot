package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/errgroup"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
)

const (
// hotDealsURL and rfdBase are now in config
)

// ErrDealLinkNotFound is returned when a deal detail page does not contain an external deal link.
var ErrDealLinkNotFound = errors.New("deal link not found on detail page")

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
		slog.Error("All retry attempts failed for ScrapeDealList", "error", err)
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}

	slog.Info("Scrape completed", "duration", time.Since(start), "deals", len(scrapedDeals))
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

		deal := c.parseDealFromSelection(s, ls.Elements)
		deals = append(deals, deal)
	})

	return deals, nil
}

func (c *Client) parseDealFromSelection(s *goquery.Selection, elems ListElements) models.DealInfo {
	var deal models.DealInfo
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
	if title != "" {
		deal.Title = title
		if postURL != "" {
			normalized, err := util.NormalizeURL(postURL, c.config.AllowedDomains)
			if err == nil {
				postURL = normalized
			}
		}
		deal.PostURL = postURL
	} else {
		parseErrors = append(parseErrors, "title/post URL element not found")
	}

	// Author
	authorURL, _ := c.resolveLink(s, elems.AuthorLink)
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
		slog.Warn("Parsing issues for deal", "title", deal.Title, "url", deal.PostURL, "errors", strings.Join(parseErrors, "; "))
	}
	return deal
}

func (c *Client) FetchDealDetails(ctx context.Context, deals []*models.DealInfo) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(5) // Limit concurrency

	for i := range deals {
		deal := deals[i] // explicit local copy for clarity in the closure
		if deal.PostURL == "" {
			continue
		}

		g.Go(func() error {
			actualURL, description, comments, summary, err := c.scrapeDealDetailPage(ctx, deal.PostURL)
			if err != nil {
				if errors.Is(err, ErrDealLinkNotFound) {
					slog.Info("No external deal link found", "postURL", deal.PostURL)
				} else {
					slog.Warn("Failed to scrape detail page", "url", deal.PostURL, "error", err)
				}
				// We don't fail the group, just don't update fields if failed
				// However, if we got partial data (e.g. description but no link), we might want to keep it?
				// For now, assume error means fail.
				return nil
			}

			deal.ActualDealURL = actualURL
			deal.Description = description
			deal.Comments = comments
			deal.Summary = summary

			if deal.ActualDealURL != "" {
				cleanedURL, changed := util.CleanReferralLink(deal.ActualDealURL, c.config.AmazonAffiliateTag, c.config.BestBuyAffiliatePrefix)
				if changed {
					deal.ActualDealURL = cleanedURL
				}
			}
			if deal.ActualDealURL == "" {
				slog.Warn("ActualDealURL was empty after parsing", "postURL", deal.PostURL)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		slog.Error("FetchDealDetails: errgroup error", "error", err)
	}
}

func (c *Client) scrapeDealDetailPage(ctx context.Context, dealURL string) (string, string, string, string, error) {
	doc, err := c.fetchHTMLContent(ctx, dealURL)
	if err != nil {
		return "", "", "", "", err
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

	if dealLink == "" {
		// Log but continue, as we might still want description/comments for AI analysis
		// or return error if link is strictly required. Original logic returned error.
		// Let's return error for now to maintain behavior, but maybe AI can find it?
		// Stick to strict behavior for now.
		return "", "", "", "", ErrDealLinkNotFound
	}

	// 2. Extract JSON-LD for Description and Comments
	var description, commentsStr string
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
						commentTexts = append(commentTexts, fmt.Sprintf("- %s: %s", c.Author.Name, cleanHTMLText(c.Text)))
					}
					// Truncate comments to avoid huge tokens
					maxCommentsLen := 2000
					fullComments := strings.Join(commentTexts, "\n")
					if len(fullComments) > maxCommentsLen {
						fullComments = fullComments[:maxCommentsLen] + "...(truncated)"
					}
					commentsStr = fullComments
					return // Found the main posting
				}
			}
		}
		// If array fail, try single object? RFD usually arrays.
		// Let's stick to array as per observation.
	})

	// 3. Extract Summary (if available)
	// Try finding the element by ID even if it's dynamic, sometimes it's SSR.
	summary := strings.TrimSpace(doc.Find("#rfd_topic_summary").Text())

	return dealLink, description, commentsStr, summary, nil
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

	req.Header.Set("User-Agent", c.config.UserAgent)
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
