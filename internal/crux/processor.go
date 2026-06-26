package crux

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const defaultSystemAlertTTL = time.Hour

type Store interface {
	GetCruxCompanies(ctx context.Context) (map[string]Company, error)
	SaveCruxSnapshot(ctx context.Context, companies []Company, changes []Change) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

type Notifier interface {
	SendCruxChange(ctx context.Context, change Change, subs []models.Subscription) error
	SendCruxSystemAlert(ctx context.Context, alert SystemAlert, subs []models.Subscription) error
}

type Processor struct {
	store          Store
	client         *Client
	notifier       Notifier
	exchanges      map[string]struct{}
	systemAlertTTL time.Duration
	alertState     systemAlertState
}

type systemAlertState struct {
	mu              sync.Mutex
	active          bool
	signature       string
	firstFailedAt   time.Time
	lastFailedAt    time.Time
	lastAlertSentAt time.Time
}

func NewProcessor(store Store, client *Client, notifier Notifier, exchanges []string) *Processor {
	allowed := make(map[string]struct{})
	for _, exchange := range exchanges {
		exchange = strings.ToUpper(strings.TrimSpace(exchange))
		if exchange != "" {
			allowed[exchange] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		for _, exchange := range []string{"TSXV", "TSX", "CSE"} {
			allowed[exchange] = struct{}{}
		}
	}
	return &Processor{
		store:          store,
		client:         client,
		notifier:       notifier,
		exchanges:      allowed,
		systemAlertTTL: defaultSystemAlertTTL,
	}
}

func (p *Processor) ProcessCruxChanges(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("crux client is not configured")
	}
	crawl, err := p.client.FetchAll(ctx)
	if err != nil {
		p.notifySystemFailure(ctx, fmt.Errorf("fetch crux companies: %w", err))
		return err
	}
	now := time.Now()
	current := p.currentCanadianCompanies(crawl.Companies, now)
	if len(current) == 0 {
		err := fmt.Errorf("crux crawl found no Canadian-listed companies")
		p.notifySystemFailure(ctx, err)
		return err
	}

	existing, err := p.store.GetCruxCompanies(ctx)
	if err != nil {
		err = fmt.Errorf("load crux companies: %w", err)
		p.notifySystemFailure(ctx, err)
		return err
	}
	baseline := len(existing) == 0
	writes, changes := diffCompanies(existing, current, now, baseline)

	if len(writes) > 0 || len(changes) > 0 {
		if err := p.store.SaveCruxSnapshot(ctx, writes, changes); err != nil {
			err = fmt.Errorf("save crux snapshot: %w", err)
			p.notifySystemFailure(ctx, err)
			return err
		}
	}

	slog.Info("Crux crawl complete",
		"pages", crawl.PagesFetched,
		"total_pages", crawl.TotalPages,
		"parsed_companies", len(crawl.Companies),
		"canadian_companies", len(current),
		"baseline", baseline,
		"changes", len(changes),
		"backends", formatBackendUse(crawl.BackendsUsed),
	)
	p.notifySystemRecovery(ctx)

	if baseline || len(changes) == 0 || p.notifier == nil {
		return nil
	}
	subs, err := p.cruxSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		slog.Info("No active Crux subscriptions for changes", "changes", len(changes))
		return nil
	}
	for _, change := range changes {
		if err := p.notifier.SendCruxChange(ctx, change, subs); err != nil {
			slog.Error("Failed to send Crux change alert", "key", change.Key, "type", change.Type, "error", err)
		}
	}
	return nil
}

func (p *Processor) notifySystemFailure(ctx context.Context, err error) {
	if p.store == nil || p.notifier == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}

	now := time.Now()
	signature := failureSignature(err)
	firstFailedAt, shouldSend := p.recordFailure(now, signature)
	if !shouldSend {
		return
	}

	alert := SystemAlert{
		Title:      "Crux Investor monitor failure",
		Severity:   "error",
		Component:  "crux-monitor",
		Details:    err.Error(),
		OccurredAt: now,
		Fields: []SystemAlertField{
			{Name: "Automatic handling", Value: "The current sweep was stopped without saving partial state. Each page tries the configured Crux backends before failing, and the scheduler will retry on the next poll."},
			{Name: "Alert suppression", Value: fmt.Sprintf("Matching failures are suppressed for %s unless the error changes.", p.alertTTL().String())},
			{Name: "First failed", Value: firstFailedAt.UTC().Format(time.RFC3339)},
		},
	}
	if p.sendSystemAlert(ctx, alert) {
		p.markFailureAlertSent(now, signature)
	}
}

