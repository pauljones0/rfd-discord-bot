package bestbuy

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	bestBuyComputeMaxRecords = 20000
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
}

type ComputeProcessor struct {
	store           ComputeStore
	client          ComputeClient
	notifier        ComputeNotifier
	affiliatePrefix string
	alertFirstSeen  bool
	embedder        ComputeEmbedder
}

type ComputeStats struct {
	Fetched       int    `json:"fetched"`
	Parsed        int    `json:"parsed"`
	Rejected      int    `json:"rejected"`
	Scored        int    `json:"scored"`
	WarmHot       int    `json:"warmHot"`
	Notified      int    `json:"notified"`
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

func (p *ComputeProcessor) ProcessComputeOutliers(ctx context.Context) error {
	start := time.Now()
	runID := start.Format("20060102-150405")
	logger := slog.With("processor", "bestbuy_compute", "runID", runID)
	stats := ComputeStats{}
	defer func() {
		logger.Info("Best Buy compute outlier run complete",
			"duration", time.Since(start).Round(time.Millisecond).String(),
			"fetched", stats.Fetched,
			"parsed", stats.Parsed,
			"rejected", stats.Rejected,
			"scored", stats.Scored,
			"warm_hot", stats.WarmHot,
			"notified", stats.Notified,
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

	products, err := p.client.FetchComputeProducts(ctx)
	if err != nil {
		logger.Warn("Best Buy compute sweep had partial failures", "error", err)
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
		}
		observations = append(observations, observation)
	}
	p.embedObservations(ctx, observations, logger)

	compPool := append([]ComputeObservation{}, existing...)
	compPool = append(compPool, observations...)
	for i := range observations {
		observation := observations[i]
		if !observation.Spec.IsCompute {
			if err := p.store.SaveBestBuyComputeObservation(ctx, observation); err != nil {
				logger.Warn("Failed to save rejected compute observation", "sku", observation.SKU, "source", observation.Source, "error", err)
			}
			continue
		}
		score := ScoreComputeOutlier(observation.Product, observation.Spec, compPool)
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

		if shouldNotifyCompute(observation, existingByKey[computeObservationKey(observation.Product)], p.alertFirstSeen) && len(computeSubs) > 0 {
			validation, err := p.client.ValidateSellerOffer(ctx, observation.Product, now)
			if err != nil {
				logger.Warn("Best Buy compute validation failed",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"error", err,
				)
			} else if validation.Valid {
				observation.Product = validation.Product
				if p.affiliatePrefix != "" && observation.URL != "" {
					observation.URL = p.affiliatePrefix + url.QueryEscape(observation.URL)
				}
				if err := p.notifier.SendBestBuyDeal(ctx, computeAnalyzedProduct(observation), computeSubs); err != nil {
					stats.NotifyErrors++
					logger.Warn("Failed to send Best Buy compute outlier", "sku", observation.SKU, "error", err)
				} else {
					stats.Notified++
					observation.LastAlertAt = now
					observation.LastAlertKey = computeAlertKey(observation)
				}
			} else {
				logger.Info("Best Buy compute outlier skipped after validation",
					"sku", observation.SKU,
					"sellerID", observation.SellerID,
					"reason", validation.Reason,
				)
			}
		}

		if err := p.store.SaveBestBuyComputeObservation(ctx, observation); err != nil {
			logger.Warn("Failed to save compute observation", "sku", observation.SKU, "source", observation.Source, "error", err)
		}
	}

	if err := p.store.PruneBestBuyComputeObservations(ctx, 45, bestBuyComputeMaxRecords); err != nil {
		logger.Warn("Failed to prune Best Buy compute observations", "error", err)
	}
	stats.ExitReason = "success"
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
	return fmt.Sprintf("%s|%s|%.2f|%.0f|%d", observation.SKU, observation.Source, price, observation.OutlierGapPct, observation.ComparableCount)
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
