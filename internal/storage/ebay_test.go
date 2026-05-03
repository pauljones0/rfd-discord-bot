package storage

import (
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
)

func TestEbayCouponObservationDocIDIncludesObservedAt(t *testing.T) {
	base := ebay.CouponObservation{
		Marketplace: "EBAY_CA",
		Seller:      "seller",
		Signature:   "fixed|30",
		ItemID:      "123",
		ObservedAt:  time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
	}
	later := base
	later.ObservedAt = base.ObservedAt.Add(time.Second)

	if ebayCouponObservationDocID(base) == ebayCouponObservationDocID(later) {
		t.Fatalf("observation doc IDs should differ by ObservedAt")
	}
}
