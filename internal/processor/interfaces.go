package processor

import (
	"context"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// DealStore abstracts the storage layer for deal data.
type DealStore interface {
	GetDealsByIDs(ctx context.Context, ids []string) (map[string]*models.DealInfo, error)
	GetRecentDeals(ctx context.Context, d time.Duration) ([]models.DealInfo, error)
	TrimOldDeals(ctx context.Context, maxDeals int) error
	BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error
	GetAllSubscriptions(ctx context.Context) ([]models.Subscription, error)
}

// DealNotifier abstracts the notification layer.
type DealNotifier interface {
	Send(ctx context.Context, deal models.DealInfo, subs []models.Subscription) (map[string]string, error)
	Update(ctx context.Context, deal models.DealInfo) error
}

// DealScraper abstracts the web scraping layer.
type DealScraper interface {
	ScrapeDealList(ctx context.Context) ([]models.DealInfo, error)
	FetchDealDetails(ctx context.Context, deals []*models.DealInfo) models.DealDetailFetchStats
}

// DealValidator abstracts the validation layer.
type DealValidator interface {
	ValidateStruct(s interface{}) error
}
