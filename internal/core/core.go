package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	minPriceObservationsForAlert = 10
	minCategoryObservations      = 10
	maxPriceHistoryEntries       = 10000
)

const (
	gpuBucketAlertWindow         = 24 * time.Hour
	gpuBucketMeaningfulDropRatio = 0.90
)

// Store abstracts database operations for the Core processor.
type Store interface {
	GetCorePriceHistory(ctx context.Context, productName string) (*models.CorePriceHistory, bool, error)
	SaveCorePriceHistory(ctx context.Context, history models.CorePriceHistory) error
	WipeCorePriceHistory(ctx context.Context) error
	GetCoreCategoryStats(ctx context.Context, category string) (*models.CoreCategoryStats, bool, error)
	SaveCoreCategoryStats(ctx context.Context, stats models.CoreCategoryStats) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
	GetCoreRules(ctx context.Context) ([]models.CoreRule, error)
}

// Notifier abstracts Discord notifications.
type Notifier interface {
	SendCoreAlert(ctx context.Context, alert models.CoreAlert, subs []models.Subscription) (map[string]string, error)
	UpdateCoreAlert(ctx context.Context, alert models.CoreAlert) error
	SendCoreSystemAlert(ctx context.Context, alert models.CoreSystemAlert, subs []models.Subscription) error
}

// Processor handles parsing and tracking Core deal alerts without AI.
type Processor struct {
	store    Store
	notifier Notifier
	rates    *RateManager

	locksMu      sync.Mutex
	productLocks map[string]*sync.Mutex

	Rebinning bool // If true, alerts are suppressed
}

// Rebin re-processes raw notifications from the last 30 days to reconstruct price history.
func (p *Processor) Rebin(ctx context.Context) error {
	p.Rebinning = true
	defer func() { p.Rebinning = false }()

	// 1. Wipe old history
	if err := p.store.WipeCorePriceHistory(ctx); err != nil {
		return fmt.Errorf("failed to wipe old history: %w", err)
	}

	// 2. Fetch raw notifications from the last 30 days
	// We use the storage.Client directly if possible, or assume the store satisfies an interface.
	// Since Store is an interface, let's see if we need to add a method to it.

	// Actually, for simplicity in this YOLO fix, I'll just look at what's available.
	// storage.Client has GetRecentCoreRawNotifications.

	// Let's assume we can fetch them. I'll need to cast or update the interface.
	type fullStore interface {
		Store
		GetRecentCoreRawNotifications(ctx context.Context, duration time.Duration) ([]models.CoreRawNotification, error)
	}

	fs, ok := p.store.(fullStore)
	if !ok {
		return fmt.Errorf("store does not support fetching raw notifications")
	}

	notifs, err := fs.GetRecentCoreRawNotifications(ctx, 30*24*time.Hour)
	if err != nil {
		return fmt.Errorf("failed to fetch raw notifications: %w", err)
	}

	slog.Info("Core bot: Starting re-binning process", "notification_count", len(notifs))

	// Sort by date ascending to reconstruct history correctly
	sort.Slice(notifs, func(i, j int) bool {
		return notifs[i].ReceivedAt.Before(notifs[j].ReceivedAt)
	})

	for _, notif := range notifs {
		if err := p.ProcessNotification(ctx, notif.Title, notif.Message, notif.Lines, notif.EventID, notif.SourcePackage, "", "", ""); err != nil {
			slog.Error("Core bot: Re-binning failed for notification", "event_id", notif.EventID, "error", err)
		}
	}

	slog.Info("Core bot: Re-binning process complete")
	return nil
}

// NewProcessor creates a new Core processor.
func NewProcessor(store Store, notifier Notifier, rates *RateManager) *Processor {
	if rates == nil {
		rates = NewRateManager()
	}
	return &Processor{
		store:        store,
		notifier:     notifier,
		rates:        rates,
		productLocks: make(map[string]*sync.Mutex),
	}
}

type ParsedNotification struct {
	ProductName string
	StoreName   string
	Price       float64
	Currency    string
	Link        string
}

type compiledRule struct {
	pattern string
	replace string
	re      *regexp.Regexp
}

type priceEvaluation struct {
	PriorCount   int
	PriorMin     float64
	PriorMax     float64
	PriorP25     float64
	PriorP50     float64
	PriorP75     float64
	LowerBound   float64
	IsAnomaly    bool
	ShouldSignal bool
	Reason       string
	AnomalyScore float64 // % drop from reference median
	AnomalyType  string  // "Steal", "Price Error / Used", "Normal"
}

type priceCluster struct {
	Median float64
	Count  int
	Prices []float64
}

func clusterPrices(prices []float64) []priceCluster {
	if len(prices) == 0 {
		return nil
	}
	// prices is already sorted
	var clusters []priceCluster
	if len(prices) == 0 {
		return clusters
	}

	current := priceCluster{Prices: []float64{prices[0]}}
	for i := 1; i < len(prices); i++ {
		// If gap is more than 5%, start new cluster
		if (prices[i]-prices[i-1])/prices[i-1] > 0.05 {
			current.Median = percentile(current.Prices, 50)
			current.Count = len(current.Prices)
			clusters = append(clusters, current)
			current = priceCluster{Prices: []float64{prices[i]}}
		} else {
			current.Prices = append(current.Prices, prices[i])
		}
	}
	current.Median = percentile(current.Prices, 50)
	current.Count = len(current.Prices)
	clusters = append(clusters, current)
	return clusters
}

func findMainCluster(clusters []priceCluster) priceCluster {
	if len(clusters) == 0 {
		return priceCluster{}
	}
	main := clusters[0]
	for _, c := range clusters[1:] {
		if c.Count > main.Count {
			main = c
		}
	}
	return main
}

var (
	countryTagRegex  = regexp.MustCompile(`\s*[\x{2068}\x{2069}]*@[a-zA-Z0-9]+\s*[\x{2068}\x{2069}]*`)
	urlRegex         = regexp.MustCompile(`https?://[^\s<>"']+`)
	priceNumberRegex = regexp.MustCompile(`[0-9](?:[0-9]|[.,\s\x{00a0}\x{202f}])*`)
	nameReplacer     = strings.NewReplacer(
		"é", "e",
		"í", "i",
		"ó", "o",
		"á", "a",
		"ú", "u",
		"è", "e",
		"ê", "e",
		"à", "a",
		"ç", "c",
		"ñ", "n",
		":", "",
		",", "",
		"-", " ",
		"|", " ",
		"(", "",
		")", "",
		"[", "",
		"]", "",
	)
)

