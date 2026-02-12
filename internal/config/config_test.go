package config

import (
	"testing"
)

func TestLoad(t *testing.T) {
	// Set test environment variables (auto-cleaned up after test)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("DISCORD_WEBHOOK_URL", "https://test.webhook")
	t.Setenv("PORT", "9090")
	t.Setenv("AMAZON_AFFILIATE_TAG", "test-tag-20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

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

func TestLoad_MissingProjectID(t *testing.T) {
	// Do NOT set GOOGLE_CLOUD_PROJECT
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	_, err := Load()
	if err == nil {
		t.Error("Load() should return an error when GOOGLE_CLOUD_PROJECT is not set")
	}
}
