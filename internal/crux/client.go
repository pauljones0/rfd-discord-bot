package crux

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
)

const defaultCompaniesURL = "https://www.cruxinvestor.com/companies"

var defaultUserAgents = []string{
	scrapebackend.DefaultUserAgent,
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_6_1) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
}

type ClientConfig struct {
	BaseURL     string
	Backends    []string
	Exchanges   []string
	Timeout     time.Duration
	PageDelay   time.Duration
	PageJitter  time.Duration
	MaxPages    int
	PaidEnabled bool
	PaidAttempt func(context.Context) error
}

type Client struct {
	cfg       ClientConfig
	rng       *rand.Rand
	fetchHTML func(context.Context, scrapebackend.FetchOptions) scrapebackend.FetchResult
}

type CrawlResult struct {
	Companies    []Company
	PagesFetched int
	TotalPages   int
	BackendsUsed map[string]int
	FetchedAt    time.Time
}

func NewClient(cfg ClientConfig) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultCompaniesURL
	}
	if len(cfg.Backends) == 0 {
		cfg.Backends = []string{scrapebackend.BackendHTTP, scrapebackend.BackendExternalStealth, scrapebackend.BackendCamoufox}
	}
	if len(cfg.Exchanges) == 0 {
		cfg.Exchanges = []string{"TSXV", "TSX", "CSE"}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 35 * time.Second
	}
	if cfg.PageDelay <= 0 {
		cfg.PageDelay = 10 * time.Second
	}
	if cfg.PageJitter < 0 {
		cfg.PageJitter = 0
	}
	if cfg.MaxPages <= 0 {
		cfg.MaxPages = 100
	}
	return &Client{cfg: cfg, rng: rand.New(rand.NewSource(time.Now().UnixNano())), fetchHTML: scrapebackend.FetchHTML}
}

func (c *Client) FetchAll(ctx context.Context) (CrawlResult, error) {
	result := CrawlResult{BackendsUsed: make(map[string]int), FetchedAt: time.Now()}
	for page := 1; page <= c.cfg.MaxPages; page++ {
		companies, totalPages, backend, err := c.FetchPage(ctx, page)
		if err != nil {
			return result, err
		}
		result.PagesFetched++
		result.BackendsUsed[backend]++
		result.Companies = append(result.Companies, companies...)
		if totalPages > 0 {
			result.TotalPages = totalPages
		}
		if result.TotalPages > 0 && page >= result.TotalPages {
			break
		}
		if page >= c.cfg.MaxPages {
			break
		}
		if err := c.sleepBetweenPages(ctx); err != nil {
			return result, err
		}
	}
	if result.TotalPages > 0 && result.PagesFetched < result.TotalPages {
		return result, fmt.Errorf("crux crawl incomplete: fetched %d of %d pages", result.PagesFetched, result.TotalPages)
	}
	return result, nil
}

