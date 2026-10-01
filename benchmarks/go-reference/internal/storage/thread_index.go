package storage

import (
	"context"
	"fmt"
)

// ensureThreadIndex upgrades old standalone databases atomically. Triggers keep
// aliases consistent for ordinary writes, migration imports and row pruning.
// Shared aliases remain legal so reads can reject ambiguous ownership.
func (s *Store) ensureThreadIndex(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='deal_threads')`).Scan(&existed); err != nil {
		return err
	}
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS deal_threads(thread_id TEXT NOT NULL,deal_id TEXT NOT NULL,PRIMARY KEY(thread_id,deal_id))`,
		`DROP TRIGGER IF EXISTS deals_threads_insert`,
		`DROP TRIGGER IF EXISTS deals_threads_update`,
		`CREATE TRIGGER IF NOT EXISTS deals_threads_insert AFTER INSERT ON deals BEGIN
		 INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT DISTINCT json_extract(thread.value,'$.DocumentID'),NEW.id
		 FROM json_each(CASE WHEN json_valid(NEW.payload) THEN NEW.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>'';
		 END`,
		`CREATE TRIGGER IF NOT EXISTS deals_threads_update AFTER UPDATE OF payload ON deals WHEN OLD.payload<>NEW.payload BEGIN
		 DELETE FROM deal_threads WHERE deal_id=OLD.id;
		 INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT DISTINCT json_extract(thread.value,'$.DocumentID'),NEW.id
		 FROM json_each(CASE WHEN json_valid(NEW.payload) THEN NEW.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>'';
		 END`,
		`CREATE TRIGGER IF NOT EXISTS deals_threads_delete AFTER DELETE ON deals BEGIN DELETE FROM deal_threads WHERE deal_id=OLD.id; END`,
		`CREATE INDEX IF NOT EXISTS deal_threads_deal ON deal_threads(deal_id)`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("initialize thread index: %w", err)
		}
	}
	if !existed {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO deal_threads(thread_id,deal_id)
		 SELECT json_extract(thread.value,'$.DocumentID'),deals.id
		 FROM deals,json_each(CASE WHEN json_valid(deals.payload) THEN deals.payload ELSE '{}' END,'$.Threads') AS thread
		 WHERE json_extract(thread.value,'$.DocumentID') IS NOT NULL AND json_extract(thread.value,'$.DocumentID')<>''`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
