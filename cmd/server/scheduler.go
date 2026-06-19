package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
)

// StartLocalScheduler starts in-process polling loops for active processors.
// It can be disabled for branch testing or maintenance.
func (s *Server) StartLocalScheduler(ctx context.Context, cfg *config.Config) {
	if cfg == nil || !cfg.LocalSchedulerEnabled {
		slog.Info("Local scheduler disabled")
		return
	}

	if s.processor != nil {
		s.startScheduledLoop(ctx, "rfd", cfg.RFDPollInterval, 4*time.Minute, s.sem, s.processor.ProcessDeals)
	}
	if s.ebayProcessor != nil {
		s.startScheduledLoop(ctx, "ebay", cfg.EbayPollInterval, 4*time.Minute, s.ebaySem, s.ebayProcessor.ProcessEbayDeals)
	}
	if s.memexpressProcessor != nil {
		s.startScheduledLoop(ctx, "memoryexpress", cfg.MemoryExpressPollInterval, 2*time.Minute, s.memexpressSem, s.memexpressProcessor.ProcessMemExpressDeals)
	}
	if s.bestbuyProcessor != nil {
		s.startScheduledLoop(ctx, "bestbuy", cfg.BestBuyPollInterval, 8*time.Minute, s.bestbuySem, s.bestbuyProcessor.ProcessBestBuyDeals)
	}
	if cfg.BestBuyComputeEnabled && s.bestbuyCompute != nil {
		s.startScheduledLoop(ctx, "bestbuy_compute", cfg.BestBuyComputePollInterval, 20*time.Minute, s.bestbuyComputeSem, s.bestbuyCompute.ProcessComputeOutliers)
	}
	if cfg.OnEveryCornerScoremerEnabled && s.onEveryCornerScoremer != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			slog.Info("OnEveryCorner Scoremer monitor starting",
				"url", cfg.OnEveryCornerScoremerURL,
				"league_ids", cfg.OnEveryCornerScoremerLeagueIDs,
				"poll_interval", cfg.OnEveryCornerScoremerPollInterval.String(),
			)
			if err := s.onEveryCornerScoremer.Run(ctx); err != nil && ctx.Err() == nil {
				slog.Error("OnEveryCorner Scoremer monitor stopped", "error", err)
			}
		}()
	}

	// Prune core raw notifications daily
	if s.db != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			slog.Info("Running startup pruning of core raw notifications...")
			if count, err := s.db.PruneCoreRawNotifications(ctx, 30*24*time.Hour); err != nil {
				slog.Error("Failed to prune core raw notifications at startup", "error", err)
			} else {
				slog.Info("Pruned old core raw notifications at startup", "deleted_rows", count)
			}

			ticker := time.NewTicker(24 * time.Hour)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					slog.Info("Pruner loop stopped")
					return
				case <-ticker.C:
					slog.Info("Running scheduled pruning of core raw notifications...")
					if count, err := s.db.PruneCoreRawNotifications(ctx, 30*24*time.Hour); err != nil {
						slog.Error("Failed to prune core raw notifications in scheduled loop", "error", err)
					} else {
						slog.Info("Pruned old core raw notifications in scheduled loop", "deleted_rows", count)
					}
				}
			}
		}()
	}
}

func (s *Server) startScheduledLoop(ctx context.Context, processorName string, interval, timeout time.Duration, sem chan struct{}, fn func(context.Context) error) {
	if interval <= 0 {
		slog.Warn("Scheduled processor disabled because interval is not positive",
			"processor", processorName,
			"interval", interval,
		)
		return
	}
	if sem == nil || fn == nil {
		slog.Warn("Scheduled processor disabled because dependencies are missing",
			"processor", processorName,
		)
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		slog.Info("Local scheduler loop started",
			"processor", processorName,
			"interval", interval.String(),
		)

		timer := time.NewTimer(interval)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				slog.Info("Local scheduler loop stopped", "processor", processorName)
				return
			case <-timer.C:
				s.runScheduledJob(ctx, processorName, sem, timeout, fn)
				timer.Reset(interval)
			}
		}
	}()
}

func (s *Server) runScheduledJob(parent context.Context, processorName string, sem chan struct{}, timeout time.Duration, fn func(context.Context) error) (ran bool) {
	select {
	case sem <- struct{}{}:
		ran = true
	default:
		slog.Info("Scheduled processor skipped because previous run is still active",
			"processor", processorName,
		)
		return false
	}
	defer func() { <-sem }()
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("Panic in scheduled processor",
				"processor", processorName,
				"panic", recovered,
			)
		}
	}()

	jobCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	start := time.Now()
	slog.Info("Scheduled processor started", "processor", processorName)
	if err := fn(jobCtx); err != nil {
		slog.Error("Scheduled processor failed",
			"processor", processorName,
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"error", err,
		)
		return true
	}
	slog.Info("Scheduled processor finished",
		"processor", processorName,
		"duration", time.Since(start).Round(time.Millisecond).String(),
	)
	return true
}
