package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	// Set test environment variables (auto-cleaned up after test)
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("PORT", "9090")
	t.Setenv("AMAZON_AFFILIATE_TAG", "test-tag-20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if cfg.ProjectID != "test-project" {
		t.Errorf("Expected test-project, got %s", cfg.ProjectID)
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
	if cfg.RFDPollInterval != 3*time.Minute {
		t.Errorf("Expected default RFD poll interval 3m, got %s", cfg.RFDPollInterval)
	}
	if cfg.EbayPollInterval != 30*time.Minute {
		t.Errorf("Expected default eBay poll interval 30m, got %s", cfg.EbayPollInterval)
	}
	if cfg.MemoryExpressPollInterval != 30*time.Minute {
		t.Errorf("Expected default Memory Express poll interval 30m, got %s", cfg.MemoryExpressPollInterval)
	}
	if cfg.BestBuyPollInterval != 30*time.Minute {
		t.Errorf("Expected default Best Buy poll interval 30m, got %s", cfg.BestBuyPollInterval)
	}
	if cfg.BestBuyComputePollInterval != time.Hour {
		t.Errorf("Expected default Best Buy compute poll interval 1h, got %s", cfg.BestBuyComputePollInterval)
	}
	if cfg.BestBuyComputeEnabled {
		t.Errorf("Expected Best Buy compute scheduler to be disabled by default")
	}
	if cfg.BestBuyComputeSoldVerifyEnabled {
		t.Errorf("Expected Best Buy compute eBay sold verification to be disabled by default")
	}
	if !reflect.DeepEqual(cfg.BestBuyComputeSoldBackends, []string{"http", "external-stealth", "camoufox", "ai-crawler"}) {
		t.Errorf("Expected default Best Buy compute sold backends, got %v", cfg.BestBuyComputeSoldBackends)
	}
	if cfg.BestBuyComputeSoldCacheTTL != 24*time.Hour {
		t.Errorf("Expected default Best Buy compute sold cache TTL 24h, got %s", cfg.BestBuyComputeSoldCacheTTL)
	}
	if cfg.BestBuyComputeSoldQueryDelay != 3*time.Second {
		t.Errorf("Expected default Best Buy compute sold query delay 3s, got %s", cfg.BestBuyComputeSoldQueryDelay)
	}
	if !cfg.BestBuySoldCompsEnabled {
		t.Errorf("Expected Best Buy seller sold comps to be enabled by default")
	}
	if !reflect.DeepEqual(cfg.BestBuySoldCompBackends, []string{"http", "external-stealth", "camoufox", "ai-crawler"}) {
		t.Errorf("Expected default Best Buy sold comp backends, got %v", cfg.BestBuySoldCompBackends)
	}
	if cfg.BestBuySoldCompCacheTTL != 24*time.Hour || cfg.BestBuySoldCompQueryDelay != 3*time.Second || cfg.BestBuySoldCompMaxPerRun != 10 {
		t.Errorf("Expected default Best Buy sold comp TTL/delay/cap 24h/3s/10, got %s/%s/%d", cfg.BestBuySoldCompCacheTTL, cfg.BestBuySoldCompQueryDelay, cfg.BestBuySoldCompMaxPerRun)
	}
	if cfg.BestBuySoldCompPaidEnabled || cfg.BestBuySoldCompPaidMaxPerRun != 0 || cfg.BestBuySoldCompPaidMaxPerDay != 0 {
		t.Errorf("Expected Best Buy sold comp paid browser disabled, got enabled=%v caps=%d/%d", cfg.BestBuySoldCompPaidEnabled, cfg.BestBuySoldCompPaidMaxPerRun, cfg.BestBuySoldCompPaidMaxPerDay)
	}
	if cfg.BestBuyComputeSoldPaidEnabled {
		t.Errorf("Expected Best Buy compute sold paid browser to be disabled by default")
	}
	if cfg.BestBuyComputeSoldPaidMaxPerRun != 0 || cfg.BestBuyComputeSoldPaidMaxPerDay != 0 {
		t.Errorf("Expected Best Buy compute sold paid caps disabled, got %d/%d", cfg.BestBuyComputeSoldPaidMaxPerRun, cfg.BestBuyComputeSoldPaidMaxPerDay)
	}
	if cfg.EbayCouponDiscoveryInterval != 6*time.Hour {
		t.Errorf("Expected default eBay coupon discovery interval 6h, got %s", cfg.EbayCouponDiscoveryInterval)
	}
	if cfg.EbayPaidBrowserMaxPerRun != 1 || cfg.EbayPaidBrowserMaxPerDay != 6 {
		t.Errorf("Expected eBay paid browser caps 1/run and 6/day, got %d/%d", cfg.EbayPaidBrowserMaxPerRun, cfg.EbayPaidBrowserMaxPerDay)
	}
	if cfg.MemoryExpressPaidMaxPerRun != 0 || cfg.MemoryExpressPaidMaxPerDay != 0 {
		t.Errorf("Expected Memory Express paid browser caps disabled, got %d/%d", cfg.MemoryExpressPaidMaxPerRun, cfg.MemoryExpressPaidMaxPerDay)
	}
	if cfg.LocalSchedulerEnabled {
		t.Errorf("Expected local scheduler to be disabled by default")
	}
	if cfg.MaxStoredDeals != 500 {
		t.Errorf("Expected default MaxStoredDeals 500, got %d", cfg.MaxStoredDeals)
	}
	if len(cfg.GeminiFallbackModels) == 0 || cfg.GeminiFallbackModels[0] != "gemini-2.5-flash-lite" {
		t.Errorf("Expected first fallback model gemini-2.5-flash-lite, got %v", cfg.GeminiFallbackModels)
	}
}

