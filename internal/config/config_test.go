package config

import (
	"testing"
	"time"
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
	if cfg.DiscordUpdateInterval != 10*time.Minute {
		t.Errorf("Expected default 10m, got %s", cfg.DiscordUpdateInterval)
	}
	if cfg.MaxStoredDeals != 500 {
		t.Errorf("Expected default MaxStoredDeals 500, got %d", cfg.MaxStoredDeals)
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

func TestLoad_CustomUpdateInterval(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("DISCORD_UPDATE_INTERVAL", "5m")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.DiscordUpdateInterval != 5*time.Minute {
		t.Errorf("Expected 5m, got %s", cfg.DiscordUpdateInterval)
	}
}

func TestLoad_InvalidUpdateInterval(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("DISCORD_UPDATE_INTERVAL", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Error("Load() should return error for invalid DISCORD_UPDATE_INTERVAL")
	}
}

func TestLoad_DefaultAffiliateTag(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("AMAZON_AFFILIATE_TAG", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.AmazonAffiliateTag != "beauahrens0d-20" {
		t.Errorf("Expected default affiliate tag 'beauahrens0d-20', got %s", cfg.AmazonAffiliateTag)
	}
}

func TestLoad_DefaultBestBuyPrefix(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("BESTBUY_AFFILIATE_PREFIX", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	expected := "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
	if cfg.BestBuyAffiliatePrefix != expected {
		t.Errorf("Expected default BestBuyAffiliatePrefix %q, got %q", expected, cfg.BestBuyAffiliatePrefix)
	}
}

func TestLoad_CustomBestBuyPrefix(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("BESTBUY_AFFILIATE_PREFIX", "https://custom.prefix/c/1/2/3?u=")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.BestBuyAffiliatePrefix != "https://custom.prefix/c/1/2/3?u=" {
		t.Errorf("Expected custom prefix, got %q", cfg.BestBuyAffiliatePrefix)
	}
}
