package bestbuy

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	bestBuyComputeMaxRecords                    = 20000
	bestBuyComputeMaxExtremeSoldFallbacksPerRun = 5
	bestBuyComputeIssueRepeatInterval           = 6 * time.Hour
)

type ComputeStore interface {
	SaveBestBuyComputeObservation(ctx context.Context, observation ComputeObservation) error
	ListBestBuyComputeObservations(ctx context.Context) ([]ComputeObservation, error)
	PruneBestBuyComputeObservations(ctx context.Context, maxAgeDays, maxRecords int) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

type ComputeClient interface {
	FetchComputeProducts(ctx context.Context) ([]Product, error)
	ValidateSellerOffer(ctx context.Context, product Product, now time.Time) (OfferValidation, error)
}

type ComputeNotifier interface {
	SendBestBuyDeal(ctx context.Context, product AnalyzedProduct, subs []models.Subscription) error
	SendBestBuyComputeIssue(ctx context.Context, issue ComputeIssue, subs []models.Subscription) error
}

type ComputeProcessor struct {
	store           ComputeStore
	client          ComputeClient
	notifier        ComputeNotifier
	affiliatePrefix string
	alertFirstSeen  bool
	embedder        ComputeEmbedder
	soldVerifier    ComputeSoldVerifier
}

type ComputeStats struct {
	Fetched       int    `json:"fetched"`
	Parsed        int    `json:"parsed"`
	Rejected      int    `json:"rejected"`
	Scored        int    `json:"scored"`
	WarmHot       int    `json:"warmHot"`
	SoldVerified  int    `json:"soldVerified"`
	SoldRejected  int    `json:"soldRejected"`
	SoldFallbacks int    `json:"soldFallbacks"`
	Notified      int    `json:"notified"`
	IssueAlerts   int    `json:"issueAlerts"`
	NotifyErrors  int    `json:"notifyErrors"`
	Subscriptions int    `json:"subscriptions"`
	ExitReason    string `json:"exitReason"`
}

func NewComputeProcessor(store ComputeStore, client ComputeClient, notifier ComputeNotifier, affiliatePrefix string, alertFirstSeen bool, embedder ComputeEmbedder) *ComputeProcessor {
	if embedder == nil {
		embedder = NewComputeEmbedder("")
	}
	return &ComputeProcessor{
		store:           store,
		client:          client,
		notifier:        notifier,
		affiliatePrefix: affiliatePrefix,
		alertFirstSeen:  alertFirstSeen,
		embedder:        embedder,
	}
}

func (p *ComputeProcessor) SetSoldVerifier(verifier ComputeSoldVerifier) {
	p.soldVerifier = verifier
}

func (p *ComputeProcessor) ProcessComputeOutliers(ctx context.Context) error {
	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "bestbuy_compute", "runID", runID)
	stats := ComputeStats{}
	if p.soldVerifier != nil {
		p.soldVerifier.BeginRun()
	}
	defer func() {
		logger.Info("Best Buy compute outlier run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"fetched", stats.Fetched,
			"parsed", stats.Parsed,
			"rejected", stats.Rejected,
			"scored", stats.Scored,
			"warm_hot", stats.WarmHot,
			"sold_verified", stats.SoldVerified,
			"sold_rejected", stats.SoldRejected,
			"sold_fallbacks", stats.SoldFallbacks,
			"notified", stats.Notified,
			"issue_alerts", stats.IssueAlerts,
			"notify_errors", stats.NotifyErrors,
			"subscriptions", stats.Subscriptions,
			"exit_reason", stats.ExitReason,
		)
	}()

	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		stats.ExitReason = "subscription_load_error"
		return fmt.Errorf("load subscriptions: %w", err)
	}
	computeSubs := computeSubscriptions(subs)
	stats.Subscriptions = len(computeSubs)
	if err := stopComputeIfContextDone(ctx, &stats); err != nil {
		return err
	}

	products, err := p.client.FetchComputeProducts(ctx)
	if err != nil {
		logger.Warn("Best Buy compute sweep had partial failures", "error", err)
	}
	if err := stopComputeIfContextDone(ctx, &stats); err != nil {
		return err
	}
	stats.Fetched = len(products)
	if len(products) == 0 {
		stats.ExitReason = "no_products"
		return nil
	}

	existing, err := p.store.ListBestBuyComputeObservations(ctx)
	if err != nil {
		stats.ExitReason = "observation_load_error"
		return fmt.Errorf("load compute observations: %w", err)
	}
	if err := stopComputeIfContextDone(ctx, &stats); err != nil {
		return err
	}
	existingByKey := make(map[string]ComputeObservation, len(existing))
	for _, observation := range existing {
		existingByKey[computeObservationKey(observation.Product)] = observation
	}

	now := time.Now()
	observations := make([]ComputeObservation, 0, len(products))
	for _, product := range products {
		if reason := rejectReasonFromIndexedState(product, now); reason != "" {
			stats.Rejected++
			continue
		}
		spec := ParseComputeSpec(product)
		if !spec.IsCompute {
			stats.Rejected++
		} else {
			stats.Parsed++
		}
		key := computeObservationKey(product)
		observation := ComputeObservation{
			Product:       product,
			Spec:          spec,
			EmbeddingText: ComputeEmbeddingText(product, spec),
			FirstSeen:     now,
			LastSeen:      now,
		}
		if prior, ok := existingByKey[key]; ok {
			observation.FirstSeen = firstNonZeroTime(prior.FirstSeen, prior.LastSeen, now)
			observation.LastAlertAt = prior.LastAlertAt
			observation.LastAlertKey = prior.LastAlertKey
			observation.LastIssueAlertAt = prior.LastIssueAlertAt
			observation.LastIssueAlertKey = prior.LastIssueAlertKey
		}
		observations = append(observations, observation)
	}
	p.embedObservations(ctx, observations, logger)
	if err := stopComputeIfContextDone(ctx, &stats); err != nil {
		return err
	}

	compPool := append([]ComputeObservation{}, existing...)
	compPool = append(compPool, observations...)
	extremeSoldFallbacks := 0
	for i := range observations {
		if err := stopComputeIfContextDone(ctx, &stats); err != nil {
			return err
		}
		observation := observations[i]
		if !observation.Spec.IsCompute {
			if err := p.store.SaveBestBuyComputeObservation(ctx, observation); err != nil {
				logger.Warn("Failed to save rejected compute observation", "sku", observation.SKU, "source", observation.Source, "error", err)
				if err := stopComputeIfContextDone(ctx, &stats); err != nil {
					return err
				}
			}
			continue
		}
		score := ScoreComputeObservationOutlier(observation, compPool)
		observation.ComparableCount = score.ComparableCount
		observation.ComparableMedianPrice = score.MedianPrice
		observation.ComparableP25Price = score.P25Price
		observation.OutlierScore = score.Score
		observation.OutlierGapPct = score.GapPct
		observation.OutlierGapAmount = score.GapAmount
		observation.IsWarm = score.IsWarm
		observation.IsLavaHot = score.IsLavaHot
		observation.Summary = score.Summary
		if score.ComparableCount > 0 {
			stats.Scored++
		}
		if score.IsWarm || score.IsLavaHot {
			stats.WarmHot++
		}

		prior := existingByKey[computeObservationKey(observation.Product)]
		if shouldTryExtremeSoldFallback(observation, score, prior, len(computeSubs), extremeSoldFallbacks, p.alertFirstSeen, p.soldVerifier != nil) {
			validation, err := p.client.ValidateSellerOffer(ctx, observation.Product, now)
			if err != nil {
				logger.Warn("Best Buy compute extreme fallback validation failed",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"error", err,
				)
				if err := stopComputeIfContextDone(ctx, &stats); err != nil {
					return err
				}
			} else if validation.Valid {
				observation.Product = validation.Product
				verification := p.soldVerifier.Verify(ctx, observation, prior, now, logger)
				if err := stopComputeIfContextDone(ctx, &stats); err != nil {
					return err
				}
				applyEbaySoldVerification(&observation, verification)
				extremeSoldFallbacks++
				stats.SoldFallbacks++

				if ebaySoldVerificationConfirmsMarket(verification) {
					applyEbaySoldFallbackScore(&observation, verification)
					observation.EbaySoldAlertKey = computeAlertKey(observation)
					stats.WarmHot++
					stats.SoldVerified++
					logger.Info("Best Buy compute extreme item promoted by eBay sold comps",
						"sku", observation.SKU,
						"sellerID", observation.SellerID,
						"query", verification.Query,
						"backend", verification.Backend,
						"sold_comps", verification.ComparableCount,
						"sold_median", verification.MedianPrice,
						"sold_gap_pct", verification.GapPct,
					)
				} else if verification.Verdict == ebaySoldVerdictFail {
					stats.SoldRejected++
					logger.Info("Best Buy compute extreme item skipped after eBay sold fallback",
						"sku", observation.SKU,
						"sellerID", observation.SellerID,
						"query", verification.Query,
						"backend", verification.Backend,
						"verdict", verification.Verdict,
						"error", verification.Error,
						"sold_comps", verification.ComparableCount,
					)
				} else {
					logger.Info("Best Buy compute extreme item not promoted because eBay sold evidence was unavailable",
						"sku", observation.SKU,
						"sellerID", observation.SellerID,
						"query", verification.Query,
						"backend", verification.Backend,
						"verdict", verification.Verdict,
						"error", verification.Error,
					)
				}
			} else {
				logger.Info("Best Buy compute extreme fallback skipped after validation",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"reason", validation.Reason,
				)
			}
		}
		shouldNotify := shouldNotifyCompute(observation, prior, p.alertFirstSeen)
		if shouldNotify && len(computeSubs) > 0 {
			validation, err := p.client.ValidateSellerOffer(ctx, observation.Product, now)
			if err != nil {
				logger.Warn("Best Buy compute validation failed",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"error", err,
				)
				if err := stopComputeIfContextDone(ctx, &stats); err != nil {
					return err
				}
			} else if validation.Valid {
				observation.Product = validation.Product
				if p.affiliatePrefix != "" && observation.URL != "" {
					observation.URL = p.affiliatePrefix + url.QueryEscape(observation.URL)
				}
				verified := false
				verification := EbaySoldVerification{}
				alreadyVerified := false
				if p.soldVerifier == nil {
					verification = EbaySoldVerification{
						Pass:      false,
						Verdict:   "disabled",
						AlertKey:  computeAlertKey(observation),
						CheckedAt: now,
						Error:     "Best Buy compute eBay sold verification is not configured",
					}
					p.reportComputeIssue(ctx, &observation, prior, score, verification, now, computeSubs, logger, &stats)
					if err := stopComputeIfContextDone(ctx, &stats); err != nil {
						return err
					}
				} else {
					if observation.EbaySoldVerdict == ebaySoldVerdictPass && observation.EbaySoldAlertKey == computeAlertKey(observation) {
						alreadyVerified = true
						verified = true
						verification = EbaySoldVerification{
							Pass:            true,
							Verdict:         "already_verified",
							ComparableCount: observation.EbaySoldComparableCount,
							MedianPrice:     observation.EbaySoldMedianPrice,
							P25Price:        observation.EbaySoldP25Price,
							GapPct:          observation.EbaySoldGapPct,
							GapAmount:       observation.EbaySoldGapAmount,
							MarketLabel: ebaySoldMarketLabel(&observation, EbaySoldVerification{
								ComparableCount: observation.EbaySoldComparableCount,
								MedianPrice:     observation.EbaySoldMedianPrice,
								P25Price:        observation.EbaySoldP25Price,
							}),
							AlertKey:  observation.EbaySoldAlertKey,
							CheckedAt: observation.EbaySoldCheckedAt,
						}
					} else {
						verification = p.soldVerifier.Verify(ctx, observation, prior, now, logger)
						if err := stopComputeIfContextDone(ctx, &stats); err != nil {
							return err
						}
						applyEbaySoldVerification(&observation, verification)
						verified = ebaySoldVerificationConfirmsMarket(verification)
					}
					if ebaySoldVerificationConfirmsMarket(verification) && !alreadyVerified {
						stats.SoldVerified++
					} else if verification.Verdict == ebaySoldVerdictFail {
						stats.SoldRejected++
						logger.Info("Best Buy compute outlier skipped after eBay sold verification",
							"sku", observation.SKU,
							"sellerID", observation.SellerID,
							"query", verification.Query,
							"backend", verification.Backend,
							"verdict", verification.Verdict,
							"error", verification.Error,
							"sold_comps", verification.ComparableCount,
							"sold_median", verification.MedianPrice,
							"sold_gap_pct", verification.GapPct,
						)
					} else if !alreadyVerified {
						logger.Warn("Best Buy compute outlier blocked because eBay sold verification was not decisive",
							"sku", observation.SKU,
							"sellerID", observation.SellerID,
							"query", verification.Query,
							"backend", verification.Backend,
							"verdict", verification.Verdict,
							"error", verification.Error,
							"sold_comps", verification.ComparableCount,
						)
						p.reportComputeIssue(ctx, &observation, prior, score, verification, now, computeSubs, logger, &stats)
						if err := stopComputeIfContextDone(ctx, &stats); err != nil {
							return err
						}
					}
				}
				if verified {
					applyEbaySoldMarketGuardToObservation(&observation, verification)
					if err := p.notifier.SendBestBuyDeal(ctx, computeAnalyzedProduct(observation), computeSubs); err != nil {
						stats.NotifyErrors++
						logger.Warn("Failed to send Best Buy compute outlier", "sku", observation.SKU, "error", err)
						if err := stopComputeIfContextDone(ctx, &stats); err != nil {
							return err
						}
					} else {
						stats.Notified++
						observation.LastAlertAt = now
						observation.LastAlertKey = computeAlertKey(observation)
					}
				}
			} else {
				logger.Info("Best Buy compute outlier skipped after validation",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"reason", validation.Reason,
				)
			}
		} else if shouldBaselineComputeAlertKey(observation, prior, len(computeSubs), p.alertFirstSeen) {
			observation.LastAlertKey = computeAlertKey(observation)
		}

		if err := p.store.SaveBestBuyComputeObservation(ctx, observation); err != nil {
			logger.Warn("Failed to save compute observation", "sku", observation.SKU, "source", observation.Source, "error", err)
			if err := stopComputeIfContextDone(ctx, &stats); err != nil {
				return err
			}
		}
	}

	if err := p.store.PruneBestBuyComputeObservations(ctx, 45, bestBuyComputeMaxRecords); err != nil {
		logger.Warn("Failed to prune Best Buy compute observations", "error", err)
		if err := stopComputeIfContextDone(ctx, &stats); err != nil {
			return err
		}
	}
	stats.ExitReason = "success"
	return nil
}

