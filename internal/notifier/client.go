package notifier

import (
	"golang.org/x/time/rate"
	"net/http"
	"time"
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
