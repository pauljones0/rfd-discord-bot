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
}

// DealNotifier abstracts the notification layer.
type DealNotifier interface {
	Send(ctx context.Context, deal models.DealInfo) (string, error)
	Update(ctx context.Context, messageID string, deal models.DealInfo) error
}
