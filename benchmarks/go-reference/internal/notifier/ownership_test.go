package notifier

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"golang.org/x/time/rate"
)

func TestUpdatePreservesForeignReceiptsWithoutEditingThem(t *testing.T) {
	var updates atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/v10/channels/200/messages/400" {
			t.Errorf("unexpected Discord request: %s %s", r.Method, r.URL.Path)
		}
		updates.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client := New("fixture-token", "20")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}
	deal := models.DealInfo{
		Title: "Retained deal", PostURL: "https://example.com/deal",
		DiscordMessageIDs:            map[string]string{"100": "300", "200": "400"},
		DiscordMessageApplicationIDs: map[string]string{"100": "10", "200": "20"},
	}
	if err := client.Update(context.Background(), deal); err != nil {
		t.Fatal(err)
	}
	if updates.Load() != 1 || deal.DiscordMessageIDs["100"] != "300" || deal.DiscordMessageApplicationIDs["100"] != "10" {
		t.Fatal("foreign receipt was edited or lost")
	}
	// A client with unknown identity cannot claim an explicitly owned message.
	client.applicationID = ""
	if err := client.Update(context.Background(), deal); err != nil || updates.Load() != 1 {
		t.Fatalf("unidentified client edited an owned message: %v", err)
	}
}
