package config

import (
	"fmt"
	"log/slog"
	"os"
)

type Config struct {
	ProjectID             string
	DiscordWebhookURL     string
	Port                  string
	AmazonAffiliateTag    string
	DiscordUpdateInterval string
	AllowedDomains        []string
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

	discordUpdateInterval := os.Getenv("DISCORD_UPDATE_INTERVAL")
	if discordUpdateInterval == "" {
		discordUpdateInterval = "10m"
	}

	return &Config{
		ProjectID:             projectID,
		DiscordWebhookURL:     discordWebhookURL,
		Port:                  port,
		AmazonAffiliateTag:    amazonAffiliateTag,
		DiscordUpdateInterval: discordUpdateInterval,
		AllowedDomains:        []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com"},
	}, nil
}
