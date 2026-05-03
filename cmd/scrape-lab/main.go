package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapelab"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

func main() {
	var targetsPath string
	var backendsRaw string
	var outDir string
	var environment string
	var timeoutRaw string
	var chromeProfile string
	var fromStore bool
	var sitesRaw string
	var ebayLimit int

	flag.StringVar(&targetsPath, "targets", "", "JSON file containing scrape lab targets")
	flag.StringVar(&backendsRaw, "backends", "", "comma-separated backend list")
	flag.StringVar(&outDir, "out", filepath.Join("docs", "scrape-lab", time.Now().Format("20060102-150405")), "output directory")
	flag.StringVar(&environment, "env", "local", "environment label written to the report")
	flag.StringVar(&timeoutRaw, "timeout", "45s", "per-backend timeout")
	flag.StringVar(&chromeProfile, "chrome-profile", os.Getenv("SCRAPELAB_CHROME_PROFILE_DIR"), "persistent Chrome profile dir")
	flag.BoolVar(&fromStore, "from-store", false, "load targets from Postgres instead of only env/default inputs")
	flag.StringVar(&sitesRaw, "sites", "ebay,memoryexpress,bestbuy", "comma-separated site list for store target discovery")
	flag.IntVar(&ebayLimit, "ebay-limit", 25, "maximum tracked eBay listing URLs to include from store")
	flag.Parse()

	timeout, err := time.ParseDuration(timeoutRaw)
	if err != nil {
		log.Fatalf("invalid timeout: %v", err)
	}

	ctx := context.Background()
	targets, err := loadTargets(ctx, targetLoadOptions{
		TargetsPath: targetsPath,
		FromStore:   fromStore,
		Sites:       parseCSV(sitesRaw),
		EbayLimit:   ebayLimit,
	})
	if err != nil {
		log.Fatalf("load targets: %v", err)
	}
	if len(targets) == 0 {
		log.Fatal("no targets configured; pass -targets, set SCRAPELAB_* target env vars, or use -from-store")
	}

	backends := parseCSV(backendsRaw)
	if len(backends) == 0 {
		backends = []string{
			scrapebackend.BackendHTTP,
			scrapebackend.BackendChromedpCloudRun,
			scrapebackend.BackendChromedpPersistent,
			scrapebackend.BackendPlaywright,
			scrapebackend.BackendExternalStealth,
			scrapebackend.BackendCamoufox,
			scrapebackend.BackendAICrawler,
			scrapebackend.BackendPaidTrial,
		}
	}

	results, err := scrapelab.Run(ctx, targets, scrapelab.Options{
		Backends:      backends,
		Environment:   environment,
		OutDir:        outDir,
		Timeout:       timeout,
		ChromeProfile: chromeProfile,
	})
	if err != nil {
		log.Fatalf("scrape lab failed: %v", err)
	}

	fmt.Printf("Wrote %d scrape lab results to %s\n", len(results), outDir)
}

type targetLoadOptions struct {
	TargetsPath string
	FromStore   bool
	Sites       []string
	EbayLimit   int
}

func loadTargets(ctx context.Context, opts targetLoadOptions) ([]scrapelab.Target, error) {
	if opts.TargetsPath != "" {
		body, err := os.ReadFile(opts.TargetsPath)
		if err != nil {
			return nil, err
		}
		var targets []scrapelab.Target
		if err := json.Unmarshal(body, &targets); err != nil {
			return nil, err
		}
		return targets, nil
	}

	targets := envTargets()
	if len(targets) > 0 {
		return targets, nil
	}

	if opts.FromStore {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		store, err := storage.New(ctx, cfg.ProjectID)
		if err != nil {
			return nil, err
		}
		defer store.Close()

		return scrapelab.DiscoverStoreTargets(ctx, store, scrapelab.DiscoveryOptions{
			Sites:     opts.Sites,
			EbayLimit: opts.EbayLimit,
		})
	}

	return nil, nil
}

func envTargets() []scrapelab.Target {
	var targets []scrapelab.Target
	for _, rawURL := range parseCSV(os.Getenv("SCRAPELAB_EBAY_URLS")) {
		targets = append(targets, scrapelab.Target{Site: "ebay", Name: "ebay-listing", URL: rawURL})
	}
	for _, storeCode := range parseCSV(os.Getenv("SCRAPELAB_MEMEXPRESS_STORES")) {
		pageURL, err := memoryexpress.ClearanceURL(storeCode)
		if err == nil {
			targets = append(targets, scrapelab.Target{Site: "memoryexpress", Name: "memoryexpress-" + storeCode, URL: pageURL})
		}
	}
	for _, rawURL := range parseCSV(os.Getenv("SCRAPELAB_BESTBUY_URLS")) {
		targets = append(targets, scrapelab.Target{Site: "bestbuy", Name: "bestbuy", URL: rawURL})
	}
	if os.Getenv("SCRAPELAB_INCLUDE_DEFAULT_BESTBUY") == "1" {
		targets = append(targets, scrapelab.BestBuyTargetsFromSellers(nil)...)
	}
	return targets
}

func parseCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