// ParseNotificationText parses deal details from a raw notification text line.
func ParseNotificationText(rates *RateManager, text string) (productName string, price float64, currency string, link string, isDeal bool) {
	parsed, ok := ParseNotification(rates, text)
	if !ok {
		return "", 0, "", "", false
	}
	return parsed.ProductName, parsed.Price, parsed.Currency, parsed.Link, true
}

// ParseNotification parses deal details from a raw notification text line.
func ParseNotification(rates *RateManager, text string) (ParsedNotification, bool) {
	parts := splitNotificationFields(text)
	if len(parts) < 3 {
		return ParsedNotification{}, false
	}

	pricePart := strings.TrimSpace(parts[0])
	storeName := strings.TrimSpace(parts[1])
	if pricePart == "" || storeName == "" {
		return ParsedNotification{}, false
	}

	price, ok := parsePrice(pricePart)
	if !ok {
		return ParsedNotification{}, false
	}

	currency := parseCurrencyFromPricePart(pricePart)
	if currency == "" && rates != nil {
		currency = rates.ResolveCurrencyFromCountry(text)
	}
	if currency == "" {
		currency = inferCurrencyFromSymbolAndStore(pricePart, storeName)
	}

	rawProduct := strings.TrimSpace(strings.Join(parts[2:], " | "))
	link := firstURL(text)
	if link != "" {
		rawProduct = strings.ReplaceAll(rawProduct, link, "")
	}

	productName := cleanProductName(rawProduct)
	if productName == "" {
		return ParsedNotification{}, false
	}

	return ParsedNotification{
		ProductName: productName,
		StoreName:   storeName,
		Price:       price,
		Currency:    currency,
		Link:        link,
	}, true
}

func splitNotificationFields(text string) []string {
	rawParts := strings.Split(text, "|")
	parts := make([]string, 0, len(rawParts))
	for _, part := range rawParts {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return parts
}

func parsePrice(pricePart string) (float64, bool) {
	match := priceNumberRegex.FindString(pricePart)
	if match == "" {
		return 0, false
	}
	match = normalizePriceNumber(match)
	if match == "" {
		return 0, false
	}
	price, err := strconv.ParseFloat(match, 64)
	if err != nil || price <= 0 {
		return 0, false
	}
	return price, true
}

func normalizePriceNumber(value string) string {
	value = strings.Map(func(r rune) rune {
		if r == '\u00a0' || r == '\u202f' || unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.ReplaceAll(strings.TrimSpace(value), " ", "")
	if value == "" {
		return ""
	}

	lastComma := strings.LastIndex(value, ",")
	lastDot := strings.LastIndex(value, ".")
	switch {
	case lastComma >= 0 && lastDot >= 0:
		if lastComma > lastDot {
			value = strings.ReplaceAll(value, ".", "")
			return strings.ReplaceAll(value, ",", ".")
		}
		return strings.ReplaceAll(value, ",", "")
	case lastComma >= 0:
		return normalizeSingleSeparatorPrice(value, ",")
	case lastDot >= 0:
		return normalizeSingleSeparatorPrice(value, ".")
	default:
		return value
	}
}

func normalizeSingleSeparatorPrice(value, sep string) string {
	parts := strings.Split(value, sep)
	if len(parts) == 1 {
		return value
	}
	last := parts[len(parts)-1]
	if len(last) > 0 && len(last) <= 2 {
		whole := strings.Join(parts[:len(parts)-1], "")
		if whole == "" {
			whole = "0"
		}
		return whole + "." + last
	}
	return strings.Join(parts, "")
}

func firstURL(text string) string {
	link := urlRegex.FindString(text)
	return strings.TrimRight(link, ".,;:)]}")
}

func cleanProductName(rawProduct string) string {
	cleaned := countryTagRegex.ReplaceAllString(rawProduct, " ")
	cleaned = strings.TrimSpace(cleaned)
	for strings.HasSuffix(cleaned, "...") {
		cleaned = strings.TrimSpace(strings.TrimSuffix(cleaned, "..."))
	}
	return cleaned
}

func parseCurrencyFromPricePart(pricePart string) string {
	upper := strings.ToUpper(pricePart)
	switch {
	case strings.Contains(upper, "C$"), strings.Contains(upper, "CA$"), strings.Contains(upper, "CAD"):
		return "CAD"
	case strings.Contains(upper, "US$"), strings.Contains(upper, "USD"):
		return "USD"
	case strings.Contains(pricePart, "€"), strings.Contains(upper, "EUR"):
		return "EUR"
	case strings.Contains(pricePart, "£"), strings.Contains(upper, "GBP"):
		return "GBP"
	case strings.Contains(upper, "NOK"):
		return "NOK"
	case strings.Contains(upper, "SEK"):
		return "SEK"
	case strings.Contains(upper, "DKK"):
		return "DKK"
	case strings.Contains(upper, "CHF"):
		return "CHF"
	case strings.Contains(upper, "AUD"):
		return "AUD"
	case strings.Contains(pricePart, "¥"), strings.Contains(upper, "JPY"):
		return "JPY"
	case strings.Contains(pricePart, "zł"), strings.Contains(upper, "PLN"):
		return "PLN"
	default:
		return ""
	}
}

func inferCurrencyFromSymbolAndStore(pricePart, storeName string) string {
	if strings.Contains(pricePart, "€") {
		return "EUR"
	}
	if strings.Contains(pricePart, "£") {
		return "GBP"
	}
	if strings.Contains(pricePart, "¥") {
		return "JPY"
	}
	if strings.Contains(pricePart, "zł") {
		return "PLN"
	}
	if strings.Contains(pricePart, "$") {
		storeUpper := strings.ToUpper(storeName)
		switch {
		case strings.Contains(storeUpper, "AMAZON CA"), strings.Contains(storeUpper, "CANADA"):
			return "CAD"
		case strings.Contains(storeUpper, "AMAZON COM"), strings.Contains(storeUpper, "AMAZON US"), strings.Contains(storeUpper, "USA"):
			return "USD"
		default:
			return "USD"
		}
	}
	return "CAD"
}

type DiscordNotificationMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Time   int64  `json:"time"`
	URI    string `json:"uri,omitempty"`
}

type DiscordNotificationBatch struct {
	ConversationTitle string
	Tag               string
	TickerText        string
	Lines             []string
	Messages          []DiscordNotificationMsg
	PictureBase64     string
	EventID           string
	SourcePackage     string
	RawLink           string
}

func (p *Processor) ProcessNotificationBatch(ctx context.Context, batch DiscordNotificationBatch) error {
	// Construct fallback link from Discord Snowflake tag
	fallbackLink := strings.TrimSpace(batch.RawLink)
	if fallbackLink == "" && strings.HasPrefix(batch.Tag, "MESSAGE_CREATE") {
		channelID := strings.TrimPrefix(batch.Tag, "MESSAGE_CREATE")
		if channelID != "" {
			fallbackLink = "https://discord.com/channels/@me/" + channelID
		}
	}

	category := ParseCategoryFromTitle(batch.ConversationTitle)
	var errs []error

	primary, lines := primaryNotificationCandidate(batch.TickerText, batch.Lines)
	messageCount := 0
	for i, msg := range batch.Messages {
		msg.Text = strings.TrimSpace(msg.Text)
		if msg.Text == "" {
			continue
		}
		messageCount++
		messageLink := fallbackLink
		if strings.TrimSpace(msg.URI) != "" {
			messageLink = strings.TrimSpace(msg.URI)
		}
		subEventID := fmt.Sprintf("%s-%d", batch.EventID, i)
		if err := p.ProcessNotification(ctx, batch.ConversationTitle, msg.Text, nil, subEventID, batch.SourcePackage, messageLink, category, batch.PictureBase64); err != nil {
			slog.Error("Failed to process grouped message", "subEventID", subEventID, "error", err)
			errs = append(errs, fmt.Errorf("message %d: %w", i, err))
		}
	}

	if primary != "" && messageCount == 0 {
		if err := p.ProcessNotification(ctx, batch.ConversationTitle, primary, lines, batch.EventID+"-payload", batch.SourcePackage, fallbackLink, category, batch.PictureBase64); err != nil {
			slog.Error("Failed to process notification payload", "event_id", batch.EventID, "error", err)
			errs = append(errs, fmt.Errorf("payload: %w", err))
		}
	}

	return errors.Join(errs...)
}

func primaryNotificationCandidate(tickerText string, lines []string) (string, []string) {
	candidates := appendUniqueNonEmpty(nil, lines...)
	candidates = appendUniqueNonEmpty(candidates, tickerText)
	if len(candidates) == 0 {
		return "", nil
	}
	return candidates[0], candidates[1:]
}

func appendUniqueNonEmpty(values []string, candidates ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(candidates))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		values = append(values, candidate)
	}
	return values
}

