package notifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
	"golang.org/x/time/rate"
)

const (
	maxRetries     = 3
	discordAPIBase = "https://discord.com/api/v10"
)

type Client struct {
	botToken      string
	applicationID string
	client        *http.Client
	rateLimiter   *rate.Limiter
}

func New(token string, applicationID ...string) *Client {
	c := &Client{botToken: token, client: &http.Client{Timeout: 10 * time.Second}, rateLimiter: rate.NewLimiter(rate.Every(1200*time.Millisecond), 1)}
	if len(applicationID) > 0 {
		c.applicationID = applicationID[0]
	}
	return c
}

// Send returns every successful channel receipt, even when other channels fail.
// Callers must save receipts before retrying the failed destinations.
func (c *Client) Send(ctx context.Context, deal models.DealInfo, subs []models.Subscription) (map[string]string, error) {
	results := make(map[string]string)
	if c.botToken == "" {
		return results, nil
	}
	var errs []error
	seen := make(map[string]bool)
	for _, sub := range subs {
		if seen[sub.ChannelID] {
			continue
		}
		seen[sub.ChannelID] = true
		payload := createDiscordPayload(deal)
		payload.Nonce = messageNonce(c.applicationID, sub.ChannelID, deal)
		payload.EnforceNonce = true
		target := fmt.Sprintf("%s/channels/%s/messages", discordAPIBase, sub.ChannelID)
		body, err := c.doRequest(ctx, http.MethodPost, target, payload)
		if err == nil {
			var message discordMessageResponse
			if json.Unmarshal(body, &message) != nil || message.ID == "" || (message.ChannelID != "" && message.ChannelID != sub.ChannelID) {
				err = errors.New("Discord returned an invalid message receipt")
			} else {
				results[sub.ChannelID] = message.ID
			}
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", sub.ChannelID, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return results, errors.Join(errs...)
}

// Discord enforces nonce uniqueness for a limited recent window. Durable saved
// receipts handle later polls; this stable 24-character nonce handles ambiguous
// request retries without changing when title or engagement metadata changes.
func messageNonce(applicationID, channelID string, deal models.DealInfo) string {
	identity := deal.DocumentID
	if identity == "" {
		identity = deal.PostURL
	}
	if identity == "" {
		identity = deal.Title
	}
	sum := sha256.Sum256([]byte(applicationID + "\x00" + channelID + "\x00" + identity))
	return hex.EncodeToString(sum[:12])
}

// Update edits messages owned by this application and preserves foreign receipts.
func (c *Client) Update(ctx context.Context, deal models.DealInfo) error {
	if c.botToken == "" {
		return nil
	}
	payload := createDiscordPayload(deal)
	var errs []error
	for channelID, messageID := range deal.DiscordMessageIDs {
		if owner := deal.DiscordMessageApplicationIDs[channelID]; owner != "" && owner != c.applicationID {
			continue
		}
		target := fmt.Sprintf("%s/channels/%s/messages/%s", discordAPIBase, channelID, messageID)
		if _, err := c.doRequest(ctx, http.MethodPatch, target, payload); err != nil {
			errs = append(errs, fmt.Errorf("channel %s: %w", channelID, err))
		}
		if ctx.Err() != nil {
			break
		}
	}
	return errors.Join(errs...)
}

// doRequest retries only transport failures, 429, and server errors. Responses
// are bounded and errors omit response bodies and credentials.
func (c *Client) doRequest(ctx context.Context, method, targetURL string, payload discordMessagePayload) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := c.rateLimiter.Wait(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, targetURL, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bot "+c.botToken)
		resp, err := c.client.Do(req)
		delay := time.Duration(1<<attempt) * time.Second
		if err != nil {
			lastErr = errors.New("Discord transport request failed")
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
			resp.Body.Close()
			if readErr != nil || len(body) > 1<<20 {
				lastErr = errors.New("could not read Discord response")
			} else if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return body, nil
			} else {
				lastErr = fmt.Errorf("Discord HTTP status %d", resp.StatusCode)
				delay = retryBackoff(resp, attempt)
				if resp.StatusCode == http.StatusTooManyRequests {
					var limit struct {
						RetryAfter float64 `json:"retry_after"`
					}
					if json.Unmarshal(body, &limit) == nil && limit.RetryAfter > 0 {
						delay = max(delay, time.Duration(limit.RetryAfter*float64(time.Second)))
					}
				}
				if delay == 0 {
					return nil, lastErr
				}
			}
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt == maxRetries {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("Discord request failed after %d attempts: %w", maxRetries+1, lastErr)
}

func retryBackoff(resp *http.Response, attempt int) time.Duration {
	if resp.StatusCode == http.StatusTooManyRequests {
		if seconds, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && seconds > 0 {
			return time.Duration(seconds * float64(time.Second))
		}
		if at, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil && time.Until(at) > 0 {
			return time.Until(at)
		}
		return time.Duration(1<<attempt) * time.Second
	}
	if resp.StatusCode >= 500 {
		return time.Duration(1<<attempt) * time.Second
	}
	return 0
}