func (c *Client) FetchPage(ctx context.Context, page int) ([]Company, int, string, error) {
	pageURL := c.pageURL(page)
	var issues []string
	for _, backend := range c.cfg.Backends {
		backend = strings.TrimSpace(backend)
		if backend == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, 0, "", fmt.Errorf("crux page %d canceled before %s fetch: %w", page, backend, err)
		}
		fetchHTML := c.fetchHTML
		if fetchHTML == nil {
			fetchHTML = scrapebackend.FetchHTML
		}
		fetch := fetchHTML(ctx, scrapebackend.FetchOptions{
			Backend:             backend,
			URL:                 pageURL,
			Timeout:             c.cfg.Timeout,
			UserAgent:           c.userAgent(),
			ExternalCommand:     os.Getenv("SCRAPELAB_EXTERNAL_STEALTH_COMMAND"),
			ExternalCommandArgs: scrapebackend.CommandArgsFromEnv("CRUX_EXTERNAL_STEALTH_COMMAND_ARGS", "SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS"),
			CamoufoxCommand:     os.Getenv("SCRAPELAB_CAMOUFOX_COMMAND"),
			CamoufoxCommandArgs: scrapebackend.CommandArgsFromEnv("CRUX_CAMOUFOX_COMMAND_ARGS", "SCRAPELAB_CAMOUFOX_COMMAND_ARGS"),
			AICrawlerCommand:    os.Getenv("SCRAPELAB_AI_CRAWLER_COMMAND"),
			AICrawlerArgs:       scrapebackend.CommandArgsFromEnv("CRUX_AI_CRAWLER_COMMAND_ARGS", "SCRAPELAB_AI_CRAWLER_COMMAND_ARGS"),
			PaidCommand:         os.Getenv("SCRAPELAB_PAID_TRIAL_COMMAND"),
			PaidCommandArgs:     scrapebackend.CommandArgsFromEnv("CRUX_PAID_TRIAL_COMMAND_ARGS", "SCRAPELAB_PAID_TRIAL_COMMAND_ARGS"),
			PaidEnabled:         c.cfg.PaidEnabled,
			PaidAttempt:         c.cfg.PaidAttempt,
		})
		if err := ctx.Err(); err != nil {
			return nil, 0, "", fmt.Errorf("crux page %d canceled while fetching with %s: %w", page, backend, err)
		}

		var companies []Company
		var totalPages int
		var parseErr error
		if strings.TrimSpace(fetch.HTML) == "" {
			parseErr = fmt.Errorf("empty response")
		} else {
			companies, totalPages, parseErr = ParseCompaniesPage(fetch.HTML, pageURL)
		}
		if parseErr == nil && len(companies) > 0 {
			if fetch.BlockSignal != "" {
				slog.Warn("Crux page parsed despite block signal", "backend", backend, "page", page, "block_signal", fetch.BlockSignal)
			}
			return companies, totalPages, backend, nil
		}
		issue := cruxFetchIssue(fetch, parseErr)
		slog.Warn("Crux page backend failed",
			"page", page,
			"backend", backend,
			"duration", fetch.Duration.Round(time.Millisecond).String(),
			"status", fetch.StatusCode,
			"html_bytes", len(fetch.HTML),
			"block_signal", fetch.BlockSignal,
			"issue", issue,
		)
		issues = append(issues, backend+"="+issue)
	}
	return nil, 0, "", fmt.Errorf("failed to fetch crux page %d: %s", page, strings.Join(issues, "; "))
}

func cruxFetchIssue(fetch scrapebackend.FetchResult, parseErr error) string {
	var parts []string
	if fetch.StatusCode > 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", fetch.StatusCode))
	}
	if strings.TrimSpace(fetch.Error) != "" {
		parts = append(parts, strings.TrimSpace(fetch.Error))
	}
	if fetch.BlockSignal != "" {
		parts = append(parts, "blocked:"+fetch.BlockSignal)
	}
	if parseErr != nil {
		parts = append(parts, "parse:"+parseErr.Error())
	}
	if len(parts) == 0 {
		return "unknown fetch/parse failure"
	}
	return strings.Join(parts, " ")
}

func (c *Client) pageURL(page int) string {
	parsed, err := url.Parse(c.cfg.BaseURL)
	if err != nil {
		return c.cfg.BaseURL
	}
	q := parsed.Query()
	if len(c.cfg.Exchanges) > 0 {
		q.Set("ticker", strings.Join(c.cfg.Exchanges, ","))
	}
	q.Set("97d0d7a7_page", fmt.Sprintf("%d", page))
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

func (c *Client) sleepBetweenPages(ctx context.Context) error {
	delay := c.cfg.PageDelay
	if c.cfg.PageJitter > 0 {
		delay += time.Duration(c.rng.Int63n(int64(c.cfg.PageJitter)))
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) userAgent() string {
	if len(defaultUserAgents) == 0 {
		return scrapebackend.DefaultUserAgent
	}
	return defaultUserAgents[c.rng.Intn(len(defaultUserAgents))]
}