func (p *Processor) ReportSystemIssue(ctx context.Context, alert models.CoreSystemAlert) error {
	if p == nil || p.notifier == nil {
		return nil
	}
	if p.store == nil {
		return fmt.Errorf("core store is not configured")
	}
	if alert.OccurredAt.IsZero() {
		alert.OccurredAt = time.Now()
	}
	if alert.Component == "" {
		alert.Component = "core-notification-ingest"
	}
	if alert.Severity == "" {
		alert.Severity = "warning"
	}
	coreSubs, err := p.coreSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(coreSubs) == 0 {
		slog.Warn("Core bot: No active subscriptions for system alert", "title", alert.Title)
		return nil
	}
	return p.notifier.SendCoreSystemAlert(ctx, alert, coreSubs)
}

// ProcessNotification ingests, parses, checks price percentiles, and alerts if eligible.
func (p *Processor) ProcessNotification(ctx context.Context, title, message string, lines []string, eventID, sourcePackage string, rawLink string, explicitCategory string, pictureBase64 string) error {
	slog.Info("Core bot: Processing notification", "event_id", eventID, "source_package", sourcePackage)

	parsed, ok := p.parseNotificationCandidates(message, lines, title, rawLink)
	if !ok {
		slog.Info("Core bot: Notification is not a valid deal format, skipping", "event_id", eventID, "message", message)
		return nil
	}

	if parsed.Link == "" && rawLink != "" {
		parsed.Link = rawLink
	}

	rules, err := p.store.GetCoreRules(ctx)
	if err != nil {
		slog.Error("Core bot: Failed to fetch core rules, proceeding without rules", "error", err)
	}
	_, ruleErr := compileRules(rules)
	if ruleErr != nil {
		slog.Warn("Core bot: Ignoring invalid normalization rules", "error", ruleErr)
	}

	category := strings.TrimSpace(explicitCategory)
	if category == "" {
		category = ParseCategoryFromTitle(title)
	}
	normName := NormalizeProductName(parsed.ProductName, rules, category)
	if normName == "" {
		normName = NormalizeProductName(parsed.ProductName, nil, category)
	}

	// Check for ambiguity especially with truncated names
	truncated := strings.HasSuffix(strings.TrimSpace(message), "...") || strings.HasSuffix(strings.TrimSpace(title), "...")
	if isAmbiguous(normName, truncated) {
		slog.Info("Core bot: Normalized product name is too ambiguous, skipping", "name", normName, "truncated", truncated)
		return nil
	}

	priceCAD := p.rates.ConvertToCAD(parsed.Price, parsed.Currency)

	lock := p.productLock(normName)
	lock.Lock()
	defer lock.Unlock()

	history, historyLoaded, err := p.loadPriceHistory(ctx, normName, category)
	if err != nil {
		slog.Error("Core bot: Failed to fetch price history, proceeding with empty history", "product", parsed.ProductName, "normalized", normName, "error", err)
	}

	// Load and update category stats
	catStats, _, err := p.store.GetCoreCategoryStats(ctx, category)
	if err != nil {
		slog.Warn("Core bot: Failed to fetch category stats", "category", category, "error", err)
	}
	if catStats == nil {
		catStats = &models.CoreCategoryStats{Category: category}
	}

	if eventAlreadyRecorded(history, eventID) {
		slog.Info("Core bot: Duplicate event already recorded for product, skipping", "event_id", eventID, "product", parsed.ProductName, "normalized", normName)
		return nil
	}

	evaluation := evaluatePrice(priceCAD, history.Prices)
	if !historyLoaded {
		slog.Info("Core bot: First observation of product, registering baseline without alert", "product", parsed.ProductName, "normalized", normName, "price_cad", priceCAD)
	} else if !evaluation.ShouldSignal {
		slog.Debug("Core bot: Price observation did not qualify for alert", "product", parsed.ProductName, "normalized", normName, "price_cad", priceCAD, "reason", evaluation.Reason, "prior_count", evaluation.PriorCount)
	}

	// Threshold enforcement: Category-wide count check
	if catStats.TotalCount < minCategoryObservations {
		if evaluation.ShouldSignal {
			slog.Info("Core bot: Alert suppressed due to insufficient category history", "category", category, "count", catStats.TotalCount)
			evaluation.ShouldSignal = false
			evaluation.Reason = "insufficient_category_history"
		}
	}

	appendPriceObservation(history, priceCAD, eventID)
	history.Category = category
	history.LastUpdated = time.Now()
	if err := p.store.SaveCorePriceHistory(ctx, *history); err != nil {
		return fmt.Errorf("failed to save core price history for %q: %w", normName, err)
	}

	// Update category count
	catStats.TotalCount++
	catStats.LastUpdated = time.Now()
	if err := p.store.SaveCoreCategoryStats(ctx, *catStats); err != nil {
		slog.Warn("Core bot: Failed to save category stats", "category", category, "error", err)
	}

	if !evaluation.ShouldSignal || p.Rebinning {
		if evaluation.Reason == "anomaly_duplicate" && !p.Rebinning {
			updated := false
			for i := range history.RecentAlerts {
				recent := &history.RecentAlerts[i]
				if time.Since(recent.FiredAt) < 12*time.Hour && math.Abs(recent.PriceCAD-priceCAD)/recent.PriceCAD < 0.03 {
					hasStore := false
					for _, s := range recent.StoreNames {
						if s == parsed.StoreName {
							hasStore = true
							break
						}
					}
					if !hasStore {
						recent.StoreNames = append(recent.StoreNames, parsed.StoreName)
						recent.Links = append(recent.Links, parsed.Link)

						if err := p.notifier.UpdateCoreAlert(ctx, *recent); err != nil {
							slog.Error("Core bot: Failed to update core deal alert", "error", err)
						} else {
							slog.Info("Core bot: Appended new store to existing alert", "store", parsed.StoreName)
							updated = true
						}
					}
					break
				}
			}
			if updated {
				_ = p.store.SaveCorePriceHistory(ctx, *history)
			}
		}
		return nil
	}

	if suppressed, recentPrice, recentAge := shouldSuppressRecentGPUBucketAlert(normName, history, priceCAD, time.Now()); suppressed {
		slog.Info("Core bot: Suppressed repeated GPU bucket alert", "normalized", normName, "price_cad", priceCAD, "recent_alert_price_cad", recentPrice, "recent_alert_age", recentAge.String())
		return nil
	}

	coreSubs, err := p.coreDealSubscriptions(ctx, evaluation.AnomalyType, priceCAD, evaluation.PriorMin)
	if err != nil {
		return err
	}
	if len(coreSubs) == 0 {
		slog.Info("Core bot: No active subscriptions for core alerts", "anomaly_type", evaluation.AnomalyType, "price_cad", priceCAD, "prior_min_cad", evaluation.PriorMin)
		return nil
	}

	deal := models.CoreDeal{
		EventID:          eventID,
		SourcePackage:    sourcePackage,
		ProductName:      parsed.ProductName,
		StoreName:        parsed.StoreName,
		Category:         category,
		PriceCAD:         priceCAD,
		OriginalPrice:    parsed.Price,
		OriginalCurr:     parsed.Currency,
		Link:             parsed.Link,
		ImageBase64:      pictureBase64,
		ReceivedAt:       time.Now(),
		MinPriceSeen:     minFloat(priceCAD, evaluation.PriorMin),
		P25PriceSeen:     evaluation.PriorP25,
		P50PriceSeen:     evaluation.PriorP50,
		P75PriceSeen:     evaluation.PriorP75,
		HistoryCount:     coreObservationCount(history),
		PriceSampleCount: evaluation.PriorCount,
		AnomalyType:      evaluation.AnomalyType,
		BoxPlot:          GenerateBoxPlot(evaluation, priceCAD),
	}

	alert := models.CoreAlert{
		PriceCAD:   priceCAD,
		StoreNames: []string{parsed.StoreName},
		Links:      []string{parsed.Link},
		FiredAt:    time.Now(),
		Deal:       deal,
	}

	slog.Info("Core bot: Price drop/low price detected; sending alert", "product", parsed.ProductName, "store", parsed.StoreName, "price_cad", priceCAD, "min_cad", deal.MinPriceSeen, "p25_cad", deal.P25PriceSeen, "history_count", deal.HistoryCount, "sample_count", deal.PriceSampleCount)
	msgIDs, err := p.notifier.SendCoreAlert(ctx, alert, coreSubs)
	if err != nil {
		return fmt.Errorf("failed to send core deal alerts: %w", err)
	}

	alert.MessageIDs = msgIDs

	var recentAlerts []models.CoreAlert
	for _, a := range history.RecentAlerts {
		if time.Since(a.FiredAt) < 24*time.Hour {
			recentAlerts = append(recentAlerts, a)
		}
	}
	history.RecentAlerts = append(recentAlerts, alert)
	_ = p.store.SaveCorePriceHistory(ctx, *history)

	return nil
}

