package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const dealsCollection = "deals"

// DefaultTimeout is the default duration for storage operations if the context has no deadline.
const DefaultTimeout = 30 * time.Second

type Client struct {
	pg *pgxpool.Pool
}

func prepareDealForStorage(deal models.DealInfo) models.DealInfo {
	deal.ExpiresAt = deal.ExpiryTime()
	return deal
}

// ensureDeadline returns a context with a deadline if one isn't already set.
// The caller must defer the returned cancel function.
func ensureDeadline(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); !ok {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}

func New(ctx context.Context, _ string) (*Client, error) {
	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	return NewPostgres(ctx, dsn)
}

func NewPostgres(ctx context.Context, databaseURL string) (*Client, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	client := &Client{pg: pool}
	if err := client.ensurePostgresSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) Close() error {
	if c == nil || c.pg == nil {
		return nil
	}
	c.pg.Close()
	return nil
}

func (c *Client) Backend() string {
	return "postgres"
}

func (c *Client) GetDealByID(ctx context.Context, id string) (*models.DealInfo, error) {
	var deal models.DealInfo
	ok, err := c.GetDocument(ctx, dealsCollection, id, &deal)
	if err != nil || !ok {
		return nil, err
	}
	deal.FirestoreID = id
	return &deal, nil
}

func (c *Client) GetDealsByIDs(ctx context.Context, ids []string) (map[string]*models.DealInfo, error) {
	result := make(map[string]*models.DealInfo, len(ids))
	for _, id := range ids {
		var deal models.DealInfo
		ok, err := c.GetDocument(ctx, dealsCollection, id, &deal)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		deal.FirestoreID = id
		result[id] = &deal
	}
	return result, nil
}

func (c *Client) TryCreateDeal(ctx context.Context, deal models.DealInfo) error {
	deal = prepareDealForStorage(deal)
	if err := c.CreateDocument(ctx, dealsCollection, deal.FirestoreID, deal); err != nil {
		if errors.Is(err, errDocumentExists) {
			return models.ErrDealExists
		}
		return err
	}
	return nil
}

func (c *Client) UpdateDeal(ctx context.Context, deal models.DealInfo) error {
	deal = prepareDealForStorage(deal)
	return c.SetDocument(ctx, dealsCollection, deal.FirestoreID, deal)
}

func (c *Client) TrimOldDeals(ctx context.Context, maxDeals int) error {
	rows, err := c.ListDocuments(ctx, dealsCollection)
	if err != nil {
		return err
	}
	if len(rows) <= maxDeals {
		return nil
	}
	sortDocumentsByTime(rows, "lastUpdated", true)
	deleted := 0
	for _, row := range rows[:len(rows)-maxDeals] {
		if err := c.DeleteDocument(ctx, dealsCollection, row.ID); err != nil {
			slog.Warn("TrimOldDeals: failed to delete postgres row", "id", row.ID, "error", err)
			continue
		}
		deleted++
	}
	if deleted > 0 {
		logger.Notice("TrimOldDeals: deleted old rows", "deleted", deleted)
	}
	return nil
}

func (c *Client) BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error {
	var errs []error
	for _, d := range creates {
		d = prepareDealForStorage(d)
		if err := c.CreateDocument(ctx, dealsCollection, d.FirestoreID, d); err != nil {
			errs = append(errs, fmt.Errorf("create %s: %w", d.FirestoreID, err))
		}
	}
	for _, d := range updates {
		d = prepareDealForStorage(d)
		if err := c.SetDocument(ctx, dealsCollection, d.FirestoreID, d); err != nil {
			errs = append(errs, fmt.Errorf("update %s: %w", d.FirestoreID, err))
		}
	}
	return errors.Join(errs...)
}

func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()
	return c.pg.Ping(ctx)
}

func (c *Client) GetRecentDeals(ctx context.Context, d time.Duration) ([]models.DealInfo, error) {
	rows, err := c.ListDocuments(ctx, dealsCollection)
	if err != nil {
		return nil, err
	}
	since := time.Now().Add(-d)
	deals := make([]models.DealInfo, 0, len(rows))
	for _, row := range rows {
		if documentTime(row.Data, "publishedTimestamp").Before(since) {
			continue
		}
		var deal models.DealInfo
		if err := decodeDocument(row.Data, &deal); err != nil {
			slog.Warn("Failed to decode recent deal", "id", row.ID, "error", err)
			continue
		}
		deal.FirestoreID = row.ID
		deals = append(deals, deal)
	}
	sortDealsByPublished(deals)
	return deals, nil
}

func (c *Client) GetGeminiQuotaStatus(ctx context.Context) (*models.GeminiQuotaStatus, error) {
	var quota models.GeminiQuotaStatus
	ok, err := c.GetDocument(ctx, "bot_config", "gemini_quota", &quota)
	if err != nil || !ok {
		return nil, err
	}
	return &quota, nil
}

func (c *Client) UpdateGeminiQuotaStatus(ctx context.Context, quota models.GeminiQuotaStatus) error {
	quota.LastUpdated = time.Now()
	return c.SetDocument(ctx, "bot_config", "gemini_quota", quota)
}

// TokenServiceConfig stores the dynamic URL for an optional local relay service.
type TokenServiceConfig struct {
	URL       string    `firestore:"url"`
	UpdatedAt time.Time `firestore:"updated_at"`
}

func (c *Client) GetTokenServiceURL(ctx context.Context) (string, error) {
	var cfg TokenServiceConfig
	ok, err := c.GetDocument(ctx, "bot_config", "token_service", &cfg)
	if err != nil || !ok {
		return "", err
	}
	return validateDynamicServiceURL(cfg.URL, cfg.UpdatedAt, "Token service URL")
}

func (c *Client) SaveTokenServiceURL(ctx context.Context, url string) error {
	return c.SetDocument(ctx, "bot_config", "token_service", TokenServiceConfig{
		URL:       url,
		UpdatedAt: time.Now(),
	})
}

func (c *Client) GetRedditServiceURL(ctx context.Context) (string, error) {
	var cfg TokenServiceConfig
	ok, err := c.GetDocument(ctx, "bot_config", "reddit_service", &cfg)
	if err != nil || !ok {
		return "", err
	}
	return validateDynamicServiceURL(cfg.URL, cfg.UpdatedAt, "Reddit service URL")
}

func (c *Client) SaveRedditServiceURL(ctx context.Context, url string) error {
	return c.SetDocument(ctx, "bot_config", "reddit_service", TokenServiceConfig{
		URL:       url,
		UpdatedAt: time.Now(),
	})
}

func validateDynamicServiceURL(rawURL string, updatedAt time.Time, label string) (string, error) {
	if strings.Contains(rawURL, "trycloudflare.com") && time.Since(updatedAt) > time.Hour {
		slog.Warn(label+" is stale (>1h old), ignoring ephemeral tunnel",
			"processor", "facebook",
			"url", rawURL,
			"age", time.Since(updatedAt).Round(time.Minute))
		return "", nil
	}
	return rawURL, nil
}

func randomDocumentID() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	var b [20]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	out := make([]byte, len(b))
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out)
}
