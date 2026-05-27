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

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	minPriceObservationsForAlert = 10
	maxPriceHistoryEntries       = 100
)

// Store abstracts database operations for the Core processor.
type Store interface {
	GetCorePriceHistory(ctx context.Context, productName string) (*models.CorePriceHistory, bool, error)
	SaveCorePriceHistory(ctx context.Context, history models.CorePriceHistory) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
	GetCoreRules(ctx context.Context) ([]models.CoreRule, error)
}

// Notifier abstracts Discord notifications.
type Notifier interface {
	SendCoreDeal(ctx context.Context, deal models.CoreDeal, subs []models.Subscription) (map[string]string, error)
}

// Processor handles parsing and tracking Core deal alerts without AI.
type Processor struct {
	store    Store
	notifier Notifier
	rates    *RateManager

	locksMu      sync.Mutex
	productLocks map[string]*sync.Mutex
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
	ShouldSignal bool
	Reason       string
}

var (
	countryTagRegex  = regexp.MustCompile(`\s*[\x{2068}\x{2069}]*@[a-zA-Z0-9]+\s*[\x{2068}\x{2069}]*`)
	urlRegex         = regexp.MustCompile(`https?://[^\s<>"']+`)
	priceNumberRegex = regexp.MustCompile(`[0-9]+(?:[,\s][0-9]{3})*(?:\.[0-9]{1,2})?|[0-9]+(?:\.[0-9]{1,2})?`)
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
	match = strings.ReplaceAll(match, ",", "")
	match = strings.ReplaceAll(match, " ", "")
	price, err := strconv.ParseFloat(match, 64)
	if err != nil || price <= 0 {
		return 0, false
	}
	return price, true
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

// ProcessNotification ingests, parses, checks price percentiles, and alerts if eligible.
func (p *Processor) ProcessNotification(ctx context.Context, title, message string, lines []string, eventID, sourcePackage string) error {
	slog.Info("Core bot: Processing notification", "event_id", eventID, "source_package", sourcePackage)

	parsed, ok := p.parseNotificationCandidates(message, lines, title)
	if !ok {
		slog.Info("Core bot: Notification is not a valid deal format, skipping", "event_id", eventID, "message", message)
		return nil
	}

	rules, err := p.store.GetCoreRules(ctx)
	if err != nil {
		slog.Error("Core bot: Failed to fetch core rules, proceeding without rules", "error", err)
	}
	compiledRules, ruleErr := compileRules(rules)
	if ruleErr != nil {
		slog.Warn("Core bot: Ignoring invalid normalization rules", "error", ruleErr)
	}

	normName := normalizeProductNameWithRules(parsed.ProductName, compiledRules)
	if normName == "" {
		normName = NormalizeProductName(parsed.ProductName, nil)
	}
	category := ParseCategoryFromTitle(title)
	priceCAD := p.rates.ConvertToCAD(parsed.Price, parsed.Currency)

	lock := p.productLock(normName)
	lock.Lock()
	defer lock.Unlock()

	history, historyLoaded, err := p.loadPriceHistory(ctx, normName, category)
	if err != nil {
		slog.Error("Core bot: Failed to fetch price history, proceeding with empty history", "product", parsed.ProductName, "normalized", normName, "error", err)
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

	appendPriceObservation(history, priceCAD, eventID)
	history.Category = category
	history.LastUpdated = time.Now()
	if err := p.store.SaveCorePriceHistory(ctx, *history); err != nil {
		return fmt.Errorf("failed to save core price history for %q: %w", normName, err)
	}

	if !evaluation.ShouldSignal {
		return nil
	}

	coreSubs, err := p.coreSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(coreSubs) == 0 {
		slog.Info("Core bot: No active subscriptions for core alerts")
		return nil
	}

	deal := models.CoreDeal{
		EventID:       eventID,
		SourcePackage: sourcePackage,
		ProductName:   parsed.ProductName,
		StoreName:     parsed.StoreName,
		Category:      category,
		PriceCAD:      priceCAD,
		OriginalPrice: parsed.Price,
		OriginalCurr:  parsed.Currency,
		Link:          parsed.Link,
		ReceivedAt:    time.Now(),
		MinPriceSeen:  minFloat(priceCAD, evaluation.PriorMin),
		P25PriceSeen:  evaluation.PriorP25,
		HistoryCount:  len(history.Prices),
	}

	slog.Info("Core bot: Price drop/low price detected; sending alert", "product", parsed.ProductName, "store", parsed.StoreName, "price_cad", priceCAD, "min_cad", deal.MinPriceSeen, "p25_cad", deal.P25PriceSeen, "history_count", deal.HistoryCount)
	if _, err := p.notifier.SendCoreDeal(ctx, deal, coreSubs); err != nil {
		return fmt.Errorf("failed to send core deal alerts: %w", err)
	}
	return nil
}

func (p *Processor) parseNotificationCandidates(message string, lines []string, title string) (ParsedNotification, bool) {
	candidates := make([]string, 0, len(lines)+2)
	candidates = append(candidates, message)
	candidates = append(candidates, lines...)
	candidates = append(candidates, title)

	seen := make(map[string]struct{}, len(candidates))
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
			return parsed, true
		}
	}
	return ParsedNotification{}, false
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
	ev := priceEvaluation{PriorCount: len(priorPrices)}
	if len(priorPrices) == 0 {
		ev.Reason = "first_observation"
		return ev
	}

	ev.PriorMin = minOf(priorPrices)
	ev.PriorMax = maxOf(priorPrices)
	ev.PriorP25 = percentile(priorPrices, 25)

	if len(priorPrices) < minPriceObservationsForAlert {
		ev.Reason = "insufficient_history"
		return ev
	}
	if priceCAD <= ev.PriorP25 && priceCAD < ev.PriorMax {
		ev.ShouldSignal = true
		ev.Reason = "at_or_below_p25"
		return ev
	}
	ev.Reason = "above_threshold"
	return ev
}

func appendPriceObservation(history *models.CorePriceHistory, priceCAD float64, eventID string) {
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
func NormalizeProductName(name string, rules []models.CoreRule) string {
	compiled, _ := compileRules(rules)
	return normalizeProductNameWithRules(name, compiled)
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

func minOf(slice []float64) float64 {
	if len(slice) == 0 {
		return 0
	}
	m := slice[0]
	for _, v := range slice[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func maxOf(slice []float64) float64 {
	if len(slice) == 0 {
		return 0
	}
	m := slice[0]
	for _, v := range slice[1:] {
		if v > m {
			m = v
		}
	}
	return m
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
