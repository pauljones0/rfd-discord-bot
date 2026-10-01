// Package validator checks the small input contract of an RFD listing.
package validator

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

type Validator struct{}

func New() *Validator { return &Validator{} }

func (v *Validator) ValidateStruct(value any) error {
	var deal models.DealInfo
	switch d := value.(type) {
	case models.DealInfo:
		deal = d
	case *models.DealInfo:
		if d == nil {
			return fmt.Errorf("deal is nil")
		}
		deal = *d
	default:
		return fmt.Errorf("expected an RFD deal")
	}
	if strings.TrimSpace(deal.Title) == "" || deal.PublishedTimestamp.IsZero() {
		return fmt.Errorf("deal requires a title and publication time")
	}
	for name, raw := range map[string]string{
		"post URL": deal.PostURL, "product URL": deal.ActualDealURL, "image URL": deal.ThreadImageURL,
	} {
		if raw == "" && name != "post URL" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return fmt.Errorf("invalid %s", name)
		}
	}
	return nil
}