func (p *Processor) notifySystemRecovery(ctx context.Context) {
	if p.store == nil || p.notifier == nil {
		p.clearFailure()
		return
	}
	firstFailedAt, lastFailedAt, hadFailure := p.activeFailure()
	if !hadFailure {
		return
	}

	now := time.Now()
	if p.sendSystemAlert(ctx, SystemAlert{
		Title:      "Crux Investor monitor recovered",
		Severity:   "info",
		Component:  "crux-monitor",
		Details:    "Crux crawl, parse, and state save completed successfully after a previous failure.",
		OccurredAt: now,
		Fields: []SystemAlertField{
			{Name: "First failed", Value: firstFailedAt.UTC().Format(time.RFC3339)},
			{Name: "Last failed", Value: lastFailedAt.UTC().Format(time.RFC3339)},
			{Name: "Recovered", Value: now.UTC().Format(time.RFC3339)},
		},
	}) {
		p.clearFailure()
	}
}

func (p *Processor) sendSystemAlert(parent context.Context, alert SystemAlert) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
	defer cancel()

	subs, err := p.cruxSubscriptions(ctx)
	if err != nil {
		slog.Error("Failed to load Crux subscriptions for system alert", "error", err)
		return false
	}
	if len(subs) == 0 {
		slog.Info("No active Crux subscriptions for system alert", "title", alert.Title)
		return false
	}

	if err := p.notifier.SendCruxSystemAlert(ctx, alert, subs); err != nil {
		slog.Error("Failed to send Crux system alert", "title", alert.Title, "error", err)
		return false
	}
	return true
}

func (p *Processor) recordFailure(now time.Time, signature string) (time.Time, bool) {
	p.alertState.mu.Lock()
	defer p.alertState.mu.Unlock()

	if !p.alertState.active || p.alertState.signature != signature {
		p.alertState.active = true
		p.alertState.signature = signature
		p.alertState.firstFailedAt = now
		p.alertState.lastFailedAt = now
		p.alertState.lastAlertSentAt = time.Time{}
		return p.alertState.firstFailedAt, true
	}

	p.alertState.lastFailedAt = now
	if p.alertState.lastAlertSentAt.IsZero() || now.Sub(p.alertState.lastAlertSentAt) >= p.alertTTL() {
		return p.alertState.firstFailedAt, true
	}
	return p.alertState.firstFailedAt, false
}

func (p *Processor) markFailureAlertSent(now time.Time, signature string) {
	p.alertState.mu.Lock()
	defer p.alertState.mu.Unlock()
	if p.alertState.active && p.alertState.signature == signature {
		p.alertState.lastAlertSentAt = now
	}
}

func (p *Processor) activeFailure() (time.Time, time.Time, bool) {
	p.alertState.mu.Lock()
	defer p.alertState.mu.Unlock()

	if !p.alertState.active {
		return time.Time{}, time.Time{}, false
	}
	return p.alertState.firstFailedAt, p.alertState.lastFailedAt, true
}

func (p *Processor) clearFailure() (time.Time, time.Time, bool) {
	p.alertState.mu.Lock()
	defer p.alertState.mu.Unlock()

	if !p.alertState.active {
		return time.Time{}, time.Time{}, false
	}
	firstFailedAt := p.alertState.firstFailedAt
	lastFailedAt := p.alertState.lastFailedAt
	p.alertState.active = false
	p.alertState.signature = ""
	p.alertState.firstFailedAt = time.Time{}
	p.alertState.lastFailedAt = time.Time{}
	p.alertState.lastAlertSentAt = time.Time{}
	return firstFailedAt, lastFailedAt, true
}