func TestLoad_ProjectIDOptionalForLocalPostgres(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error without GOOGLE_CLOUD_PROJECT: %v", err)
	}
	if cfg.ProjectID != "" {
		t.Fatalf("ProjectID = %q, want empty local default", cfg.ProjectID)
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

func TestLoad_BackendFallbackConfig(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("EBAY_COUPON_BACKENDS", "http, chromedp-cloudrun")
	t.Setenv("MEMEXPRESS_BACKENDS", "http,chromedp-persistent, paid-trial")
	t.Setenv("BESTBUY_BACKENDS", "http,playwright")
	t.Setenv("BESTBUY_COMPUTE_SOLD_BACKENDS", "http, camoufox, paid-trial")
	t.Setenv("BESTBUY_SOLD_COMP_BACKENDS", "http, external-stealth")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if !reflect.DeepEqual(cfg.EbayCouponBackends, []string{"http", "chromedp-cloudrun"}) {
		t.Fatalf("EbayCouponBackends = %v", cfg.EbayCouponBackends)
	}
	if !reflect.DeepEqual(cfg.MemoryExpressBackends, []string{"http", "chromedp-persistent", "paid-trial"}) {
		t.Fatalf("MemoryExpressBackends = %v", cfg.MemoryExpressBackends)
	}
	if !reflect.DeepEqual(cfg.BestBuyBackends, []string{"http", "playwright"}) {
		t.Fatalf("BestBuyBackends = %v", cfg.BestBuyBackends)
	}
	if !reflect.DeepEqual(cfg.BestBuyComputeSoldBackends, []string{"http", "camoufox", "paid-trial"}) {
		t.Fatalf("BestBuyComputeSoldBackends = %v", cfg.BestBuyComputeSoldBackends)
	}
	if !reflect.DeepEqual(cfg.BestBuySoldCompBackends, []string{"http", "external-stealth"}) {
		t.Fatalf("BestBuySoldCompBackends = %v", cfg.BestBuySoldCompBackends)
	}
}

func TestLoad_CustomSchedulerConfig(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("LOCAL_SCHEDULER_ENABLED", "true")
	t.Setenv("RFD_POLL_INTERVAL", "4m")
	t.Setenv("EBAY_POLL_INTERVAL", "45m")
	t.Setenv("MEMEXPRESS_POLL_INTERVAL", "35m")
	t.Setenv("BESTBUY_POLL_INTERVAL", "20m")
	t.Setenv("BESTBUY_COMPUTE_ENABLED", "true")
	t.Setenv("BESTBUY_COMPUTE_POLL_INTERVAL", "2h")
	t.Setenv("BESTBUY_COMPUTE_ALERT_FIRST_SEEN", "true")
	t.Setenv("BESTBUY_COMPUTE_SOLD_VERIFY_ENABLED", "true")
	t.Setenv("BESTBUY_COMPUTE_SOLD_CACHE_TTL", "12h")
	t.Setenv("BESTBUY_COMPUTE_SOLD_QUERY_DELAY", "4s")
	t.Setenv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_ENABLED", "true")
	t.Setenv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_MAX_CALLS_PER_RUN", "1")
	t.Setenv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_MAX_CALLS_PER_DAY", "2")
	t.Setenv("BESTBUY_SOLD_COMPS_ENABLED", "true")
	t.Setenv("BESTBUY_SOLD_COMP_CACHE_TTL", "6h")
	t.Setenv("BESTBUY_SOLD_COMP_QUERY_DELAY", "5s")
	t.Setenv("BESTBUY_SOLD_COMP_MAX_PER_RUN", "3")
	t.Setenv("BESTBUY_SOLD_COMP_PAID_BROWSER_ENABLED", "true")
	t.Setenv("BESTBUY_SOLD_COMP_PAID_BROWSER_MAX_CALLS_PER_RUN", "1")
	t.Setenv("BESTBUY_SOLD_COMP_PAID_BROWSER_MAX_CALLS_PER_DAY", "2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}

	if !cfg.LocalSchedulerEnabled {
		t.Fatal("LocalSchedulerEnabled = false, want true")
	}
	if cfg.RFDPollInterval != 4*time.Minute {
		t.Fatalf("RFDPollInterval = %s, want 4m", cfg.RFDPollInterval)
	}
	if cfg.EbayPollInterval != 45*time.Minute {
		t.Fatalf("EbayPollInterval = %s, want 45m", cfg.EbayPollInterval)
	}
	if cfg.MemoryExpressPollInterval != 35*time.Minute {
		t.Fatalf("MemoryExpressPollInterval = %s, want 35m", cfg.MemoryExpressPollInterval)
	}
	if cfg.BestBuyPollInterval != 20*time.Minute {
		t.Fatalf("BestBuyPollInterval = %s, want 20m", cfg.BestBuyPollInterval)
	}
	if !cfg.BestBuyComputeEnabled {
		t.Fatal("BestBuyComputeEnabled = false, want true")
	}
	if cfg.BestBuyComputePollInterval != 2*time.Hour {
		t.Fatalf("BestBuyComputePollInterval = %s, want 2h", cfg.BestBuyComputePollInterval)
	}
	if !cfg.BestBuyComputeAlertFirstSeen {
		t.Fatal("BestBuyComputeAlertFirstSeen = false, want true")
	}
	if !cfg.BestBuyComputeSoldVerifyEnabled {
		t.Fatal("BestBuyComputeSoldVerifyEnabled = false, want true")
	}
	if cfg.BestBuyComputeSoldCacheTTL != 12*time.Hour {
		t.Fatalf("BestBuyComputeSoldCacheTTL = %s, want 12h", cfg.BestBuyComputeSoldCacheTTL)
	}
	if cfg.BestBuyComputeSoldQueryDelay != 4*time.Second {
		t.Fatalf("BestBuyComputeSoldQueryDelay = %s, want 4s", cfg.BestBuyComputeSoldQueryDelay)
	}
	if !cfg.BestBuySoldCompsEnabled {
		t.Fatal("BestBuySoldCompsEnabled = false, want true")
	}
	if cfg.BestBuySoldCompCacheTTL != 6*time.Hour || cfg.BestBuySoldCompQueryDelay != 5*time.Second || cfg.BestBuySoldCompMaxPerRun != 3 {
		t.Fatalf("BestBuySoldComp TTL/delay/cap = %s/%s/%d, want 6h/5s/3", cfg.BestBuySoldCompCacheTTL, cfg.BestBuySoldCompQueryDelay, cfg.BestBuySoldCompMaxPerRun)
	}
	if !cfg.BestBuySoldCompPaidEnabled || cfg.BestBuySoldCompPaidMaxPerRun != 1 || cfg.BestBuySoldCompPaidMaxPerDay != 2 {
		t.Fatalf("BestBuySoldCompPaid = %v caps=%d/%d, want true 1/2", cfg.BestBuySoldCompPaidEnabled, cfg.BestBuySoldCompPaidMaxPerRun, cfg.BestBuySoldCompPaidMaxPerDay)
	}
	if !cfg.BestBuyComputeSoldPaidEnabled {
		t.Fatal("BestBuyComputeSoldPaidEnabled = false, want true")
	}
	if cfg.BestBuyComputeSoldPaidMaxPerRun != 1 || cfg.BestBuyComputeSoldPaidMaxPerDay != 2 {
		t.Fatalf("BestBuyComputeSoldPaid caps = %d/%d, want 1/2", cfg.BestBuyComputeSoldPaidMaxPerRun, cfg.BestBuyComputeSoldPaidMaxPerDay)
	}
}

func TestLoad_InvalidSchedulerInterval(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "test-project")
	t.Setenv("RFD_POLL_INTERVAL", "not-a-duration")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() should return error for invalid RFD_POLL_INTERVAL")
	}
}

