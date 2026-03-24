package facebook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// CarfaxTokenClient communicates with the remote token service to obtain
// reCAPTCHA v3 tokens from a real headed Chrome browser. The token service
// runs on a GCE VM (or local machine) — see cmd/token-service/main.go.
type CarfaxTokenClient struct {
	serviceURL string // e.g., "http://10.128.0.2:8081"
	secret     string // Bearer token for Authorization header
	httpClient *http.Client
}

// tokenResponse is the JSON response from GET /token.
type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
	Error     string `json:"error"`
}

// NewCarfaxTokenClient creates a client that fetches reCAPTCHA tokens from the
// remote token service.
func NewCarfaxTokenClient(serviceURL, secret string) *CarfaxTokenClient {
	return &CarfaxTokenClient{
		serviceURL: serviceURL,
		secret:     secret,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// GetToken obtains a fresh reCAPTCHA v3 token from the token service.
// Retries once on transient errors (connection refused, timeout).
func (c *CarfaxTokenClient) GetToken(ctx context.Context) (string, error) {
	start := time.Now()
	token, err := c.fetchToken(ctx)
	if err != nil {
		slog.Warn("Token fetch failed, retrying once",
			"processor", "facebook", "component", "carfax_http",
			"error", err, "duration_ms", time.Since(start).Milliseconds())
		time.Sleep(500 * time.Millisecond)
		token, err = c.fetchToken(ctx)
		if err != nil {
			slog.Error("Token fetch failed after retry",
				"processor", "facebook", "component", "carfax_http",
				"error", err, "duration_ms", time.Since(start).Milliseconds(),
				"service_url", c.serviceURL)
			return "", fmt.Errorf("token service unavailable after retry: %w", err)
		}
	}
	slog.Info("Token fetched from service",
		"processor", "facebook", "component", "carfax_http",
		"token_length", len(token), "duration_ms", time.Since(start).Milliseconds())
	return token, nil
}

func (c *CarfaxTokenClient) fetchToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.serviceURL+"/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("token service request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token service returned %d: %s (url=%s)", resp.StatusCode, string(body), c.serviceURL+"/token")
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}

	if tr.Error != "" {
		return "", fmt.Errorf("token service error: %s", tr.Error)
	}

	if len(tr.Token) < 100 {
		return "", fmt.Errorf("token too short (%d chars)", len(tr.Token))
	}

	return tr.Token, nil
}
