package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	ProjectID              string
	Port                   string
	AmazonAffiliateTag     string
	BestBuyAffiliatePrefix string
	DiscordUpdateInterval  time.Duration
	MaxStoredDeals         int
	AllowedDomains         []string
	RFDBaseURL             string
	GeminiAPIKey           string
	GeminiLocation         string
	GeminiFallbackModels   []string

	// Discord App Auth
	DiscordAppID     string
	DiscordPublicKey string
	DiscordBotToken  string

	// eBay API (optional — eBay features disabled if not set)
	EbayClientID     string
	EbayClientSecret string
}

func Load() (*Config, error) {
	// Try loading from .env file (ignore error if it doesn't exist)
	_ = godotenv.Load()

	projectID := os.Getenv("GOOGLE_CLOUD_PROJECT")
	if projectID == "" {
		return nil, fmt.Errorf("GOOGLE_CLOUD_PROJECT environment variable is required but not set")
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

	discordPublicKey := os.Getenv("DISCORD_PUBLIC_KEY")
	discordBotToken := os.Getenv("DISCORD_BOT_TOKEN")
	if discordBotToken == "" {
		slog.Warn("DISCORD_BOT_TOKEN not set, Discord application features may be disabled")
	}

	geminiLocation := os.Getenv("GEMINI_LOCATION")
	if geminiLocation == "" {
		geminiLocation = "us-central1"
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")

	return &Config{
		ProjectID:              projectID,
		Port:                   port,
		AmazonAffiliateTag:     amazonAffiliateTag,
		BestBuyAffiliatePrefix: bestBuyAffiliatePrefix,
		DiscordUpdateInterval:  discordUpdateInterval,
		MaxStoredDeals:         maxStoredDeals,
		AllowedDomains:         []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com", "bestbuy.ca"},
		RFDBaseURL:             "https://forums.redflagdeals.com",
		GeminiAPIKey:           geminiAPIKey,
		GeminiLocation:         geminiLocation,
		GeminiFallbackModels: []string{
			"gemini-2.5-flash-lite",
			"gemini-2.5-flash",
			"gemini-2.5-pro",
		},
		DiscordAppID:     os.Getenv("DISCORD_APP_ID"),
		DiscordPublicKey: discordPublicKey,
		DiscordBotToken:  discordBotToken,
		EbayClientID:     os.Getenv("EBAY_CLIENT_ID"),
		EbayClientSecret: os.Getenv("EBAY_CLIENT_SECRET"),
	}, nil
}
