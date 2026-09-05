package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// Store owns one SQLite database. It has no dependency on the combined bot,
// its document collections, another process, or a database server.
type Store struct{ db *sql.DB }

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	// Check before changing journal mode or adding tables. Never initialize
	// this schema inside another bot's database.
	if err = validateStoreSchema(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	_, err = db.ExecContext(ctx, `PRAGMA journal_mode=WAL;
 PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS deals(id TEXT PRIMARY KEY,payload TEXT NOT NULL,published_at INTEGER NOT NULL,updated_at INTEGER NOT NULL);
 CREATE INDEX IF NOT EXISTS deals_published ON deals(published_at);
 CREATE TABLE IF NOT EXISTS subscriptions(guild_id TEXT NOT NULL,channel_id TEXT NOT NULL,filter TEXT NOT NULL,payload TEXT NOT NULL,PRIMARY KEY(guild_id,channel_id,filter));
 CREATE TABLE IF NOT EXISTS settings(key TEXT PRIMARY KEY,payload TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) GetDealByID(ctx context.Context, id string) (*models.DealInfo, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM deals WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeDeal(id, data)
}

func decodeDeal(id string, data []byte) (*models.DealInfo, error) {
	var d models.DealInfo
	if err := json.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("decode deal %q: %w", id, err)
	}
	if id == "" || d.DocumentID != id {
		return nil, fmt.Errorf("deal %q has inconsistent stored identity", id)
	}
	return &d, nil
}
func (s *Store) GetDealsByIDs(ctx context.Context, ids []string) (map[string]*models.DealInfo, error) {
	out := make(map[string]*models.DealInfo, len(ids))
	// Bound bind parameters for large imports, without issuing one query per
	// scraped item. Values are parameters, never interpolated into SQL.
	for start := 0; start < len(ids); start += 900 {
		batch := ids[start:min(start+900, len(ids))]
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		query := "SELECT id,payload FROM deals WHERE id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + ")"
		deals, err := s.deals(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for _, d := range deals {
			out[d.DocumentID] = &d
		}
	}
	// Thread identities survive longer than the 48-hour fuzzy-match window.
	// Resolve only missing IDs, so a retained duplicate cannot become a fresh
	// alert merely because its canonical record is older than that window.
	var missing []string
	for _, id := range ids {
		if out[id] == nil {
			missing = append(missing, id)
		}
	}
	for start := 0; start < len(missing); start += 900 {
		batch := missing[start:min(start+900, len(missing))]
		args := make([]any, len(batch))
		wanted := make(map[string]bool, len(batch))
		for i, id := range batch {
			args[i], wanted[id] = id, true
		}
		query := "SELECT id,payload FROM deals WHERE EXISTS (SELECT 1 FROM json_each(deals.payload,'$.Threads') AS thread WHERE json_extract(thread.value,'$.DocumentID') IN (" + strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",") + "))"
		deals, err := s.deals(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("load retained thread identities: %w", err)
		}
		for _, d := range deals {
			for _, thread := range d.Threads {
				if !wanted[thread.DocumentID] {
					continue
				}
				if prior := out[thread.DocumentID]; prior != nil && prior.DocumentID != d.DocumentID {
					return nil, fmt.Errorf("thread %q has ambiguous stored ownership", thread.DocumentID)
				}
				out[thread.DocumentID] = &d
			}
		}
	}
	return out, nil
}
func (s *Store) GetRecentDeals(ctx context.Context, age time.Duration) ([]models.DealInfo, error) {
	return s.deals(ctx, "SELECT id,payload FROM deals WHERE published_at>=? ORDER BY published_at,id", time.Now().Add(-age).UnixNano())
}

func (s *Store) deals(ctx context.Context, query string, args ...any) ([]models.DealInfo, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.DealInfo
	for rows.Next() {
		var b []byte
		var id string
		if err = rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		d, err := decodeDeal(id, b)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}
func (s *Store) TryCreateDeal(ctx context.Context, d models.DealInfo) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	r, err := s.db.ExecContext(ctx, "INSERT INTO deals(id,payload,published_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", d.DocumentID, b, d.PublishedTimestamp.UnixNano(), d.LastUpdated.UnixNano())
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return models.ErrDealExists
	}
	return err
}
func (s *Store) UpdateDeal(ctx context.Context, d models.DealInfo) error {
	return s.BatchWrite(ctx, nil, []models.DealInfo{d})
}
func (s *Store) BatchWrite(ctx context.Context, creates, updates []models.DealInfo) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, set := range []struct {
		items  []models.DealInfo
		create bool
	}{{creates, true}, {updates, false}} {
		for _, d := range set.items {
			b, e := json.Marshal(d)
			if e != nil {
				return e
			}
			query := "INSERT INTO deals(id,payload,published_at,updated_at) VALUES(?,?,?,?)"
			if !set.create {
				query += " ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,published_at=excluded.published_at,updated_at=excluded.updated_at"
			}
			if _, e = tx.ExecContext(ctx, query, d.DocumentID, b, d.PublishedTimestamp.UnixNano(), d.LastUpdated.UnixNano()); e != nil {
				return e
			}
		}
	}
	return tx.Commit()
}
func (s *Store) TrimOldDeals(ctx context.Context, max int) error {
	if max < 1 {
		return fmt.Errorf("retention limit must be positive")
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM deals WHERE id IN (SELECT id FROM deals ORDER BY updated_at DESC,id LIMIT -1 OFFSET ?)", max)
	return err
}

func (s *Store) SaveSubscription(ctx context.Context, sub models.Subscription) error {
	if sub.GuildID == "" || sub.ChannelID == "" || !dealtypes.IsRFD(sub.DealType) || !sub.IsRFD() {
		return fmt.Errorf("invalid RFD subscription")
	}
	sub.SubscriptionType = "rfd"
	b, err := json.Marshal(sub)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO subscriptions(guild_id,channel_id,filter,payload) VALUES(?,?,?,?) ON CONFLICT(guild_id,channel_id,filter) DO UPDATE SET payload=excluded.payload", sub.GuildID, sub.ChannelID, sub.DealType, b)
	return err
}
func (s *Store) RemoveSubscription(ctx context.Context, guild, channel, filter string) error {
	if guild == "" || channel == "" {
		return fmt.Errorf("guild and channel are required")
	}
	if filter == "" {
		_, err := s.db.ExecContext(ctx, "DELETE FROM subscriptions WHERE guild_id=? AND channel_id=?", guild, channel)
		return err
	}
	_, err := s.db.ExecContext(ctx, "DELETE FROM subscriptions WHERE guild_id=? AND channel_id=? AND filter=?", guild, channel, filter)
	return err
}
func (s *Store) subscriptions(ctx context.Context, guild string) ([]models.Subscription, error) {
	query := "SELECT guild_id,channel_id,filter,payload FROM subscriptions"
	var args []any
	if guild != "" {
		query += " WHERE guild_id=?"
		args = append(args, guild)
	}
	query += " ORDER BY guild_id,channel_id,filter"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Subscription
	for rows.Next() {
		var b []byte
		var sub models.Subscription
		var guildID, channelID, filter string
		if err = rows.Scan(&guildID, &channelID, &filter, &b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &sub); err != nil {
			return nil, err
		}
		if sub.GuildID != guildID || sub.ChannelID != channelID || sub.DealType != filter || guildID == "" || channelID == "" || !sub.IsRFD() || !dealtypes.IsRFD(filter) {
			return nil, fmt.Errorf("subscription has inconsistent stored scope")
		}
		out = append(out, sub)
	}
	return out, rows.Err()
}
func (s *Store) GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error) {
	return s.subscriptions(ctx, "")
}
func (s *Store) GetSubscriptionsByGuild(ctx context.Context, guild string) ([]models.Subscription, error) {
	if guild == "" {
		return nil, fmt.Errorf("guild is required")
	}
	return s.subscriptions(ctx, guild)
}

func (s *Store) GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error) {
	var b []byte
	err := s.db.QueryRowContext(ctx, "SELECT payload FROM settings WHERE key='gemini-quota'").Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var q models.GeminiQuotaStatus
	err = json.Unmarshal(b, &q)
	return &q, err
}
func (s *Store) UpdateGeminiQuotaStatus(ctx context.Context, q models.GeminiQuotaStatus) error {
	b, err := json.Marshal(q)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO settings(key,payload) VALUES('gemini-quota',?) ON CONFLICT(key) DO UPDATE SET payload=excluded.payload", b)
	return err
}
