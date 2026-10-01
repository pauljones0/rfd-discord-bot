package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config contains only settings used by the standalone RedFlagDeals bot.
type Config struct {
	DiscordBotToken        string
	DiscordAppID           string
	DiscordGuildID         string
	SQLitePath             string
	ListenAddr             string
	RFDPollInterval        time.Duration
	PollTimeout            time.Duration
	DiscordUpdateInterval  time.Duration
	MaxStoredDeals         int
	RFDBaseURL             string
	AllowedDomains         []string
	AmazonAffiliateTag     string
	BestBuyAffiliatePrefix string
	GeminiAPIKeys          []string
	GeminiModels           []string
}

func Load() (*Config, error) {
	c := &Config{
		DiscordBotToken:        strings.TrimSpace(os.Getenv("DISCORD_BOT_TOKEN")),
		DiscordAppID:           strings.TrimSpace(os.Getenv("DISCORD_APP_ID")),
		DiscordGuildID:         strings.TrimSpace(os.Getenv("DISCORD_GUILD_ID")),
		SQLitePath:             value("SQLITE_PATH", "data/rfd.sqlite"),
		ListenAddr:             value("LISTEN_ADDR", "127.0.0.1:8080"),
		RFDBaseURL:             "https://forums.redflagdeals.com",
		AllowedDomains:         []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com"},
		AmazonAffiliateTag:     strings.TrimSpace(os.Getenv("AMAZON_AFFILIATE_TAG")),
		BestBuyAffiliatePrefix: strings.TrimSpace(os.Getenv("BESTBUY_AFFILIATE_PREFIX")),
		GeminiAPIKeys:          csv(os.Getenv("GEMINI_API_KEY")),
		GeminiModels:           csv(os.Getenv("GEMINI_MODELS")),
	}
	var err error
	if c.RFDPollInterval, err = duration("RFD_POLL_INTERVAL", 3*time.Minute); err != nil {
		return nil, err
	}
	if c.PollTimeout, err = duration("RFD_POLL_TIMEOUT", 4*time.Minute); err != nil {
		return nil, err
	}
	if c.DiscordUpdateInterval, err = duration("DISCORD_UPDATE_INTERVAL", 10*time.Minute); err != nil {
		return nil, err
	}
	c.MaxStoredDeals, err = strconv.Atoi(value("MAX_STORED_DEALS", "2000"))
	if err != nil || c.MaxStoredDeals < 1 {
		return nil, fmt.Errorf("MAX_STORED_DEALS must be a positive integer")
	}
	if c.DiscordBotToken == "" || c.DiscordAppID == "" {
		return nil, fmt.Errorf("DISCORD_BOT_TOKEN and DISCORD_APP_ID are required")
	}
	if len(c.GeminiAPIKeys) > 0 && len(c.GeminiModels) == 0 {
		return nil, fmt.Errorf("set GEMINI_MODELS to supported model IDs when enabling optional Gemini title cleanup")
	}
	if c.BestBuyAffiliatePrefix != "" {
		u, e := url.Parse(c.BestBuyAffiliatePrefix)
		if e != nil || u.Scheme != "https" || u.Host == "" {
			return nil, fmt.Errorf("BESTBUY_AFFILIATE_PREFIX must be an HTTPS URL prefix")
		}
	}
	return c, nil
}

func value(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
func duration(key string, fallback time.Duration) (time.Duration, error) {
	v, e := time.ParseDuration(value(key, fallback.String()))
	if e != nil || v <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return v, nil
}
func csv(raw string) []string {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
