package config

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
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
	GeminiAPIKeys          []string
	GeminiLocations        []string
	GeminiFallbackModels   []string

	// Discord App Auth
	DiscordAppID     string
	DiscordPublicKey string
	DiscordBotToken  string

	// eBay API (optional — eBay features disabled if not set)
	EbayClientID     string
	EbayClientSecret string

	// Proxy (optional — Facebook/Carfax scraping runs without proxy if not set)
	ProxyURL string

	// Memory Express local runner configuration.
	MemoryExpressPollInterval  time.Duration
	MemoryExpressChromePath    string
	MemoryExpressChromeProfile string

	// Carfax Token Service (optional — if not set, Carfax falls back to Playwright UI automation)
	// The token service runs a real headed Chrome that generates high-scoring reCAPTCHA v3 tokens.
	// See cmd/token-service/main.go for setup instructions.
	CarfaxTokenServiceURL    string
	CarfaxTokenServiceSecret string

	// Reddit Service (optional — Reddit processors disabled if not set)
	RedditServiceURL    string
	RedditServiceSecret string
}

func Load() (*Config, error) {
	// Try loading from .env file. Some local .env files include multiline JSON blobs
	// that godotenv can't parse, so fall back to a loose loader that still picks up
	// normal KEY=value lines around those blocks.
	if err := godotenv.Load(); err != nil {
		if fallbackErr := loadLooseDotEnv(".env"); fallbackErr == nil {
			slog.Warn("Loaded .env with loose parser after standard parser failed")
		}
	}

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

	memexpressPollIntervalStr := os.Getenv("MEMEXPRESS_POLL_INTERVAL")
	if memexpressPollIntervalStr == "" {
		memexpressPollIntervalStr = "30m"
	}
	memexpressPollInterval, err := time.ParseDuration(memexpressPollIntervalStr)
	if err != nil {
		return nil, fmt.Errorf("invalid MEMEXPRESS_POLL_INTERVAL %q: %w", memexpressPollIntervalStr, err)
	}

	geminiLocation := os.Getenv("GEMINI_LOCATION")
	if geminiLocation == "" {
		geminiLocation = "us-central1"
	}

	// GEMINI_LOCATIONS overrides GEMINI_LOCATION with a comma-separated list of regions
	// for multi-region failover. If not set, defaults to a broad set of regions.
	var geminiLocations []string
	if v := os.Getenv("GEMINI_LOCATIONS"); v != "" {
		for _, loc := range strings.Split(v, ",") {
			loc = strings.TrimSpace(loc)
			if loc != "" {
				geminiLocations = append(geminiLocations, loc)
			}
		}
	}
	if len(geminiLocations) == 0 {
		geminiLocations = []string{
			geminiLocation,
			"us-east4",
			"us-west1",
			"us-west4",
			"europe-west1",
			"europe-west4",
			"asia-northeast1",
			"asia-southeast1",
		}
	}

	var geminiAPIKeys []string
	if v := os.Getenv("GEMINI_API_KEY"); v != "" {
		for _, k := range strings.Split(v, ",") {
			k = strings.TrimSpace(k)
			if k != "" {
				geminiAPIKeys = append(geminiAPIKeys, k)
			}
		}
	}

	return &Config{
		ProjectID:              projectID,
		Port:                   port,
		AmazonAffiliateTag:     amazonAffiliateTag,
		BestBuyAffiliatePrefix: bestBuyAffiliatePrefix,
		DiscordUpdateInterval:  discordUpdateInterval,
		MaxStoredDeals:         maxStoredDeals,
		AllowedDomains:         []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com", "bestbuy.ca"},
		RFDBaseURL:             "https://forums.redflagdeals.com",
		GeminiAPIKeys:          geminiAPIKeys,
		GeminiLocations:        geminiLocations,
		GeminiFallbackModels: []string{
			"gemini-2.5-flash-lite",
			"gemini-2.5-flash",
			"gemini-2.5-pro",
		},
		DiscordAppID:               os.Getenv("DISCORD_APP_ID"),
		DiscordPublicKey:           discordPublicKey,
		DiscordBotToken:            discordBotToken,
		EbayClientID:               os.Getenv("EBAY_CLIENT_ID"),
		EbayClientSecret:           os.Getenv("EBAY_CLIENT_SECRET"),
		ProxyURL:                   os.Getenv("PROXY_URL"),
		MemoryExpressPollInterval:  memexpressPollInterval,
		MemoryExpressChromePath:    firstNonEmpty(os.Getenv("MEMEXPRESS_CHROME_PATH"), os.Getenv("CHROME_PATH")),
		MemoryExpressChromeProfile: os.Getenv("MEMEXPRESS_CHROME_PROFILE_DIR"),
		CarfaxTokenServiceURL:      os.Getenv("CARFAX_TOKEN_SERVICE_URL"),
		CarfaxTokenServiceSecret:   os.Getenv("CARFAX_TOKEN_SERVICE_SECRET"),
		RedditServiceURL:           os.Getenv("REDDIT_SERVICE_URL"),
		RedditServiceSecret:        os.Getenv("REDDIT_SERVICE_SECRET"),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func loadLooseDotEnv(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	skippingBlock := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if skippingBlock {
			if line == "}" {
				skippingBlock = false
			}
			continue
		}

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:eq])
		if !isEnvKey(key) {
			continue
		}

		value := strings.TrimSpace(line[eq+1:])
		if strings.HasPrefix(value, "{") && !strings.HasSuffix(value, "}") {
			skippingBlock = true
			continue
		}

		if os.Getenv(key) != "" {
			continue
		}

		if err := os.Setenv(key, strings.Trim(value, `"'`)); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func isEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '_' && i >= 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
