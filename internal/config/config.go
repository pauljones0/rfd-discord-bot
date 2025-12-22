package config

import (
	"log"
	"os"
)

type Config struct {
	ProjectID             string
	DiscordWebhookURL     string
	Port                  string
	AmazonAffiliateTag    string
	DiscordUpdateInterval string
}

func Load() *Config {
	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		log.Println("Warning: GOOGLE_CLOUD_PROJECT environment variable not set. Attempting to use a default project ID for local testing (this might fail).")
	}

	discordWebhookURL := os.Getenv("DISCORD_WEBHOOK_URL")
	if discordWebhookURL == "" {
		log.Println("Warning: DISCORD_WEBHOOK_URL environment variable not set. Discord notifications will be skipped.")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
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
	}
}