func stopComputeIfContextDone(ctx context.Context, stats *ComputeStats) error {
	if err := ctx.Err(); err != nil {
		if stats != nil {
			stats.ExitReason = "context_canceled"
		}
		return err
	}
	return nil
}

func computeSubscriptions(subs []models.Subscription) []models.Subscription {
	out := make([]models.Subscription, 0, len(subs))
	for _, sub := range subs {
		if sub.SubscriptionType == dealtypes.SubscriptionBestBuy && sub.DealType == dealtypes.BestBuyCompute {
			out = append(out, sub)
		}
	}
	return out
}

func (p *ComputeProcessor) embedObservations(ctx context.Context, observations []ComputeObservation, logger *slog.Logger) {
	if p.embedder == nil || len(observations) == 0 {
		return
	}
	texts := make([]string, len(observations))
	for i := range observations {
		texts[i] = observations[i].EmbeddingText
	}
	model, vectors, err := p.embedder.Embed(ctx, texts)
	if err != nil {
		logger.Warn("Best Buy compute embedding failed; continuing with structured scoring only", "error", err)
		return
	}
	for i := range observations {
		observations[i].EmbeddingModel = model
		if i < len(vectors) {
			observations[i].EmbeddingVector = vectors[i]
		}
	}
}

func shouldNotifyCompute(observation, prior ComputeObservation, alertFirstSeen bool) bool {
	if !observation.IsWarm && !observation.IsLavaHot {
		return false
	}
	if prior.SKU == "" && !alertFirstSeen {
		return false
	}
	key := computeAlertKey(observation)
	return key != "" && key != prior.LastAlertKey
}