func TestLoadLooseDotEnv_IgnoresMultilineJSONBlock(t *testing.T) {
	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("DISCORD_BOT_TOKEN", "")
	t.Setenv("MULTILINE_SECRET", "")

	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "GOOGLE_CLOUD_PROJECT=test-project\n" +
		"MULTILINE_SECRET={\n" +
		"  \"type\": \"service_account\",\n" +
		"  \"project_id\": \"test-project\"\n" +
		"}\n" +
		"DISCORD_BOT_TOKEN=test-token\n"

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile() returned unexpected error: %v", err)
	}

	if err := loadLooseDotEnv(path); err != nil {
		t.Fatalf("loadLooseDotEnv() returned unexpected error: %v", err)
	}

	if got := os.Getenv("GOOGLE_CLOUD_PROJECT"); got != "test-project" {
		t.Fatalf("Expected GOOGLE_CLOUD_PROJECT to be loaded, got %q", got)
	}
	if got := os.Getenv("DISCORD_BOT_TOKEN"); got != "test-token" {
		t.Fatalf("Expected DISCORD_BOT_TOKEN to be loaded, got %q", got)
	}
	if got := os.Getenv("MULTILINE_SECRET"); got != "" {
		t.Fatalf("Expected multiline secret block to be skipped, got %q", got)
	}
}

func TestLoad_AdminAndUnsignedInteractionConfig(t *testing.T) {
	t.Setenv("RFD_ADMIN_TOKEN", "test-admin-token")
	t.Setenv("ALLOW_UNSIGNED_DISCORD_INTERACTIONS", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned unexpected error: %v", err)
	}
	if cfg.RFDAdminToken != "test-admin-token" {
		t.Fatalf("RFDAdminToken = %q, want test-admin-token", cfg.RFDAdminToken)
	}
	if !cfg.AllowUnsignedDiscordInteractions {
		t.Fatal("AllowUnsignedDiscordInteractions = false, want true")
	}
}
