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

func (c *Client) SaveCruxCompanies(ctx context.Context, companies []crux.Company) error {
	if len(companies) == 0 {
		return nil
	}
	docs := make(map[string]any, len(companies))
	for _, company := range companies {
		if company.Key == "" {
			continue
		}
		docs[company.Key] = company
	}
	return c.SetDocuments(ctx, cruxCompaniesCollection, docs)
}

func (c *Client) SaveCruxChanges(ctx context.Context, changes []crux.Change) error {
	if len(changes) == 0 {
		return nil
	}
	docs := make(map[string]any, len(changes))
	for _, change := range changes {
		docID := crux.ChangeDocID(change)
		if docID == "" {
			return fmt.Errorf("crux change for %q has empty document id", change.Key)
		}
		change.ID = docID
		docs[docID] = change
	}
	return c.SetDocuments(ctx, cruxChangesCollection, docs)
}