func (p *Processor) parseNotificationCandidates(message string, lines []string, title string, rawLink string) (ParsedNotification, bool) {
	candidates := make([]string, 0, len(lines)+3)
	candidates = append(candidates, message)
	candidates = append(candidates, lines...)
	candidates = append(candidates, title)

	seen := make(map[string]struct{}, len(candidates))
	var best ParsedNotification
	var found bool

	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}

		if parsed, ok := ParseNotification(p.rates, candidate); ok {
			if !found {
				best = parsed
				found = true
				continue
			}

			// Compare prices in CAD to pick the best deal
			currentBestCAD := p.rates.ConvertToCAD(best.Price, best.Currency)
			newCAD := p.rates.ConvertToCAD(parsed.Price, parsed.Currency)
			if newCAD < currentBestCAD {
				best = parsed
			}
		}
	}
	return best, found
}

func (p *Processor) loadPriceHistory(ctx context.Context, normName, category string) (*models.CorePriceHistory, bool, error) {
	history, ok, err := p.store.GetCorePriceHistory(ctx, normName)
	if err != nil {
		ok = false
	}
	if !ok || history == nil {
		return &models.CorePriceHistory{
			ProductName: normName,
			Category:    category,
			Prices:      []float64{},
			LastUpdated: time.Now(),
		}, false, err
	}
	return history, true, nil
}

func (p *Processor) coreSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	allSubs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch subscriptions: %w", err)
	}

	coreSubs := make([]models.Subscription, 0, len(allSubs))
	for _, sub := range allSubs {
		if sub.IsCore() && (sub.DealType == "" || dealtypes.IsCore(sub.DealType)) {
			coreSubs = append(coreSubs, sub)
		}
	}
	return coreSubs, nil
}

func (p *Processor) coreDealSubscriptions(ctx context.Context, anomalyType string, priceCAD, priorMin float64) ([]models.Subscription, error) {
	coreSubs, err := p.coreSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	eligible := make([]models.Subscription, 0, len(coreSubs))
	for _, sub := range coreSubs {
		if dealtypes.CoreEligible(sub.DealType, anomalyType, priceCAD, priorMin) {
			eligible = append(eligible, sub)
		}
	}
	return eligible, nil
}

