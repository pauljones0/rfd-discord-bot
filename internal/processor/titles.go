package processor

import (
	"context"
	"log/slog"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/metrics"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const titleBatchSize = 10

// cleanTitles operates only on reconciled canonical deals. Its slices cannot
// survive a poll, and its deadline never consumes the persistence context.
func (p *DealProcessor) cleanTitles(ctx context.Context, deals []*models.DealInfo, logger *slog.Logger, tracker *metrics.Tracker) {
	if p.aiClient == nil {
		return
	}
	budget := p.titleCleanupTimeout
	if budget <= 0 {
		budget = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	var pending []*models.DealInfo
	for _, deal := range deals {
		if !deal.AIProcessed || deal.CleanTitle == "" {
			pending = append(pending, deal)
		}
	}
	for start := 0; start < len(pending) && ctx.Err() == nil; start += titleBatchSize {
		batch := pending[start:min(start+titleBatchSize, len(pending))]
		requests := make([]models.TitleRequest, len(batch))
		for i, deal := range batch {
			requests[i] = models.TitleRequest{Index: i, Title: deal.Title, Retailer: deal.Retailer, Price: deal.Price}
		}
		results, err := p.aiClient.CleanTitles(ctx, requests)
		calls, input, output, parseFailures, retries := 1, 0, 0, 0, 0
		if usage, ok := p.aiClient.(interface {
			DrainUsage() (int, int, int, int, int)
		}); ok {
			calls, input, output, parseFailures, retries = usage.DrainUsage()
		} else {
			input, output = p.aiClient.DrainTokens()
		}
		tracker.TrackGeminiUsage(calls, input, output)
		if err != nil {
			logger.Warn("Title cleanup failed; retaining original titles", "error", err)
		}
		cleaned := 0
		for i, deal := range batch {
			if title := results[i]; title != "" {
				deal.CleanTitle, deal.AIProcessed = title, true
				tracker.TrackAdProcessed()
				cleaned++
			}
		}
		tracker.TrackAIOutcome("batch_title_cleaning", len(batch), cleaned, len(batch)-cleaned, parseFailures, retries)
	}
}
