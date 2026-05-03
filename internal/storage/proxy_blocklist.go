package storage

import (
	"context"
	"time"
)

const blockedProxyIPsCollection = "blocked_proxy_ips"

// IsProxyBlocked checks whether a proxy IP has been soft-blocked by Facebook.
func (c *Client) IsProxyBlocked(ctx context.Context, ip string) (bool, error) {
	_, ok, err := c.GetRawDocument(ctx, blockedProxyIPsCollection, ip)
	return ok, err
}

// BlockProxyIP adds a proxy IP to the blocklist after a Facebook soft block.
func (c *Client) BlockProxyIP(ctx context.Context, ip, city string) error {
	return c.SetRawDocument(ctx, blockedProxyIPsCollection, ip, map[string]any{
		"blocked_at": time.Now(),
		"city":       city,
	})
}