func shouldTryExtremeSoldFallback(observation ComputeObservation, score ComputeScore, prior ComputeObservation, subscriptionCount, attempts int, alertFirstSeen, verifierEnabled bool) bool {
	if !verifierEnabled || subscriptionCount == 0 || attempts >= bestBuyComputeMaxExtremeSoldFallbacksPerRun {
		return false
	}
	if score.IsWarm || score.IsLavaHot || score.ComparableCount >= computeMinComparableCount {
		return false
	}
	if prior.SKU == "" && !alertFirstSeen {
		return false
	}
	if prior.LastAlertKey != "" && prior.LastAlertKey == computeAlertKey(observation) {
		return false
	}
	return isExtremeComputeSpec(observation.Spec, effectiveProductPrice(observation.Product))
}

func applyEbaySoldFallbackScore(observation *ComputeObservation, verification EbaySoldVerification) {
	if observation == nil || !verification.Pass {
		return
	}
	observation.ComparableCount = verification.ComparableCount
	observation.ComparableMedianPrice = verification.MedianPrice
	observation.ComparableP25Price = verification.P25Price
	observation.OutlierGapPct = verification.GapPct
	observation.OutlierGapAmount = verification.GapAmount
	observation.IsWarm = true
	observation.IsLavaHot = ebaySoldMarketLabel(observation, verification) == soldCompMarketHot
	observation.Summary = computeEbaySoldFallbackSummary(observation.Spec, verification)
}

