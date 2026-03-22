package storage

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const blockedProxyIPsCollection = "blocked_proxy_ips"

// IsProxyBlocked checks whether a proxy IP has been soft-blocked by Facebook.
func (c *Client) IsProxyBlocked(ctx context.Context, ip string) (bool, error) {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	doc, err := c.client.Collection(blockedProxyIPsCollection).Doc(ip).Get(ctx)
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return false, nil
		}
		return false, err
	}
	return doc.Exists(), nil
}

// BlockProxyIP adds a proxy IP to the blocklist after a Facebook soft block.
func (c *Client) BlockProxyIP(ctx context.Context, ip, city string) error {
	ctx, cancel := ensureDeadline(ctx, DefaultTimeout)
	defer cancel()

	_, err := c.client.Collection(blockedProxyIPsCollection).Doc(ip).Set(ctx, map[string]interface{}{
		"blocked_at": time.Now(),
		"city":       city,
	})
	return err
}
