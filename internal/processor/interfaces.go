package processor

import (
	"context"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

// DealStore abstracts the storage layer for deal data.
type DealStore interface {
	GetDealByID(ctx context.Context, id string) (*models.DealInfo, error)
	GetDealsByIDs(ctx context.Context, ids []string) (map[string]*models.DealInfo, error)
	TryCreateDeal(ctx context.Context, deal models.DealInfo) error
	UpdateDeal(ctx context.Context, deal models.DealInfo) error
	TrimOldDeals(ctx context.Context, maxDeals int) error
	BatchWrite(ctx context.Context, creates []models.DealInfo, updates []models.DealInfo) error
	Ping(ctx context.Context) error
}

// DealNotifier abstracts the notification layer.
type DealNotifier interface {
	Send(ctx context.Context, deal models.DealInfo) (string, error)
	Update(ctx context.Context, messageID string, deal models.DealInfo) error
}

// DealScraper abstracts the web scraping layer.
type DealScraper interface {
	ScrapeDealList(ctx context.Context) ([]models.DealInfo, error)
	FetchDealDetails(ctx context.Context, deals []*models.DealInfo)
}

// DealValidator abstracts the validation layer.
type DealValidator interface {
	ValidateStruct(s interface{}) error
}
