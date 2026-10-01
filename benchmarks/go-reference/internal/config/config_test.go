package config

import "testing"

func TestMinimalConfigurationNeedsNoOtherServices(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN", "test-token")
	t.Setenv("DISCORD_APP_ID", "123")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GEMINI_MODELS", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.GeminiAPIKeys) != 0 || c.AmazonAffiliateTag != "" || c.BestBuyAffiliatePrefix != "" {
		t.Fatal("minimal configuration enabled optional credentials/tracking")
	}
}
func TestInvalidPollIntervalsFailClearly(t *testing.T) {
	t.Setenv("DISCORD_BOT_TOKEN", "test-token")
	t.Setenv("DISCORD_APP_ID", "123")
	for _, value := range []string{"invalid", "0s", "-1m"} {
		t.Setenv("RFD_POLL_INTERVAL", value)
		if _, err := Load(); err == nil {
			t.Fatalf("accepted interval %q", value)
		}
	}
}
