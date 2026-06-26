package crux

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type Store interface {
	GetCruxCompanies(ctx context.Context) (map[string]Company, error)
	SaveCruxSnapshot(ctx context.Context, companies []Company, changes []Change) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

type Notifier interface {
	SendCruxChange(ctx context.Context, change Change, subs []models.Subscription) error
}

type Processor struct {
	store     Store
	client    *Client
	notifier  Notifier
	exchanges map[string]struct{}
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
	return &Processor{store: store, client: client, notifier: notifier, exchanges: allowed}
}

func (p *Processor) ProcessCruxChanges(ctx context.Context) error {
	if p.client == nil {
		return fmt.Errorf("crux client is not configured")
	}
	crawl, err := p.client.FetchAll(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	current := p.currentCanadianCompanies(crawl.Companies, now)
	if len(current) == 0 {
		return fmt.Errorf("crux crawl found no Canadian-listed companies")
	}

	existing, err := p.store.GetCruxCompanies(ctx)
	if err != nil {
		return fmt.Errorf("load crux companies: %w", err)
	}
	baseline := len(existing) == 0
	writes, changes := diffCompanies(existing, current, now, baseline)

	if len(writes) > 0 || len(changes) > 0 {
		if err := p.store.SaveCruxSnapshot(ctx, writes, changes); err != nil {
			return fmt.Errorf("save crux snapshot: %w", err)
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