func shouldSuppressRecentGPUBucketAlert(normName string, history *models.CorePriceHistory, priceCAD float64, now time.Time) (bool, float64, time.Duration) {
	if !strings.HasPrefix(normName, "gpu ") || history == nil || priceCAD <= 0 {
		return false, 0, 0
	}

	var bestRecentPrice float64
	var bestRecentAge time.Duration
	for _, alert := range history.RecentAlerts {
		if alert.FiredAt.IsZero() || alert.PriceCAD <= 0 {
			continue
		}
		age := now.Sub(alert.FiredAt)
		if age < 0 {
			age = 0
		}
		if age > gpuBucketAlertWindow {
			continue
		}
		if bestRecentPrice == 0 || alert.PriceCAD < bestRecentPrice {
			bestRecentPrice = alert.PriceCAD
			bestRecentAge = age
		}
	}
	if bestRecentPrice == 0 {
		return false, 0, 0
	}

	return priceCAD > bestRecentPrice*gpuBucketMeaningfulDropRatio, bestRecentPrice, bestRecentAge
}

func (p *Processor) productLock(normName string) *sync.Mutex {
	p.locksMu.Lock()
	defer p.locksMu.Unlock()

	lock, ok := p.productLocks[normName]
	if !ok {
		lock = &sync.Mutex{}
		p.productLocks[normName] = lock
	}
	return lock
}

func evaluatePrice(priceCAD float64, priorPrices []float64) priceEvaluation {
	ev := priceEvaluation{PriorCount: len(priorPrices), AnomalyType: "Normal"}
	if len(priorPrices) == 0 {
		ev.Reason = "first_observation"
		return ev
	}

	sort.Float64s(priorPrices)
	ev.PriorMin = priorPrices[0]
	ev.PriorMax = priorPrices[len(priorPrices)-1]
	ev.PriorP25 = percentile(priorPrices, 25)
	ev.PriorP50 = percentile(priorPrices, 50)
	ev.PriorP75 = percentile(priorPrices, 75)

	iqr := ev.PriorP75 - ev.PriorP25
	// Using standard 1.5 * IQR for mild outliers
	ev.LowerBound = math.Max(0, ev.PriorP25-(1.5*iqr))
	ev.IsAnomaly = priceCAD < ev.LowerBound && priceCAD > 0

	if len(priorPrices) < minPriceObservationsForAlert {
		ev.Reason = "insufficient_history"
		return ev
	}

	// Cluster-based analysis
	clusters := clusterPrices(priorPrices)
	mainCluster := findMainCluster(clusters)
	refPrice := mainCluster.Median

	// Calculate anomaly score: % drop from the main cluster median
	if refPrice > 0 {
		ev.AnomalyScore = (refPrice - priceCAD) / refPrice * 100
	}

	if ev.IsAnomaly {
		// Classification based on severity
		if ev.AnomalyScore > 35 {
			ev.AnomalyType = "Price Error / Used"
		} else if ev.AnomalyScore > 15 {
			ev.AnomalyType = "Steal"
		} else {
			ev.AnomalyType = "Deal"
		}

		// Prevent duplicates: Check if we already have a very similar price in history
		// (e.g., within 3% of an existing price). Handles currency swings and rounding.
		isDupe := false
		for _, prev := range priorPrices {
			diff := math.Abs(prev - priceCAD)
			if diff/priceCAD < 0.03 {
				isDupe = true
				break
			}
		}

		if isDupe {
			ev.Reason = "anomaly_duplicate"
		} else {
			ev.ShouldSignal = true
			ev.Reason = "downside_anomaly"
		}
		return ev
	}

	ev.Reason = "within_normal_range"
	return ev
}

// GenerateBoxPlot creates an ASCII representation of the price distribution and the new outlier.
func GenerateBoxPlot(ev priceEvaluation, currentPrice float64) string {
	const width = 60
	minP := math.Min(ev.PriorMin, currentPrice)
	maxP := math.Max(ev.PriorMax, currentPrice)

	if maxP == minP {
		const msg = "[ Price Distribution Not Available ]"
		msgLen := utf8.RuneCountInString(msg)
		if msgLen >= width {
			return msg
		}
		padding := (width - msgLen) / 2
		return strings.Repeat(" ", padding) + msg + strings.Repeat(" ", width-msgLen-padding)
	}

	scale := func(val float64) int {
		pos := int(math.Round((val - minP) / (maxP - minP) * float64(width-1)))
		if pos < 0 {
			return 0
		}
		if pos >= width {
			return width - 1
		}
		return pos
	}

	p25Pos := scale(ev.PriorP25)
	p50Pos := scale(ev.PriorP50)
	p75Pos := scale(ev.PriorP75)
	currPos := scale(currentPrice)

	line := make([]rune, width)
	for i := range line {
		line[i] = ' '
	}

	// Draw the whiskers line
	minPos := scale(ev.PriorMin)
	maxPos := scale(ev.PriorMax)
	for i := minPos; i <= maxPos; i++ {
		line[i] = '-'
	}

	// Draw the box (IQR)
	for i := p25Pos; i <= p75Pos; i++ {
		line[i] = '█'
	}

	// Draw median
	if p50Pos >= 0 && p50Pos < width {
		line[p50Pos] = '|'
	}

	// Draw whiskers caps
	if minPos >= 0 && minPos < width {
		line[minPos] = '['
	}
	if maxPos >= 0 && maxPos < width {
		line[maxPos] = ']'
	}

	// Draw current price
	if currPos >= 0 && currPos < width {
		if line[currPos] == '█' || line[currPos] == '|' || line[currPos] == '[' || line[currPos] == ']' {
			line[currPos] = 'X' // Overlap
		} else {
			line[currPos] = '▼' // Current price mark
		}
	}

	return string(line)
}

func appendPriceObservation(history *models.CorePriceHistory, priceCAD float64, eventID string) {
	if history.ObservationCount < len(history.Prices) {
		history.ObservationCount = len(history.Prices)
	}
	history.ObservationCount++
	history.Prices = append(history.Prices, priceCAD)
	if eventID != "" {
		history.EventIDs = append(history.EventIDs, eventID)
	}

	if len(history.Prices) > maxPriceHistoryEntries {
		drop := len(history.Prices) - maxPriceHistoryEntries
		history.Prices = history.Prices[drop:]
		if len(history.EventIDs) > drop {
			history.EventIDs = history.EventIDs[drop:]
		}
	}
	if len(history.EventIDs) > maxPriceHistoryEntries {
		history.EventIDs = history.EventIDs[len(history.EventIDs)-maxPriceHistoryEntries:]
	}
}

func coreObservationCount(history *models.CorePriceHistory) int {
	if history == nil {
		return 0
	}
	if history.ObservationCount < len(history.Prices) {
		return len(history.Prices)
	}
	return history.ObservationCount
}

