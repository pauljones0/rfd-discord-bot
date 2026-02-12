package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	ProjectID              string
	DiscordWebhookURL      string
	Port                   string
	AmazonAffiliateTag     string
	BestBuyAffiliatePrefix string
	DiscordUpdateInterval  time.Duration
	MaxStoredDeals         int
	AllowedDomains         []string
}

func Load() (*Config, error) {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT environment variable is required but not set")
	}

	discordWebhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhookURL == "" {
		slog.Warn("DISCORD_WEBHOOK_URL not set, Discord notifications will be skipped")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		slog.Info("Defaulting to port", "port", port)
	}

	amazonAffiliateTag := os.Getenv("AMAZON_AFFILIATE_TAG")
	if amazonAffiliateTag == "" {
		// Default tag from previous hardcoded value
		amazonAffiliateTag = "beauahrens0d-20"
	}

	bestBuyAffiliatePrefix := os.Getenv("BESTBUY_AFFILIATE_PREFIX")
	if bestBuyAffiliatePrefix == "" {
		bestBuyAffiliatePrefix = "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
	}

	discordUpdateIntervalStr := os.Getenv("DISCORD_UPDATE_INTERVAL")
	if discordUpdateIntervalStr == "" {
		discordUpdateIntervalStr = "10m"
	}
	discordUpdateInterval, err := time.ParseDuration(discordUpdateIntervalStr)
	if err != nil {
		return nil, fmt.Errorf("invalid DISCORD_UPDATE_INTERVAL %q: %w", discordUpdateIntervalStr, err)
	}

	maxStoredDeals := 500
	if v := os.Getenv("MAX_STORED_DEALS"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_STORED_DEALS %q: %w", v, err)
		}
		maxStoredDeals = parsed
	}

	return &Config{
		ProjectID:              projectID,
		DiscordWebhookURL:      discordWebhookURL,
		Port:                   port,
		AmazonAffiliateTag:     amazonAffiliateTag,
		BestBuyAffiliatePrefix: bestBuyAffiliatePrefix,
		DiscordUpdateInterval:  discordUpdateInterval,
		MaxStoredDeals:         maxStoredDeals,
		AllowedDomains:         []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com"},
	}, nil
}
