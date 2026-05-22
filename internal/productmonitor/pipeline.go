package productmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

type Config[P any, S any, V any, A any] struct {
	ProductKey  func(P) string
	ScreenKey   func(S) string
	AnalyzeKey  func(V) string
	IsTopDeal   func(S) bool
	IsWarmHot   func(A) bool
	Tier1Limit  func(int) int
	ReturnSaved func(A) bool

	Fallback     func(P) A
	FromScreen   func(P, S) A
	FromAnalysis func(P, S, V) A
	Save         func(context.Context, A) error

	ScreenBatch   func(context.Context, []P) ([]S, error)
	BeforeAnalyze func(context.Context, []P) ([]P, error)
	AnalyzeBatch  func(context.Context, []P) ([]V, error)
	AnalyzeOne    func(context.Context, P) (*V, error)
}

type Result[A any] struct {
	Items       []A
	Tier1Passed int
	Tier2Passed int
}

func ProcessBatch[P any, S any, V any, A any](ctx context.Context, batch []P, cfg Config[P, S, V, A], logger *slog.Logger) (Result[A], error) {
	var result Result[A]
	if len(batch) == 0 {
		return result, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.ReturnSaved == nil {
		cfg.ReturnSaved = func(A) bool { return false }
	}

	if cfg.ScreenBatch == nil {
		logger.Warn("AI analyzer not available, saving products without analysis")
		for _, product := range batch {
			analyzed := cfg.Fallback(product)
			saveAnalyzed(ctx, cfg, logger, analyzed)
			if cfg.ReturnSaved(analyzed) {
				result.Items = append(result.Items, analyzed)
			}
		}
		return result, nil
	}

	screenResults, err := cfg.ScreenBatch(ctx, batch)
	if err != nil {
		logger.Error("Tier-1 batch screening failed", "batch_size", len(batch), "error", err)
		for _, product := range batch {
			analyzed := cfg.Fallback(product)
			saveAnalyzed(ctx, cfg, logger, analyzed)
			if cfg.ReturnSaved(analyzed) {
				result.Items = append(result.Items, analyzed)
			}
		}
		if isModelExhausted(err) {
			return result, err
		}
		return result, nil
	}

	screenByKey := make(map[string]S, len(screenResults))
	for _, screen := range screenResults {
		screenByKey[cfg.ScreenKey(screen)] = screen
	}

	type candidate struct {
		product P
		screen  S
	}
	var candidates []candidate
	for _, product := range batch {
		screen, ok := screenByKey[cfg.ProductKey(product)]
		if !ok || !cfg.IsTopDeal(screen) {
			analyzed := cfg.Fallback(product)
			if ok {
				analyzed = cfg.FromScreen(product, screen)
			}
			saveAnalyzed(ctx, cfg, logger, analyzed)
			if cfg.ReturnSaved(analyzed) {
				result.Items = append(result.Items, analyzed)
			}
			continue
		}
		candidates = append(candidates, candidate{product: product, screen: screen})
	}

	if cfg.Tier1Limit != nil {
		limit := cfg.Tier1Limit(len(batch))
		if limit >= 0 && len(candidates) > limit {
			overflow := candidates[limit:]
			candidates = candidates[:limit]
			logger.Info("Tier-1 candidate cap applied",
				"batch_size", len(batch),
				"candidate_limit", limit,
				"candidates_selected", len(candidates),
				"candidates_dropped", len(overflow),
			)
			for _, candidate := range overflow {
				analyzed := cfg.FromScreen(candidate.product, candidate.screen)
				saveAnalyzed(ctx, cfg, logger, analyzed)
				if cfg.ReturnSaved(analyzed) {
					result.Items = append(result.Items, analyzed)
				}
			}
		}
	}

	result.Tier1Passed = len(candidates)
	logger.Info("Tier-1 screening results", "batch_size", len(batch), "passed_tier1", len(candidates))
	if len(candidates) == 0 {
		return result, nil
	}

	verifyProducts := make([]P, 0, len(candidates))
	for _, candidate := range candidates {
		verifyProducts = append(verifyProducts, candidate.product)
	}

	if cfg.BeforeAnalyze != nil {
		enrichedProducts, err := cfg.BeforeAnalyze(ctx, verifyProducts)
		if err != nil {
			logger.Warn("Pre-analysis enrichment failed; continuing with original candidates", "candidate_count", len(verifyProducts), "error", err)
		} else if len(enrichedProducts) != len(verifyProducts) {
			logger.Warn("Pre-analysis enrichment returned unexpected candidate count; continuing with original candidates", "candidate_count", len(verifyProducts), "enriched_count", len(enrichedProducts))
		} else {
			verifyProducts = enrichedProducts
			for i := range candidates {
				candidates[i].product = enrichedProducts[i]
			}
		}
	}

	verifyByKey := map[string]V{}
	var batchErr error
	if cfg.AnalyzeBatch != nil {
		var batchResults []V
		batchResults, batchErr = cfg.AnalyzeBatch(ctx, verifyProducts)
		if batchErr != nil {
			if isModelExhausted(batchErr) {
				logger.Warn("Tier-2 batch verification skipped, AI quota exhausted", "candidate_count", len(candidates))
			} else {
				logger.Warn("Tier-2 batch verification failed, falling back to individual calls", "candidate_count", len(candidates), "error", batchErr)
			}
		} else {
			for _, verify := range batchResults {
				verifyByKey[cfg.AnalyzeKey(verify)] = verify
			}
		}
	}

	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}

		analyzed := cfg.FromScreen(candidate.product, candidate.screen)
		if !isModelExhausted(batchErr) {
			if verify, ok := verifyByKey[cfg.ProductKey(candidate.product)]; ok {
				analyzed = cfg.FromAnalysis(candidate.product, candidate.screen, verify)
			} else if cfg.AnalyzeOne != nil {
				verify, err := cfg.AnalyzeOne(ctx, candidate.product)
				if err != nil {
					logger.Warn("Tier-2 verification failed", "key", cfg.ProductKey(candidate.product), "error", err)
				} else if verify != nil {
					analyzed = cfg.FromAnalysis(candidate.product, candidate.screen, *verify)
				}
			}
		}

		saveAnalyzed(ctx, cfg, logger, analyzed)
		if cfg.IsWarmHot != nil && cfg.IsWarmHot(analyzed) {
			result.Tier2Passed++
		}
		if cfg.ReturnSaved(analyzed) {
			result.Items = append(result.Items, analyzed)
		}
	}

	return result, nil
}

func saveAnalyzed[P any, S any, V any, A any](ctx context.Context, cfg Config[P, S, V, A], logger *slog.Logger, analyzed A) {
	if cfg.Save == nil {
		return
	}
	if err := cfg.Save(ctx, analyzed); err != nil {
		logger.Error("Failed to save analyzed product", "error", err)
	}
}

func isModelExhausted(err error) bool {
	return err != nil && strings.Contains(err.Error(), "all model tiers exhausted")
}

func RequireConfig[P any, S any, V any, A any](cfg Config[P, S, V, A]) error {
	if cfg.ProductKey == nil || cfg.ScreenKey == nil || cfg.AnalyzeKey == nil || cfg.IsTopDeal == nil || cfg.Fallback == nil || cfg.FromScreen == nil || cfg.FromAnalysis == nil {
		return fmt.Errorf("product monitor config is incomplete")
	}
	return nil
}
