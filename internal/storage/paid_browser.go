package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/paidbrowser"
)

const paidBrowserUsageCollection = "paid_browser_usage"

func paidBrowserUsageDocID(site, day string) string {
	site = strings.ToLower(strings.TrimSpace(site))
	day = strings.TrimSpace(day)
	return fmt.Sprintf("%s_%s", site, day)
}

func (c *Client) GetPaidBrowserUsage(ctx context.Context, site, day string) (*paidbrowser.Usage, error) {
	var usage paidbrowser.Usage
	ok, err := c.GetDocument(ctx, paidBrowserUsageCollection, paidBrowserUsageDocID(site, day), &usage)
	if err != nil || !ok {
		return nil, err
	}
	return &usage, nil
}

func (c *Client) SavePaidBrowserUsage(ctx context.Context, usage paidbrowser.Usage) error {
	return c.SetDocument(ctx, paidBrowserUsageCollection, paidBrowserUsageDocID(usage.Site, usage.Day), usage)
}
