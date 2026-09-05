package notifier

import (
	"golang.org/x/time/rate"
	"net/http"
	"time"
)

type Client struct {
	botToken    string
	client      *http.Client
	rateLimiter *rate.Limiter
}

func New(token string) *Client {
	return &Client{botToken: token, client: &http.Client{Timeout: 10 * time.Second}, rateLimiter: rate.NewLimiter(rate.Every(1200*time.Millisecond), 1)}
}
