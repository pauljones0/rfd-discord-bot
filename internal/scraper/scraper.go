package scraper

import (
	"context"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"github.com/pauljones0/rfd-discord-bot/internal/util"
	"golang.org/x/sync/errgroup"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"sync/atomic"
	"time"
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
		slog.Error("All retry attempts failed for ScrapeDealList", "error", err)
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}

	slog.Info("Scrape completed", "duration", time.Since(start), "deals", len(scrapedDeals))
	return scrapedDeals, nil
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
			if detail.Price != "" {
				deal.Price = detail.Price
			}
			if detail.OriginalPrice != "" {
				deal.OriginalPrice = detail.OriginalPrice
			}
			if detail.Savings != "" {
				deal.Savings = detail.Savings
			}
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

func (c *Client) scrapeDealDetailPage(ctx context.Context, dealURL string) (dealDetailResult, error) {
	doc, err := c.fetchHTMLContent(ctx, dealURL)
	if err != nil {
		return dealDetailResult{}, err
	}
	return c.parseDetailPage(doc)
}