func applyEbaySoldMarketGuardToObservation(observation *ComputeObservation, verification EbaySoldVerification) {
	if observation == nil || !verification.Pass {
		return
	}
	switch ebaySoldMarketLabel(observation, verification) {
	case soldCompMarketHot:
		observation.IsWarm = true
		observation.IsLavaHot = true
	case soldCompMarketWarm:
		observation.IsWarm = true
		observation.IsLavaHot = false
	}
}

func ebaySoldMarketLabel(observation *ComputeObservation, verification EbaySoldVerification) string {
	if verification.MarketLabel != "" {
		return verification.MarketLabel
	}
	if observation == nil || verification.ComparableCount <= 0 || verification.MedianPrice <= 0 {
		return ""
	}
	verdict := soldCompMarketVerdictFromSummary(
		effectiveProductPrice(observation.Product),
		verification.ComparableCount,
		verification.MedianPrice,
		verification.P25Price,
		defaultEbaySoldMinComps,
	)
	return verdict.Label
}

func computeEbaySoldFallbackSummary(spec ComputeSpec, verification EbaySoldVerification) string {
	details := []string{}
	if spec.Model != "" {
		details = append(details, spec.Model)
	} else if spec.Family != "" {
		details = append(details, strings.ReplaceAll(spec.Family, "_", " "))
	}
	if spec.RAMGB > 0 {
		details = append(details, fmt.Sprintf("%.0fGB RAM", spec.RAMGB))
	}
	if spec.CoreCount > 0 {
		details = append(details, fmt.Sprintf("%d cores", spec.CoreCount))
	}
	return fmt.Sprintf("%s looks %.0f%% ($%.0f) below %d eBay sold comps; median sold $%.0f.",
		firstNonEmpty(strings.Join(details, ", "), "Extreme compute config"), verification.GapPct, verification.GapAmount, verification.ComparableCount, verification.MedianPrice)
}

