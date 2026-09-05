package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/pauljones0/rfd-discord-bot/internal/dealtypes"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const MigrationVersion = 1

const (
	maxMigrationBytes = 64 << 20
	applicationKey    = "discord-application-id"
	migrationKey      = "migration-v1"
)

// Migration is an offline, versioned transfer of RFD subscriptions and history.
// Models intentionally retain their exported Go JSON field names.
type Migration struct {
	Version             int                   `json:"version"`
	SourceApplicationID string                `json:"source_application_id"`
	ExportedAt          time.Time             `json:"exported_at"`
	Subscriptions       []models.Subscription `json:"subscriptions"`
	Deals               []models.DealInfo     `json:"deals"`
}

type ImportOptions struct {
	SourceApplicationID string
	TargetApplicationID string
}

type ImportResult struct {
	Version             int       `json:"version"`
	SourceApplicationID string    `json:"source_application_id"`
	TargetApplicationID string    `json:"target_application_id"`
	ExportedAt          time.Time `json:"exported_at"`
	ImportedAt          time.Time `json:"imported_at"`
	Subscriptions       int       `json:"subscriptions"`
	Deals               int       `json:"deals"`
	MessageReceipts     int       `json:"message_receipts"`
}

// DecodeMigration rejects unsupported fields and trailing documents so schema
// mismatches cannot silently discard history. It never contacts Discord.
func DecodeMigration(r io.Reader) (*Migration, error) {
	limited := &io.LimitedReader{R: r, N: maxMigrationBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var migration Migration
	if err := decoder.Decode(&migration); err != nil {
		return nil, fmt.Errorf("decode migration: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON documents")
		}
		return nil, fmt.Errorf("migration must contain one JSON document: %w", err)
	}
	if limited.N <= 0 {
		return nil, fmt.Errorf("migration exceeds %d bytes", maxMigrationBytes)
	}
	return &migration, nil
}

