package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	_ = godotenv.Load()

	port := os.Getenv("REDDIT_SERVICE_PORT")
	if port == "" {
		port = "8082"
	}

	secret := os.Getenv("REDDIT_SERVICE_SECRET")

	mux := http.NewServeMux()

	mux.HandleFunc("GET /reddit", func(w http.ResponseWriter, r *http.Request) {
		if secret != "" {
			authHeader := r.Header.Get("Authorization")
			if authHeader != "Bearer "+secret {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		subreddit := r.URL.Query().Get("subreddit")
		if subreddit == "" {
			http.Error(w, "subreddit query parameter required", http.StatusBadRequest)
			return
		}

		slog.Info("Fetching subreddit", "subreddit", subreddit)

		data, err := fetchReddit(r.Context(), subreddit)
		if err != nil {
			slog.Error("Failed to fetch reddit", "subreddit", subreddit, "error", err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write(data)
	})

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"ready": true})
	})

	slog.Info("Reddit relay service starting", "port", port)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

func fetchReddit(ctx context.Context, subreddit string) ([]byte, error) {
	maxRetries := 3
	backoff := 2 * time.Second
	maxBackoff := 10 * time.Second
	var lastErr error
	var lastStatus int

	for i := 0; i < maxRetries; i++ {
		url := fmt.Sprintf("https://www.reddit.com/r/%s/.json?sort=new&limit=100", subreddit)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "script:canadianhardwareswapbot:v2.0 (by u/pauljones0)")

		client := &http.Client{Timeout: 15 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("reddit request failed: %w", err)
		}

		lastStatus = resp.StatusCode

		if resp.StatusCode == http.StatusOK {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				return nil, fmt.Errorf("failed to read reddit response: %w", err)
			}

			filtered, err := filterAutoModerator(body)
			if err != nil {
				slog.Warn("AutoModerator filtering failed, returning raw data", "error", err)
				return body, nil
			}
			return filtered, nil
		}

		resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden || resp.StatusCode >= 500 {
			slog.Warn("Reddit request failed, retrying",
				"status", resp.StatusCode, "retry", i+1, "backoff", backoff)
			select {
			case <-time.After(backoff):
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		body, _ := io.ReadAll(resp.Body)
		lastErr = fmt.Errorf("reddit returned %d: %s", lastStatus, string(body))
		break
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("max retries exceeded, last status: %d", lastStatus)
}

func filterAutoModerator(data []byte) ([]byte, error) {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	dataObj, ok := raw["data"].(map[string]interface{})
	if !ok {
		return data, nil
	}

	children, ok := dataObj["children"].([]interface{})
	if !ok {
		return data, nil
	}

	var filtered []interface{}
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			filtered = append(filtered, child)
			continue
		}
		childData, ok := childMap["data"].(map[string]interface{})
		if !ok {
			filtered = append(filtered, child)
			continue
		}
		author, _ := childData["author"].(string)
		if author != "AutoModerator" {
			filtered = append(filtered, child)
		}
	}

	dataObj["children"] = filtered
	return json.Marshal(raw)
}