func eventAlreadyRecorded(history *models.CorePriceHistory, eventID string) bool {
	if eventID == "" || history == nil {
		return false
	}
	for _, seen := range history.EventIDs {
		if seen == eventID {
			return true
		}
	}
	return false
}

// ParseCategoryFromTitle extracts the hashtag category from notification titles like "CoreFinder #pokemon: CoreFinder".
func ParseCategoryFromTitle(title string) string {
	idxHash := strings.Index(title, "#")
	if idxHash == -1 {
		return "Core Deal"
	}
	remaining := title[idxHash+1:]
	idxColon := strings.Index(remaining, ":")
	if idxColon != -1 {
		remaining = remaining[:idxColon]
	}
	category := strings.TrimSpace(remaining)
	if category == "" {
		return "Core Deal"
	}
	return category
}

// compileRules compiles regex replacement rules and returns every valid rule.
func compileRules(rules []models.CoreRule) ([]compiledRule, error) {
	compiled := make([]compiledRule, 0, len(rules))
	var errs []error
	for _, rule := range rules {
		if strings.TrimSpace(rule.Pattern) == "" {
			continue
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			errs = append(errs, fmt.Errorf("pattern %q: %w", rule.Pattern, err))
			continue
		}
		compiled = append(compiled, compiledRule{
			pattern: rule.Pattern,
			replace: rule.Replace,
			re:      re,
		})
	}
	return compiled, errors.Join(errs...)
}

// ValidateRules verifies that every non-empty regex rule can be compiled.
func ValidateRules(rules []models.CoreRule) error {
	_, err := compileRules(rules)
	return err
}

// NormalizeProductName normalizes characters and symbols to group similar product names reliably.
func NormalizeProductName(name string, rules []models.CoreRule, category string) string {
	compiled, _ := compileRules(rules)
	norm := normalizeProductNameWithRules(name, compiled)

	// Special handling for GPUs must run before RAM. GPU titles often include
	// phrases like "8GB GDDR7 RAM", which should not be treated as system RAM.
	if isGPUCategory(category) || (isGPUProduct(norm) && !isRAMCategory(category)) {
		if gpuSpec := extractGPUSpecs(norm, category); gpuSpec != "" {
			return gpuSpec
		}
	}

	// Special handling for RAM
	if isRAMCategory(category) || isRAMProduct(norm) {
		if ramSpec := extractRAMSpecs(norm); ramSpec != "" {
			return ramSpec
		}
		// If it's a RAM category but we extracted absolutely nothing, mark it as ram unknown
		// so it at least groups with itself instead of forming a highly brittle unique key
		if isRAMCategory(category) {
			return "ram unknown " + norm
		}
	}

	// Special handling for TCG (Pokemon/Magic)
	if isTCGCategory(category) || isTCGProduct(norm) {
		return normalizeTCG(norm)
	}

	// Special handling for Storage (SSD/HDD)
	if isStorageProduct(norm) {
		if storageSpec := extractStorageSpecs(norm); storageSpec != "" {
			return storageSpec
		}
	}

	return norm
}

func normalizeProductNameWithRules(name string, rules []compiledRule) string {
	for _, rule := range rules {
		name = rule.re.ReplaceAllString(name, rule.replace)
	}

	name = strings.ToLower(name)
	name = nameReplacer.Replace(name)

	words := strings.Fields(name)
	return strings.Join(words, " ")
}

func isGPUCategory(category string) bool {
	cat := strings.ToLower(category)
	cat = strings.ReplaceAll(cat, "-", "")
	return strings.Contains(cat, "nvidiartx") || strings.Contains(cat, "amdrx") || extractGPUModelOK(cat)
}

func isGPUProduct(name string) bool {
	return extractGPUModelOK(name)
}

var (
	gpuRTXModelRegex     = regexp.MustCompile(`\brtx\s*(?:™\s*)?(\d{4})\s*(ti)?\b|\brtx(\d{4})(ti)?\b`)
	gpuRXModelRegex      = regexp.MustCompile(`\bradeon\s*rx\s*(\d{4})\s*(xt)?\b|\brx\s*(\d{4})\s*(xt)?\b|\brx(\d{4})(xt)?\b`)
	gpuGDDRVRAMRegex     = regexp.MustCompile(`\b(4|6|8|10|12|16|20|24|32|48)\s*g(?:b|o|d)?\s+gddr\d?\b|\bgddr\d?\s*(4|6|8|10|12|16|20|24|32|48)\s*g(?:b|o|d)?\b`)
	gpuVRAMRegex         = regexp.MustCompile(`\b(4|6|8|10|12|16|20|24|32|48)\s*g(?:b|o|d)?\b`)
	gpuMBVRAMRegex       = regexp.MustCompile(`\b(8192|16384|24576|32768|49152)\s*mb\b|\b(8|16|24|32|48)[.,](?:192|384|576|768)\s*mb\b`)
	gpuPartVRAMRegex     = regexp.MustCompile(`\bo(4|6|8|10|12|16|20|24|32|48)g\b`)
	gpuTrailingVRAMRegex = regexp.MustCompile(`\b(4|6|8|10|12|16|20|24|32|48)\b\s*$`)
)

func extractGPUModelOK(value string) bool {
	_, _, ok := extractGPUModel(value)
	return ok
}

func extractGPUSpecs(name, category string) string {
	categoryFamily, categoryModel, categoryOK := extractGPUModel(category)
	family, model, ok := extractGPUModel(name)
	if !ok {
		family, model, ok = categoryFamily, categoryModel, categoryOK
	}
	if !ok {
		return ""
	}
	if categoryOK && categoryFamily == family && isMoreSpecificGPUModel(categoryModel, model) {
		model = categoryModel
	}

	modelKey := family + " " + model
	vram := extractGPUVRAM(name)
	vram = normalizeGPUVRAMForModel(modelKey, vram)
	if vram == "" {
		vram = "unknown-vram"
	}

	return "gpu " + modelKey + " " + vram
}

func isMoreSpecificGPUModel(candidate, current string) bool {
	if candidate == "" || current == "" || candidate == current {
		return false
	}
	return strings.HasPrefix(candidate, current+" ")
}

func extractGPUModel(value string) (family, model string, ok bool) {
	if model, ok := extractRTXModel(value); ok {
		return "rtx", model, true
	}
	if model, ok := extractRXModel(value); ok {
		return "rx", model, true
	}
	return "", "", false
}

func extractRTXModel(value string) (string, bool) {
	value = strings.ToLower(value)
	match := gpuRTXModelRegex.FindStringSubmatch(value)
	if len(match) == 0 {
		return "", false
	}

	model := match[1]
	ti := match[2]
	if model == "" {
		model = match[3]
		ti = match[4]
	}
	if model == "" {
		return "", false
	}
	if ti != "" {
		return model + " ti", true
	}
	return model, true
}