func (p *Processor) alertTTL() time.Duration {
	if p.systemAlertTTL <= 0 {
		return defaultSystemAlertTTL
	}
	return p.systemAlertTTL
}

func failureSignature(err error) string {
	signature := strings.Join(strings.Fields(err.Error()), " ")
	if len(signature) > 1000 {
		signature = signature[:1000]
	}
	return signature
}

func (p *Processor) currentCanadianCompanies(companies []Company, now time.Time) map[string]Company {
	current := make(map[string]Company)
	for _, company := range companies {
		if _, ok := p.exchanges[company.Exchange]; !ok {
			continue
		}
		company.Active = true
		company.LastSeenAt = now
		if company.FirstSeenAt.IsZero() {
			company.FirstSeenAt = now
		}
		current[company.Key] = company
	}
	return current
}

func diffCompanies(existing, current map[string]Company, now time.Time, baseline bool) ([]Company, []Change) {
	writes := make([]Company, 0, len(current))
	var changes []Change

	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		company := current[key]
		old, exists := existing[key]
		if !exists {
			company.FirstSeenAt = now
			company.LastChangedAt = now
			writes = append(writes, company)
			if !baseline {
				changes = append(changes, buildChange(ChangeAdded, Company{}, company, now))
			}
			continue
		}

		company.FirstSeenAt = firstTime(old.FirstSeenAt, now)
		company.LastChangedAt = old.LastChangedAt
		var changeType string
		switch {
		case !old.Active:
			changeType = ChangeAdded
		case old.HasCruxScore && company.HasCruxScore && company.CruxScore > old.CruxScore:
			changeType = ChangeUpgraded
		case old.HasCruxScore && company.HasCruxScore && company.CruxScore < old.CruxScore:
			changeType = ChangeDowngraded
		case !old.HasCruxScore && company.HasCruxScore:
			changeType = ChangeScoreAdded
		case old.HasCruxScore && !company.HasCruxScore:
			changeType = ChangeScoreRemoved
		}
		if changeType != "" {
			company.LastChangedAt = now
			company.RemovedAt = time.Time{}
			changes = append(changes, buildChange(changeType, old, company, now))
		}
		writes = append(writes, company)
	}

	if !baseline {
		existingKeys := make([]string, 0, len(existing))
		for key := range existing {
			existingKeys = append(existingKeys, key)
		}
		sort.Strings(existingKeys)
		for _, key := range existingKeys {
			old := existing[key]
			if !old.Active {
				continue
			}
			if _, ok := current[key]; ok {
				continue
			}
			old.Active = false
			old.RemovedAt = now
			old.LastChangedAt = now
			writes = append(writes, old)
			changes = append(changes, buildChange(ChangeDeleted, old, Company{}, now))
		}
	}

	return writes, changes
}

func buildChange(changeType string, oldCompany, newCompany Company, now time.Time) Change {
	company := newCompany
	if company.Key == "" {
		company = oldCompany
	}
	change := Change{
		Type:             changeType,
		Key:              company.Key,
		Name:             company.Name,
		Exchange:         company.Exchange,
		Symbol:           company.Symbol,
		Ticker:           company.Ticker,
		URL:              company.URL,
		DevelopmentStage: company.DevelopmentStage,
		Commodity:        company.Commodity,
		DetectedAt:       now,
		OldScore:         oldCompany.CruxScore,
		HasOldScore:      oldCompany.HasCruxScore,
		NewScore:         newCompany.CruxScore,
		HasNewScore:      newCompany.HasCruxScore,
	}
	change.ID = ChangeDocID(change)
	return change
}

func firstTime(value, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value
}

func (p *Processor) cruxSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	subs, err := p.store.GetAllSubscriptions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]models.Subscription, 0, len(subs))
	for _, sub := range subs {
		if sub.IsCrux() && dealtypes.IsCrux(sub.DealType) {
			out = append(out, sub)
		}
	}
	return out, nil
}

func formatBackendUse(values map[string]int) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, values[key]))
	}
	return strings.Join(parts, ",")
}
