package reddit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// RedditServiceStore defines the Firestore operations needed to resolve the relay URL.
type RedditServiceStore interface {
	GetRedditServiceURL(ctx context.Context) (string, error)
}

// Client fetches Reddit posts through the local relay service.
type Client struct {
	staticURL  string
	secret     string
	store      RedditServiceStore
	httpClient *http.Client
}

// NewClient creates a new Reddit relay client.
func NewClient(staticURL, secret string, store RedditServiceStore) *Client {
	return &Client{
		staticURL: staticURL,
		secret:    secret,
		store:     store,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// FetchPosts fetches posts from the given subreddit via the relay service.
func (c *Client) FetchPosts(ctx context.Context, subreddit string) ([]Post, error) {
	relayURL := c.resolveURL(ctx)
	if relayURL == "" {
		return nil, fmt.Errorf("reddit relay service URL not configured")
	}

	reqURL := fmt.Sprintf("%s/reddit?subreddit=%s", relayURL, subreddit)
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	if c.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.secret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reddit relay request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reddit relay returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read relay response: %w", err)
	}

	var feed Feed
	if err := json.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("failed to decode reddit JSON: %w", err)
	}

	var posts []Post
	for _, child := range feed.Data.Children {
		posts = append(posts, child.Data)
	}
	return posts, nil
}

// resolveURL tries Firestore first, falls back to static env var.
func (c *Client) resolveURL(ctx context.Context) string {
	if c.store != nil {
		if dynamicURL, err := c.store.GetRedditServiceURL(ctx); err != nil {
			slog.Warn("Failed to fetch dynamic reddit service URL, using static config",
				"processor", "reddit", "error", err, "static_url", c.staticURL)
		} else if dynamicURL != "" {
			slog.Info("Using dynamic reddit service URL from Firestore",
				"processor", "reddit", "url", dynamicURL)
			return dynamicURL
		}
	}
	return c.staticURL
}