func shouldBaselineComputeAlertKey(observation, prior ComputeObservation, subscriptionCount int, alertFirstSeen bool) bool {
	if !observation.IsWarm && !observation.IsLavaHot {
		return false
	}
	if observation.LastAlertKey != "" {
		return false
	}
	if computeAlertKey(observation) == "" {
		return false
	}
	return subscriptionCount == 0 || (prior.SKU == "" && !alertFirstSeen)
}

func (p *ComputeProcessor) reportComputeIssue(ctx context.Context, observation *ComputeObservation, prior ComputeObservation, score ComputeScore, verification EbaySoldVerification, now time.Time, subs []models.Subscription, logger *slog.Logger, stats *ComputeStats) {
	if p == nil || p.notifier == nil || observation == nil || len(subs) == 0 {
		return
	}
	reason := computeIssueReason(verification)
	issueKey := computeIssueAlertKey(*observation, reason)
	if !shouldNotifyComputeIssue(prior, issueKey, now) {
		return
	}
	issue := ComputeIssue{
		Title:        "Best Buy compute eBay verification issue",
		Severity:     "warning",
		Reason:       reason,
		Details:      verification.Error,
		Product:      observation.Product,
		Spec:         observation.Spec,
		Score:        score,
		Verification: verification,
		OccurredAt:   now,
	}
	if err := p.notifier.SendBestBuyComputeIssue(ctx, issue, subs); err != nil {
		if stats != nil {
			stats.NotifyErrors++
		}
		if logger != nil {
			logger.Warn("Failed to send Best Buy compute issue", "sku", observation.SKU, "reason", reason, "error", err)
		}
		return
	}
	if stats != nil {
		stats.IssueAlerts++
	}
	observation.LastIssueAlertAt = now
	observation.LastIssueAlertKey = issueKey
}

