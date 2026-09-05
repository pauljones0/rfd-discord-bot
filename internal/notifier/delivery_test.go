package notifier

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"golang.org/x/time/rate"
)

func TestSendRetainsSuccessfulReceiptsAlongsideChannelErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, "/good/") {
			_, _ = fmt.Fprint(w, `{"id":"message-good","channel_id":"good"}`)
		} else {
			w.WriteHeader(http.StatusForbidden)
			_, _ = fmt.Fprint(w, `{"message":"private upstream diagnostic"}`)
		}
	}))
	defer server.Close()
	client := New("fixture-token", "app")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}
	receipts, err := client.Send(context.Background(), models.DealInfo{DocumentID: "deal", Title: "A deal"}, []models.Subscription{
		{ChannelID: "good"}, {ChannelID: "bad"}, {ChannelID: "good"},
	})
	if err == nil || !strings.Contains(err.Error(), "channel bad") {
		t.Fatalf("missing channel failure: %v", err)
	}
	if strings.Contains(err.Error(), "private upstream") {
		t.Fatal("response body leaked into error")
	}
	if len(receipts) != 1 || receipts["good"] != "message-good" || calls.Load() != 2 {
		t.Fatalf("successful receipt lost or channel repeated: %v, calls=%d", receipts, calls.Load())
	}
}

func TestSendReusesNonceAfterAmbiguousSuccess(t *testing.T) {
	var attempts atomic.Int32
	seen := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload discordMessagePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if payload.Nonce == "" || len(payload.Nonce) > 25 || !payload.EnforceNonce {
			t.Error("missing bounded retry deduplication")
			w.WriteHeader(400)
			return
		}
		if payload.AllowedMentions == nil || len(payload.AllowedMentions.Parse) != 0 {
			t.Error("delivery allows mentions")
		}
		attempts.Add(1)
		message, exists := seen[payload.Nonce]
		if !exists {
			seen[payload.Nonce] = "one-message"
			// The upstream accepted the message, then its response failed.
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"channel_id":"channel"}`, message)
	}))
	defer server.Close()
	client := New("fixture-token", "app")
	client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
	client.client.Transport = &rewriteTransport{target: server.URL}
	deal := models.DealInfo{DocumentID: "stable-deal", Title: "Old title"}
	receipts, err := client.Send(context.Background(), deal, []models.Subscription{{ChannelID: "channel"}})
	if err != nil || receipts["channel"] != "one-message" || attempts.Load() != 2 {
		t.Fatalf("retry failed: %v %v", receipts, err)
	}
	deal.Title = "Cleaned title"
	receipts, err = client.Send(context.Background(), deal, []models.Subscription{{ChannelID: "channel"}})
	if err != nil || receipts["channel"] != "one-message" || attempts.Load() != 3 {
		t.Fatalf("nonce changed with content: %v %v", receipts, err)
	}
	nonce := messageNonce("app", "channel", deal)
	if nonce == messageNonce("another-app", "channel", deal) || nonce == messageNonce("app", "another-channel", deal) {
		t.Fatal("nonce did not isolate application and destination")
	}
}

func TestSendRejectsMissingAndMismatchedReceipts(t *testing.T) {
	for _, body := range []string{`{}`, `{"id":"wrong","channel_id":"another-channel"}`, `null`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, body) }))
			defer server.Close()
			client := New("fixture-token", "app")
			client.rateLimiter = rate.NewLimiter(rate.Inf, 1)
			client.client.Transport = &rewriteTransport{target: server.URL}
			receipts, err := client.Send(context.Background(), models.DealInfo{DocumentID: "deal"}, []models.Subscription{{ChannelID: "channel"}})
			if err == nil || len(receipts) != 0 {
				t.Fatalf("invalid receipt persisted: %v %v", receipts, err)
			}
		})
	}
}

func TestRetryBackoffAcceptsFractionalSeconds(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"0.25"}}}
	if got := retryBackoff(response, 0); got != 250*time.Millisecond {
		t.Fatalf("fractional retry-after lost: %s", got)
	}
}