func validDiscordID(id string) bool {
	n, err := strconv.ParseUint(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == id
}

func validDocumentID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, r := range id {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

func validWebURL(value string, required bool) bool {
	if value == "" {
		return !required
	}
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

func validTimestamp(value time.Time, required bool) bool {
	if value.IsZero() {
		return !required
	}
	return time.Unix(0, value.UnixNano()).Equal(value)
}

func validateMigration(m *Migration, options ImportOptions) error {
	if m == nil || m.Version != MigrationVersion {
		return fmt.Errorf("migration version must be %d", MigrationVersion)
	}
	if !validDiscordID(options.SourceApplicationID) || !validDiscordID(options.TargetApplicationID) {
		return errors.New("expected source and target application IDs must be Discord snowflakes")
	}
	if m.SourceApplicationID != options.SourceApplicationID {
		return errors.New("migration source application does not match expected source")
	}
	if !validTimestamp(m.ExportedAt, true) {
		return errors.New("migration exported_at must be a valid nonzero timestamp")
	}
	if m.Subscriptions == nil || m.Deals == nil {
		return errors.New("migration requires subscriptions and deals arrays (empty arrays are allowed)")
	}
	if len(m.Subscriptions) > 10000 || len(m.Deals) > 100000 {
		return errors.New("migration exceeds supported record counts")
	}
	subscriptions := make(map[string]bool, len(m.Subscriptions))
	channelGuilds := make(map[string]string)
	for i, sub := range m.Subscriptions {
		if !validDiscordID(sub.GuildID) || !validDiscordID(sub.ChannelID) {
			return fmt.Errorf("subscription %d has an invalid Discord ID", i)
		}
		// Older subscriptions stored usernames here. AddedBy is provenance,
		// never an API destination; preserve it without assuming a snowflake.
		if strings.ContainsFunc(sub.AddedBy, unicode.IsControl) {
			return fmt.Errorf("subscription %d has invalid attribution text", i)
		}
		if !sub.IsRFD() || !dealtypes.IsRFD(sub.DealType) || !validTimestamp(sub.AddedAt, false) {
			return fmt.Errorf("subscription %d has an invalid RFD filter, type, or timestamp", i)
		}
		key := sub.GuildID + "/" + sub.ChannelID + "/" + sub.DealType
		if subscriptions[key] {
			return fmt.Errorf("subscription %d duplicates an earlier record", i)
		}
		subscriptions[key] = true
		if guild := channelGuilds[sub.ChannelID]; guild != "" && guild != sub.GuildID {
			return fmt.Errorf("subscription %d assigns one channel to different guilds", i)
		}
		channelGuilds[sub.ChannelID] = sub.GuildID
	}
	deals := make(map[string]bool, len(m.Deals))
	for i, deal := range m.Deals {
		if !validDocumentID(deal.DocumentID) || strings.TrimSpace(deal.Title) == "" {
			return fmt.Errorf("deal %d requires a valid document ID and title", i)
		}
		if deals[deal.DocumentID] {
			return fmt.Errorf("deal %d duplicates an earlier document ID", i)
		}
		deals[deal.DocumentID] = true
		if !validWebURL(deal.PostURL, true) || !validWebURL(deal.ActualDealURL, false) || !validWebURL(deal.ThreadImageURL, false) {
			return fmt.Errorf("deal %d has an invalid HTTP(S) URL", i)
		}
		if !validTimestamp(deal.PublishedTimestamp, true) || !validTimestamp(deal.LastUpdated, false) || !validTimestamp(deal.DiscordLastUpdatedTime, false) || !validTimestamp(deal.ExpiresAt, false) {
			return fmt.Errorf("deal %d has an invalid timestamp", i)
		}
		for channelID, messageID := range deal.DiscordMessageIDs {
			if !validDiscordID(channelID) || !validDiscordID(messageID) {
				return fmt.Errorf("deal %d has an invalid Discord message receipt", i)
			}
		}
		for channelID, appID := range deal.DiscordMessageApplicationIDs {
			if deal.DiscordMessageIDs[channelID] == "" || appID != options.SourceApplicationID {
				return fmt.Errorf("deal %d has inconsistent Discord message ownership", i)
			}
		}
		threads := make(map[string]bool, len(deal.Threads))
		for j, thread := range deal.Threads {
			if !validDocumentID(thread.DocumentID) || !validWebURL(thread.PostURL, true) || thread.CommentCount < 0 || thread.ViewCount < 0 {
				return fmt.Errorf("deal %d thread %d has an invalid ID, URL, or count", i, j)
			}
			// A document can retain multiple URLs for one thread after a redirect.
			key := thread.DocumentID + "/" + thread.PostURL
			if threads[key] {
				return fmt.Errorf("deal %d thread %d duplicates an earlier thread", i, j)
			}
			threads[key] = true
		}
	}
	return nil
}

// BindApplication pins an empty database to its Discord application before any
// workers start. Existing or imported databases may only use the pinned app.
func (s *Store) BindApplication(ctx context.Context, applicationID string) error {
	if !validDiscordID(applicationID) {
		return errors.New("application ID must be a Discord snowflake")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM settings WHERE key=?", applicationKey).Scan(&existing)
	if err == nil {
		if existing != applicationID {
			return errors.New("database belongs to a different Discord application")
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM deals)+(SELECT COUNT(*) FROM subscriptions)").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("nonempty database has no application binding; migrate into an empty database with explicit application IDs")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO settings(key,payload) VALUES(?,?)", applicationKey, applicationID); err != nil {
		return err
	}
	return tx.Commit()
}

// ImportMigration atomically imports into an empty destination. A compatible
// application binding is the only pre-existing setting allowed. Re-running an
// import is rejected rather than merging or duplicating existing history.
func (s *Store) ImportMigration(ctx context.Context, m *Migration, options ImportOptions) (ImportResult, error) {
	var result ImportResult
	if err := validateMigration(m, options); err != nil {
		return result, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = validateStoreSchema(ctx, tx); err != nil {
		return result, err
	}
	var count int
	err = tx.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM deals)+(SELECT COUNT(*) FROM subscriptions)+(SELECT COUNT(*) FROM settings WHERE key<>?)", applicationKey).Scan(&count)
	if err != nil {
		return result, err
	}
	if count != 0 {
		return result, errors.New("migration destination is not empty; import into a new database")
	}
	var existing string
	err = tx.QueryRowContext(ctx, "SELECT payload FROM settings WHERE key=?", applicationKey).Scan(&existing)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err == nil && existing != options.TargetApplicationID {
		return result, errors.New("migration destination is bound to a different Discord application")
	}
	result = ImportResult{Version: MigrationVersion, SourceApplicationID: options.SourceApplicationID, TargetApplicationID: options.TargetApplicationID, ExportedAt: m.ExportedAt, ImportedAt: time.Now().UTC(), Subscriptions: len(m.Subscriptions), Deals: len(m.Deals)}
	for _, sub := range m.Subscriptions {
		sub.SubscriptionType = "rfd"
		payload, err := json.Marshal(sub)
		if err != nil {
			return ImportResult{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO subscriptions(guild_id,channel_id,filter,payload) VALUES(?,?,?,?)", sub.GuildID, sub.ChannelID, sub.DealType, payload); err != nil {
			return ImportResult{}, err
		}
	}
	for _, deal := range m.Deals {
		deal.DiscordMessageApplicationIDs = make(map[string]string, len(deal.DiscordMessageIDs))
		for channelID := range deal.DiscordMessageIDs {
			deal.DiscordMessageApplicationIDs[channelID] = options.SourceApplicationID
			result.MessageReceipts++
		}
		payload, err := json.Marshal(deal)
		if err != nil {
			return ImportResult{}, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO deals(id,payload,published_at,updated_at) VALUES(?,?,?,?)", deal.DocumentID, payload, deal.PublishedTimestamp.UnixNano(), deal.LastUpdated.UnixNano()); err != nil {
			return ImportResult{}, err
		}
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return ImportResult{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO settings(key,payload) VALUES(?,?) ON CONFLICT(key) DO NOTHING", applicationKey, options.TargetApplicationID); err != nil {
		return ImportResult{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO settings(key,payload) VALUES(?,?)", migrationKey, payload); err != nil {
		return ImportResult{}, err
	}
	if err = tx.Commit(); err != nil {
		return ImportResult{}, err
	}
	return result, nil
}
