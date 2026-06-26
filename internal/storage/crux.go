package storage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/pauljones0/rfd-discord-bot/internal/crux"
)

const (
	cruxCompaniesCollection = "crux_companies"
	cruxChangesCollection   = "crux_changes"
)

func (c *Client) GetCruxCompanies(ctx context.Context) (map[string]crux.Company, error) {
	rows, err := c.ListDocuments(ctx, cruxCompaniesCollection)
	if err != nil {
		return nil, err
	}
	companies := make(map[string]crux.Company, len(rows))
	for _, row := range rows {
		var company crux.Company
		if err := decodeDocument(row.Data, &company); err != nil {
			slog.Warn("Failed to decode Crux company", "processor", "crux", "id", row.ID, "error", err)
			continue
		}
		if company.Key == "" {
			company.Key = row.ID
		}
		companies[company.Key] = company
	}
	return companies, nil
}

func (c *Client) SaveCruxSnapshot(ctx context.Context, companies []crux.Company, changes []crux.Change) error {
	companyDocs, err := cruxCompanyDocuments(companies)
	if err != nil {
		return err
	}
	changeDocs, err := cruxChangeDocuments(changes)
	if err != nil {
		return err
	}
	return c.saveCruxDocuments(ctx, companyDocs, changeDocs)
}

func (c *Client) SaveCruxCompanies(ctx context.Context, companies []crux.Company) error {
	docs, err := cruxCompanyDocuments(companies)
	if err != nil {
		return err
	}
	return c.SetRawDocuments(ctx, cruxCompaniesCollection, docs)
}

func (c *Client) SaveCruxChanges(ctx context.Context, changes []crux.Change) error {
	docs, err := cruxChangeDocuments(changes)
	if err != nil {
		return err
	}
	return c.SetRawDocuments(ctx, cruxChangesCollection, docs)
}

func (c *Client) saveCruxDocuments(ctx context.Context, companyDocs, changeDocs map[string]map[string]any) error {
	if len(companyDocs) == 0 && len(changeDocs) == 0 {
		return nil
	}
	tx, err := c.pg.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin crux snapshot transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if len(companyDocs) > 0 {
		if err := setRawDocuments(ctx, tx, cruxCompaniesCollection, companyDocs); err != nil {
			return fmt.Errorf("save crux companies: %w", err)
		}
	}
	if len(changeDocs) > 0 {
		if err := setRawDocuments(ctx, tx, cruxChangesCollection, changeDocs); err != nil {
			return fmt.Errorf("save crux changes: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit crux snapshot transaction: %w", err)
	}
	return nil
}

func cruxCompanyDocuments(companies []crux.Company) (map[string]map[string]any, error) {
	docs := make(map[string]map[string]any, len(companies))
	for _, company := range companies {
		if company.Key == "" {
			continue
		}
		data, err := encodeDocument(company)
		if err != nil {
			return nil, fmt.Errorf("encode crux company %s: %w", company.Key, err)
		}
		docs[company.Key] = data
	}
	return docs, nil
}

func cruxChangeDocuments(changes []crux.Change) (map[string]map[string]any, error) {
	docs := make(map[string]map[string]any, len(changes))
	for _, change := range changes {
		docID := crux.ChangeDocID(change)
		if docID == "" {
			return nil, fmt.Errorf("crux change for %q has empty document id", change.Key)
		}
		change.ID = docID
		data, err := encodeDocument(change)
		if err != nil {
			return nil, fmt.Errorf("encode crux change %s: %w", docID, err)
		}
		docs[docID] = data
	}
	return docs, nil
}