func computeIssueReason(verification EbaySoldVerification) string {
	reason := strings.TrimSpace(verification.Verdict)
	if reason == "" {
		reason = "unknown"
	}
	return reason
}

func computeIssueAlertKey(observation ComputeObservation, reason string) string {
	key := computeAlertKey(observation)
	reason = strings.TrimSpace(reason)
	if key == "" || reason == "" {
		return ""
	}
	return key + "|issue|" + reason
}

func shouldNotifyComputeIssue(prior ComputeObservation, issueKey string, now time.Time) bool {
	if issueKey == "" {
		return false
	}
	if prior.LastIssueAlertKey == issueKey && !prior.LastIssueAlertAt.IsZero() && now.Sub(prior.LastIssueAlertAt) < bestBuyComputeIssueRepeatInterval {
		return false
	}
	return true
}

func computeAnalyzedProduct(observation ComputeObservation) AnalyzedProduct {
	product := observation.Product
	product.ComparableCount = observation.ComparableCount
	product.ComparableMedianPrice = observation.ComparableMedianPrice
	product.ComparableLowestPrice = observation.ComparableP25Price
	product.ComparableDiscountPct = observation.OutlierGapPct
	product.ComparableSummary = observation.Summary
	return AnalyzedProduct{
		Product:     product,
		CleanTitle:  firstNonEmpty(product.Name, product.SKU),
		IsWarm:      observation.IsWarm,
		IsLavaHot:   observation.IsLavaHot,
		Summary:     observation.Summary,
		ProcessedAt: time.Now(),
		LastSeen:    observation.LastSeen,
		AlertKind:   AlertKindComputeOutlier,
	}
}

func computeAlertKey(observation ComputeObservation) string {
	price := effectiveProductPrice(observation.Product)
	if observation.SKU == "" || observation.Source == "" || price <= 0 {
		return ""
	}
	// We stabilize the key to avoid re-alerting on the same item at the same price
	// even if the outlier gap or comparable count changes slightly.
	return fmt.Sprintf("%s|%s|%.2f", observation.SKU, observation.Source, price)
}

func computeObservationKey(product Product) string {
	key := product.SKU + "|" + product.Source
	if key == "|" {
		return product.Name + "|" + product.URL
	}
	return key
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}