func extractRXModel(value string) (string, bool) {
	value = strings.ToLower(value)
	value = strings.NewReplacer("-", " ", "_", " ").Replace(value)
	match := gpuRXModelRegex.FindStringSubmatch(value)
	if len(match) == 0 {
		return "", false
	}

	model := match[1]
	xt := match[2]
	if model == "" {
		model = match[3]
		xt = match[4]
	}
	if model == "" {
		model = match[5]
		xt = match[6]
	}
	if model == "" {
		return "", false
	}
	if xt != "" {
		return model + " xt", true
	}
	return model, true
}

func extractGPUVRAM(name string) string {
	if match := gpuPartVRAMRegex.FindStringSubmatch(name); len(match) > 0 {
		return match[1] + "gb"
	}
	if match := gpuMBVRAMRegex.FindStringSubmatch(name); len(match) > 0 {
		if match[1] != "" {
			switch match[1] {
			case "8192":
				return "8gb"
			case "16384":
				return "16gb"
			case "24576":
				return "24gb"
			case "32768":
				return "32gb"
			case "49152":
				return "48gb"
			}
		}
		return match[2] + "gb"
	}
	if match := gpuGDDRVRAMRegex.FindStringSubmatch(name); len(match) > 0 {
		if match[1] != "" {
			return match[1] + "gb"
		}
		return match[2] + "gb"
	}

	window := gpuModelWindow(name)
	if window == "" {
		return ""
	}
	for _, loc := range gpuVRAMRegex.FindAllStringSubmatchIndex(window, -1) {
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		after := strings.TrimSpace(window[loc[1]:])
		if strings.HasPrefix(after, "ram") {
			continue
		}
		return window[loc[2]:loc[3]] + "gb"
	}
	if match := gpuTrailingVRAMRegex.FindStringSubmatch(window); len(match) > 0 {
		return match[1] + "gb"
	}
	return ""
}

func gpuModelWindow(name string) string {
	loc := gpuRTXModelRegex.FindStringIndex(name)
	if len(loc) == 0 {
		loc = gpuRXModelRegex.FindStringIndex(name)
	}
	if len(loc) == 0 {
		return ""
	}
	end := loc[1] + 80
	if end > len(name) {
		end = len(name)
	}
	return name[loc[0]:end]
}

func defaultGPUVRAM(model string) string {
	switch model {
	case "rtx 4090":
		return "24gb"
	case "rtx 5060":
		return "8gb"
	case "rtx 5070":
		return "12gb"
	case "rtx 5070 ti":
		return "16gb"
	case "rtx 5080":
		return "16gb"
	case "rtx 5090":
		return "32gb"
	case "rx 9060 xt":
		return ""
	case "rx 9070":
		return "16gb"
	case "rx 9070 xt":
		return "16gb"
	default:
		return ""
	}
}

func normalizeGPUVRAMForModel(model, extracted string) string {
	allowed := allowedGPUVRAM(model)
	if len(allowed) == 0 {
		return extracted
	}
	if extracted != "" {
		for _, value := range allowed {
			if extracted == value {
				return extracted
			}
		}
	}
	return defaultGPUVRAM(model)
}

func allowedGPUVRAM(model string) []string {
	switch model {
	case "rtx 4090":
		return []string{"24gb"}
	case "rtx 5060":
		return []string{"8gb"}
	case "rtx 5060 ti":
		return []string{"8gb", "16gb"}
	case "rtx 5070":
		return []string{"12gb"}
	case "rtx 5070 ti":
		return []string{"16gb"}
	case "rtx 5080":
		return []string{"16gb"}
	case "rtx 5090":
		return []string{"32gb"}
	case "rx 9060 xt":
		return []string{"8gb", "16gb"}
	case "rx 9070":
		return []string{"16gb"}
	case "rx 9070 xt":
		return []string{"16gb"}
	default:
		return nil
	}
}

func isRAMCategory(category string) bool {
	cat := strings.ToLower(category)
	if strings.Contains(cat, "ddr5") || strings.Contains(cat, "ddr4") || regexp.MustCompile(`\bram\b`).MatchString(cat) {
		return true
	}
	return false
}

func isRAMProduct(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "ddr5") || strings.Contains(name, "ddr4") || regexp.MustCompile(`\b(?:ram|memory|dimm)\b`).MatchString(name)
}

var (
	ramCapacityRegex  = regexp.MustCompile(`\b(\d+)\s*(?:gb|g|go)\b`)
	ramConfigRegex    = regexp.MustCompile(`\b(\d+)\s*[x*]\s*(\d+)\s*(?:gb|g|go)?\b`)
	ramConfigRevRegex = regexp.MustCompile(`\b(\d+)\s*(?:gb|g|go)\s*[x*]\s*(\d+)\b`)
	ramTruncatedRegex = regexp.MustCompile(`\b(8|16|24|32|48|64|96|128|192)\s*(?:\.{3,}|…)$`)
	ramSpeedRegex     = regexp.MustCompile(`\b(\d{4})\s*(?:mhz|mt/s|mts)\b`)
	ramCLRegex        = regexp.MustCompile(`\bcl\s*(\d{2})\b`)

	// Manufacturer Part Numbers
	// G.Skill: F5-6000J3038F16GX2-TZ5N (16GB x 2 = 32GB)
	ramGSkillPNRegex = regexp.MustCompile(`(?i)\bf[45][-\s]*\w+?(\d+)gx(\d+)\b`)
	// Kingston: KF560C36BBEAK2-32 (Kit of 2, 32GB total) or KF560C36BBE-8 (8GB total)
	ramKingstonPNRegex = regexp.MustCompile(`(?i)\bkf[45]\w+?(?:k(\d+))?[-\s]*(\d{1,3})\b`)
	// TeamGroup: TED532G4800C40DC01 (32GB total, DC = Dual Channel = 2 sticks)
	ramTeamPNRegex = regexp.MustCompile(`(?i)\b(?:ted|ff|ctced)[45](\d{1,3})g\w+?(dc|hc)?01\b`)
)

