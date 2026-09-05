package processor

import (
	"context"
	"fmt"
	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"log/slog"
)

// scrapeAndValidate scrapes the deal list and performs initial validation and ID assignment.
func (p *DealProcessor) scrapeAndValidate(ctx context.Context, logger *slog.Logger, tracker *metrics.Tracker) ([]models.DealInfo, error) {
	scrapedDeals, err := p.scraper.ScrapeDealList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scrape hot deals list: %w", err)
	}
	tracker.TrackAdsScraped(len(scrapedDeals))
	logger.Info("Successfully scraped deal list", "count", len(scrapedDeals))

	var validDeals []models.DealInfo
	for i := range scrapedDeals {
		deal := &scrapedDeals[i]

		// Validate using the validator
		if err := p.validator.ValidateStruct(deal); err != nil {
			logger.Error("Validation failed for deal", "title", deal.Title, "error", err)
			continue
		}

		deal.DocumentID = generateDealID(deal.PublishedTimestamp)
		if len(deal.Threads) > 0 {
			deal.Threads[0].DocumentID = deal.DocumentID
		}

		validDeals = append(validDeals, *deal)
	}
	return validDeals, nil
}

// loadExistingDeals fetches existing deals from storage corresponding to the valid scraped deals.
func (p *DealProcessor) loadExistingDeals(ctx context.Context, validDeals []models.DealInfo, logger *slog.Logger) (map[string]*models.DealInfo, error) {
	var idsToLookup []string
	for _, deal := range validDeals {
		idsToLookup = append(idsToLookup, deal.DocumentID)
	}

	existingDeals, err := p.store.GetDealsByIDs(ctx, idsToLookup)
	if err != nil {
		logger.Error("Batch read failed", "error", err)
		return nil, fmt.Errorf("failed to load existing deals: %w", err)
	}
	for i := range validDeals {
		if canonical := existingDeals[validDeals[i].DocumentID]; canonical != nil {
			// Storage may resolve a retained thread alias beyond the recent fuzzy
			// history window. Keep the source alias in Threads for future lookups.
			validDeals[i].DocumentID = canonical.DocumentID
			existingDeals[canonical.DocumentID] = canonical
		}
	}
	return existingDeals, nil
}

// enrichDealsWithDetails determines which deals need detail scraping (new or changed) and fetches them.
func (p *DealProcessor) enrichDealsWithDetails(ctx context.Context, validDeals []models.DealInfo, existingDeals map[string]*models.DealInfo, logger *slog.Logger) models.DealDetailFetchStats {
	var dealsToDetail []*models.DealInfo
	for i := range validDeals {
		deal := &validDeals[i]
		existing := existingDeals[deal.DocumentID]

		if existing == nil {
			// New deal — needs details
			dealsToDetail = append(dealsToDetail, deal)
			continue
		}

		// Optimization: Only fetch details if we actually need them.
		// We need details if:
		// 1. We don't have the ActualDealURL or Description yet.
		// 2. The PostURL changed (new thread/link).
		// 3. The Title changed (likely implies content update or significant edit).
		// Missing source evidence is independent of optional title cleanup.
		existingPostURL := existing.PostURL
		if existingPostURL == "" {
			existingPostURL = existing.PrimaryPostURL()
		}
		postChanged := threadKey(existingPostURL) != threadKey(deal.PostURL)
		needsDetails := existing.ActualDealURL == "" ||
			(existing.Description == "" && existing.Summary == "") ||
			postChanged ||
			existing.Title != deal.Title

		if needsDetails {
			dealsToDetail = append(dealsToDetail, deal)
		} else {
			// Unchanged or only metrics changed — copy details from existing so we have them for AI (if needed) or storage
			preserveExistingDetails(deal, existing)
		}
	}

	if len(dealsToDetail) > 0 {
		logger.Info("Fetching details for deals", "count", len(dealsToDetail))
		return p.scraper.FetchDealDetails(ctx, dealsToDetail)
	}
	logger.Info("No deals needed detail scraping")
	return models.DealDetailFetchStats{}
}

func rfdDetailFetchUnhealthy(stats models.DealDetailFetchStats) bool {
	return stats.Attempted >= 3 && stats.Succeeded == 0 && stats.Failed > 0
}
