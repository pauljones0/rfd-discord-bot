package config

import (
	"os"
	"testing"
)

func TestLoad(t *testing.T) {
	// Set test environment variables
	os.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	os.Setenv("DISCORD_WEBHOOK_URL", "https://test.webhook")
	os.Setenv("PORT", "9090")
	os.Setenv("AMAZON_AFFILIATE_TAG", "test-tag-20")

	cfg := Load()

	if cfg.ProjectID != "test-project" {
		t.Errorf("Expected test-project, got %s", cfg.ProjectID)
	}
	if cfg.DiscordWebhookURL != "https://test.webhook" {
		t.Errorf("Expected https://test.webhook, got %s", cfg.DiscordWebhookURL)
	}
	if cfg.Port != "9090" {
		t.Errorf("Expected 9090, got %s", cfg.Port)
	}
	if cfg.AmazonAffiliateTag != "test-tag-20" {
		t.Errorf("Expected test-tag-20, got %s", cfg.AmazonAffiliateTag)
	}
	if cfg.DiscordUpdateInterval != "10m" {
		t.Errorf("Expected default 10m, got %s", cfg.DiscordUpdateInterval)
	}
}
