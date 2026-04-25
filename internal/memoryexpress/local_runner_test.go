package memoryexpress

import (
	"reflect"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

func TestNormalizeStoreCodes(t *testing.T) {
	t.Parallel()

	got := normalizeStoreCodes([]string{"SKST", "CalNE", "invalid", "", "SKST", "CalNE"})
	want := []string{"CalNE", "SKST"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeStoreCodes() = %v, want %v", got, want)
	}
}

func TestSubscribedStoreCodes(t *testing.T) {
	t.Parallel()

	subs := []models.Subscription{
		{SubscriptionType: "memoryexpress", StoreCode: "SKST"},
		{SubscriptionType: "memoryexpress", StoreCode: "CalNE"},
		{SubscriptionType: "memoryexpress", StoreCode: "SKST"},
		{SubscriptionType: "memoryexpress", StoreCode: "not-real"},
		{SubscriptionType: "rfd", StoreCode: "BBBC"},
	}

	got := subscribedStoreCodes(subs)
	want := []string{"CalNE", "SKST"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("subscribedStoreCodes() = %v, want %v", got, want)
	}
}
