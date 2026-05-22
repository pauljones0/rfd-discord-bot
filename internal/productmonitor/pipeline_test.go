package productmonitor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

type monitorProduct struct {
	Key      string
	Enriched bool
}

type monitorScreen struct {
	Key string
	Top bool
}

type monitorVerify struct {
	Key string
}

type monitorAnalyzed struct {
	Key      string
	Enriched bool
	Warm     bool
}

func TestProcessBatchBeforeAnalyzeRunsForTier1CandidatesOnly(t *testing.T) {
	var hookInputs [][]monitorProduct
	var analyzeInputs [][]monitorProduct
	var saved []monitorAnalyzed

	cfg := Config[monitorProduct, monitorScreen, monitorVerify, monitorAnalyzed]{
		ProductKey: func(product monitorProduct) string { return product.Key },
		ScreenKey:  func(screen monitorScreen) string { return screen.Key },
		AnalyzeKey: func(result monitorVerify) string { return result.Key },
		IsTopDeal:  func(screen monitorScreen) bool { return screen.Top },
		Tier1Limit: func(int) int { return 1 },
		Fallback: func(product monitorProduct) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched}
		},
		FromScreen: func(product monitorProduct, _ monitorScreen) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched}
		},
		FromAnalysis: func(product monitorProduct, _ monitorScreen, _ monitorVerify) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched, Warm: true}
		},
		Save: func(_ context.Context, analyzed monitorAnalyzed) error {
			saved = append(saved, analyzed)
			return nil
		},
		ScreenBatch: func(_ context.Context, products []monitorProduct) ([]monitorScreen, error) {
			return []monitorScreen{{Key: products[0].Key, Top: true}, {Key: products[1].Key, Top: false}, {Key: products[2].Key, Top: true}}, nil
		},
		BeforeAnalyze: func(_ context.Context, products []monitorProduct) ([]monitorProduct, error) {
			hookInputs = append(hookInputs, append([]monitorProduct(nil), products...))
			out := append([]monitorProduct(nil), products...)
			for i := range out {
				out[i].Enriched = true
			}
			return out, nil
		},
		AnalyzeBatch: func(_ context.Context, products []monitorProduct) ([]monitorVerify, error) {
			analyzeInputs = append(analyzeInputs, append([]monitorProduct(nil), products...))
			return []monitorVerify{{Key: products[0].Key}}, nil
		},
	}

	result, err := ProcessBatch(context.Background(), []monitorProduct{{Key: "a"}, {Key: "b"}, {Key: "c"}}, cfg, discardLogger())
	if err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if result.Tier1Passed != 1 || result.Tier2Passed != 0 {
		t.Fatalf("tier counts = %d/%d, want 1/0", result.Tier1Passed, result.Tier2Passed)
	}
	if len(hookInputs) != 1 || len(hookInputs[0]) != 1 || hookInputs[0][0].Key != "a" {
		t.Fatalf("BeforeAnalyze inputs = %#v, want only capped tier-1 candidate a", hookInputs)
	}
	if len(analyzeInputs) != 1 || !analyzeInputs[0][0].Enriched {
		t.Fatalf("AnalyzeBatch did not receive enriched products: %#v", analyzeInputs)
	}
	if len(saved) != 3 || !saved[2].Enriched {
		t.Fatalf("saved = %#v, want enriched tier-2 candidate persisted", saved)
	}
}

func TestProcessBatchBeforeAnalyzeErrorPreservesOriginalCandidates(t *testing.T) {
	var analyzeInputs [][]monitorProduct
	cfg := Config[monitorProduct, monitorScreen, monitorVerify, monitorAnalyzed]{
		ProductKey: func(product monitorProduct) string { return product.Key },
		ScreenKey:  func(screen monitorScreen) string { return screen.Key },
		AnalyzeKey: func(result monitorVerify) string { return result.Key },
		IsTopDeal:  func(screen monitorScreen) bool { return screen.Top },
		Fallback: func(product monitorProduct) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched}
		},
		FromScreen: func(product monitorProduct, _ monitorScreen) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched}
		},
		FromAnalysis: func(product monitorProduct, _ monitorScreen, _ monitorVerify) monitorAnalyzed {
			return monitorAnalyzed{Key: product.Key, Enriched: product.Enriched}
		},
		ScreenBatch: func(_ context.Context, products []monitorProduct) ([]monitorScreen, error) {
			return []monitorScreen{{Key: products[0].Key, Top: true}}, nil
		},
		BeforeAnalyze: func(context.Context, []monitorProduct) ([]monitorProduct, error) {
			return nil, errors.New("boom")
		},
		AnalyzeBatch: func(_ context.Context, products []monitorProduct) ([]monitorVerify, error) {
			analyzeInputs = append(analyzeInputs, append([]monitorProduct(nil), products...))
			return []monitorVerify{{Key: products[0].Key}}, nil
		},
	}

	_, err := ProcessBatch(context.Background(), []monitorProduct{{Key: "a"}}, cfg, discardLogger())
	if err != nil {
		t.Fatalf("ProcessBatch() error = %v", err)
	}
	if len(analyzeInputs) != 1 || analyzeInputs[0][0].Enriched {
		t.Fatalf("AnalyzeBatch inputs = %#v, want original un-enriched candidate", analyzeInputs)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