func extractRAMSpecs(name string) string {
	var countStr, sizeStr string
	var totalCapacity int

	speedMatch := ramSpeedRegex.FindStringSubmatch(name)
	clMatch := ramCLRegex.FindStringSubmatch(name)

	suffix := ""
	if speedMatch != nil {
		suffix += " " + speedMatch[1]
	}
	if clMatch != nil {
		suffix += " cl" + clMatch[1]
	}

	// 1. Try Part Numbers First (most precise)
	if match := ramGSkillPNRegex.FindStringSubmatch(name); len(match) > 0 {
		size, _ := strconv.Atoi(match[1])
		count, _ := strconv.Atoi(match[2])
		return fmt.Sprintf("ram %dgb %dx%dgb%s", size*count, count, size, suffix)
	}
	if match := ramKingstonPNRegex.FindStringSubmatch(name); len(match) > 0 {
		total, _ := strconv.Atoi(match[2])
		if match[1] != "" {
			count, _ := strconv.Atoi(match[1])
			if count > 0 && total%count == 0 {
				return fmt.Sprintf("ram %dgb %dx%dgb%s", total, count, total/count, suffix)
			}
		}
		return fmt.Sprintf("ram %dgb%s", total, suffix)
	}
	if match := ramTeamPNRegex.FindStringSubmatch(name); len(match) > 0 {
		total, _ := strconv.Atoi(match[1])
		if strings.ToLower(match[2]) == "dc" {
			return fmt.Sprintf("ram %dgb 2x%dgb%s", total, total/2, suffix)
		}
		return fmt.Sprintf("ram %dgb%s", total, suffix)
	}

	// 2. Try standard config (e.g. 2x16)
	if match := ramConfigRegex.FindStringSubmatch(name); len(match) > 0 {
		countStr = match[1]
		sizeStr = match[2]
	} else if match := ramConfigRevRegex.FindStringSubmatch(name); len(match) > 0 {
		// Try reversed config (e.g. 16gb x 2)
		sizeStr = match[1]
		countStr = match[2]
	}

	if countStr != "" && sizeStr != "" {
		count, _ := strconv.Atoi(countStr)
		size, _ := strconv.Atoi(sizeStr)
		totalCapacity = count * size
		return fmt.Sprintf("ram %dgb %dx%dgb%s", totalCapacity, count, size, suffix)
	}

	// 3. Try plain capacity (e.g. 16gb)
	if match := ramCapacityRegex.FindStringSubmatch(name); len(match) > 0 {
		return fmt.Sprintf("ram %sgb%s", match[1], suffix)
	}

	// 4. Try truncated capacity (e.g. DDR5 16...)
	if match := ramTruncatedRegex.FindStringSubmatch(name); len(match) > 0 {
		return fmt.Sprintf("ram %sgb%s", match[1], suffix)
	}

	return ""
}

func isTCGCategory(category string) bool {
	cat := strings.ToLower(category)
	return strings.Contains(cat, "pokemon") || strings.Contains(cat, "magic") || strings.Contains(cat, "tcg") || strings.Contains(cat, "mtg")
}

func isTCGProduct(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "pokemon") || strings.Contains(name, "magic the gathering") || strings.Contains(name, "tcg") || strings.Contains(name, "booster box") || strings.Contains(name, "etb")
}

var tcgTypes = []string{
	"collector booster", "booster box", "booster pack", "elite trainer box", "elite trainer", "etb",
	"sleeved booster", "sleeved", "blister", "case", "triple pack", "premium collection", "ultra premium", "upc",
	"starter deck", "commander deck", "deck", "bundle", "mini tin", "tin",
	"small pack", "big pack", "booster", "mega premium", "box set",
}

func normalizeTCG(name string) string {
	// Preserve specific TCG keywords that differentiate products
	var foundType string
	for _, t := range tcgTypes {
		if strings.Contains(name, t) {
			foundType = t
			break
		}
	}

	// Remove common prefixes/suffixes to find the "Set Name"
	set := name
	set = strings.ReplaceAll(set, "pokemon tcg", "")
	set = strings.ReplaceAll(set, "pokemon", "")
	set = strings.ReplaceAll(set, "magic the gathering", "")
	set = strings.ReplaceAll(set, "magic", "")
	set = strings.ReplaceAll(set, "card game", "")
	set = strings.TrimSpace(set)

	if foundType != "" {
		set = strings.ReplaceAll(set, foundType, "")
		set = strings.TrimSpace(set)
		// If set name is truncated, it's still better than just the set name
		return fmt.Sprintf("tcg %s %s", set, foundType)
	}

	return "tcg " + set
}

func isStorageProduct(name string) bool {
	name = strings.ToLower(name)
	return strings.Contains(name, "ssd") || strings.Contains(name, "nvme") || strings.Contains(name, "hard drive") || strings.Contains(name, "hdd") || strings.Contains(name, "internal drive") || strings.Contains(name, "external drive")
}

var (
	storageCapacityRegex = regexp.MustCompile(`\b(\d+)\s*(?:tb|gb)\b`)
)

func extractStorageSpecs(name string) string {
	// Extract capacity (e.g. 1TB, 2TB, 500GB)
	match := storageCapacityRegex.FindStringSubmatch(strings.ToLower(name))
	if len(match) > 1 {
		capacity := match[0]
		// Find the brand if possible (Samsung, Western Digital, WD, Seagate, Crucial)
		brand := ""
		brands := []string{"samsung", "western digital", "wd", "seagate", "crucial", "kingston", "sandisk", "pny", "lexar", "sabrent"}
		for _, b := range brands {
			if strings.Contains(strings.ToLower(name), b) {
				brand = b
				break
			}
		}

		// Find model if possible (980 pro, 990 pro, sn850x, sn770, etc.)
		model := ""
		models := []string{"980 pro", "990 pro", "sn850x", "sn770", "sn850", "sn570", "sn750", "t7", "t5", "mx500", "p5 plus"}
		for _, m := range models {
			if strings.Contains(strings.ToLower(name), m) {
				model = m
				break
			}
		}

		res := "storage"
		if brand != "" {
			res += " " + brand
		}
		if model != "" {
			res += " " + model
		}
		res += " " + capacity
		return res
	}
	return ""
}

func isAmbiguous(normName string, truncated bool) bool {
	// If it's already specialized (starts with ram or tcg), it's not ambiguous enough to skip
	if strings.HasPrefix(normName, "ram ") || strings.HasPrefix(normName, "tcg ") {
		return false
	}

	words := strings.Fields(normName)
	if len(words) == 0 {
		return true
	}

	// 1-word names are almost always ambiguous (e.g. "Monitor")
	if len(words) == 1 {
		return true
	}

	return false
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func percentile(slice []float64, pct float64) float64 {
	if len(slice) == 0 {
		return 0
	}
	temp := make([]float64, len(slice))
	copy(temp, slice)
	sort.Float64s(temp)

	idx := float64(len(temp)-1) * (pct / 100.0)
	low := int(math.Floor(idx))
	high := int(math.Ceil(idx))
	if high >= len(temp) {
		return temp[low]
	}
	if low == high {
		return temp[low]
	}
	weight := idx - float64(low)
	return temp[low]*(1-weight) + temp[high]*weight
}
