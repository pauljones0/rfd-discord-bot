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
	DatabaseURL            string
	Port                   string
	AmazonAffiliateTag     string
	BestBuyAffiliatePrefix string
	DiscordUpdateInterval  time.Duration
	RFDPollInterval        time.Duration
	MaxStoredDeals         int
	AllowedDomains         []string
	RFDBaseURL             string
	GeminiAPIKeys          []string
	GeminiLocations        []string
	GeminiFallbackModels   []string
	LocalSchedulerEnabled  bool
	RFDAdminToken          string
	SwordswallowerSecret   string

	// OnEveryCorner source controller configuration.
	OnEveryCornerEnabled                    bool
	OnEveryCornerPrimarySource              string
	OnEveryCornerBackupSources              []string
	OnEveryCornerScheduleCachePath          string
	OnEveryCornerScheduleLookahead          time.Duration
	OnEveryCornerScheduleRefreshInterval    time.Duration
	OnEveryCornerPendingKickoffPollInterval time.Duration
	OnEveryCornerPendingKickoffTimeout      time.Duration
	OnEveryCornerLivePollInterval           time.Duration
	OnEveryCornerPostLiveGracePeriod        time.Duration
	OnEveryCornerTotalCornerAPIToken        string
	OnEveryCornerTotalCornerAPIURL          string
	OnEveryCornerTotalCornerLeagueIDs       []string
	OnEveryCornerTotalCornerTimezone        string
	OnEveryCornerScoremerURL                string
	OnEveryCornerScoremerPollInterval       time.Duration
	OnEveryCornerScoremerLeagueIDs          []string

	// Discord App Auth
	DiscordAppID                     string
	DiscordPublicKey                 string
	DiscordBotToken                  string
	AllowUnsignedDiscordInteractions bool

	// eBay API (optional — eBay features disabled if not set)
	EbayClientID                string
	EbayClientSecret            string
	EbayCouponBackends          []string
	EbayPollInterval            time.Duration
	EbayCouponDiscoveryInterval time.Duration
	EbayPaidBrowserEnabled      bool
	EbayPaidBrowserMaxPerRun    int
	EbayPaidBrowserMaxPerDay    int

	// Proxy (optional — Facebook/Carfax scraping runs without proxy if not set)
	ProxyURL string

	// Memory Express local runner configuration.
	MemoryExpressPollInterval       time.Duration
	MemoryExpressChromePath         string
	MemoryExpressChromeProfile      string
	MemoryExpressBackends           []string
	MemoryExpressPaidBrowserEnabled bool
	MemoryExpressPaidMaxPerRun      int
	MemoryExpressPaidMaxPerDay      int

	// Best Buy scraping backend configuration.
	BestBuyBackends                 []string
	BestBuyPollInterval             time.Duration
	BestBuyComputeEnabled           bool
	BestBuyComputePollInterval      time.Duration
	BestBuyComputeAlertFirstSeen    bool
	BestBuyComputeEmbedCommand      string
	BestBuyComputeSoldVerifyEnabled bool
	BestBuyComputeSoldBackends      []string
	BestBuyComputeSoldCacheTTL      time.Duration
	BestBuyComputeSoldQueryDelay    time.Duration
	BestBuyComputeSoldPaidEnabled   bool
	BestBuyComputeSoldPaidMaxPerRun int
	BestBuyComputeSoldPaidMaxPerDay int
	BestBuySoldCompsEnabled         bool
	BestBuySoldCompBackends         []string
	BestBuySoldCompCacheTTL         time.Duration
	BestBuySoldCompQueryDelay       time.Duration
	BestBuySoldCompMaxPerRun        int
	BestBuySoldCompPaidEnabled      bool
	BestBuySoldCompPaidMaxPerRun    int
	BestBuySoldCompPaidMaxPerDay    int

	// Carfax Token Service (optional — if not set, Carfax falls back to Playwright UI automation)
	// The token service runs a real headed Chrome that generates high-scoring reCAPTCHA v3 tokens.
	// See cmd/token-service/main.go for setup instructions.
	CarfaxTokenServiceURL    string
	CarfaxTokenServiceSecret string

	// Reddit Service (optional — Reddit processors disabled if not set)
	RedditServiceURL    string
	RedditServiceSecret string

	// X (Twitter) API credentials for posting OnEveryCorner goal alerts (alongside Discord).
	// First account
	XAPIKey            string
	XAPIKeySecret      string
	XAccessToken       string
	XAccessTokenSecret string

	// Second X account
	X2APIKey            string
	X2APIKeySecret      string
	X2AccessToken       string
	X2AccessTokenSecret string

	FacebookEnabled     bool
	HardwareSwapEnabled bool
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

	discordUpdateInterval, err := durationEnv("DISCORD_UPDATE_INTERVAL", 10*time.Minute)
	if err != nil {
		return nil, err
	}

	rfdPollInterval, err := durationEnv("RFD_POLL_INTERVAL", 3*time.Minute)
	if err != nil {
		return nil, err
	}

	ebayPollInterval, err := durationEnv("EBAY_POLL_INTERVAL", 30*time.Minute)
	if err != nil {
		return nil, err
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

	memexpressPollInterval, err := durationEnv("MEMEXPRESS_POLL_INTERVAL", 30*time.Minute)
	if err != nil {
		return nil, err
	}

	bestBuyPollInterval, err := durationEnv("BESTBUY_POLL_INTERVAL", 30*time.Minute)
	if err != nil {
		return nil, err
	}

	bestBuyComputePollInterval, err := durationEnv("BESTBUY_COMPUTE_POLL_INTERVAL", time.Hour)
	if err != nil {
		return nil, err
	}

	bestBuyComputeSoldCacheTTL, err := durationEnv("BESTBUY_COMPUTE_SOLD_CACHE_TTL", 24*time.Hour)
	if err != nil {
		return nil, err
	}

	bestBuyComputeSoldQueryDelay, err := durationEnv("BESTBUY_COMPUTE_SOLD_QUERY_DELAY", 3*time.Second)
	if err != nil {
		return nil, err
	}

	bestBuySoldCompCacheTTL, err := durationEnv("BESTBUY_SOLD_COMP_CACHE_TTL", 24*time.Hour)
	if err != nil {
		return nil, err
	}

	bestBuySoldCompQueryDelay, err := durationEnv("BESTBUY_SOLD_COMP_QUERY_DELAY", 3*time.Second)
	if err != nil {
		return nil, err
	}

	ebayCouponDiscoveryInterval, err := durationEnv("EBAY_COUPON_DISCOVERY_INTERVAL", 6*time.Hour)
	if err != nil {
		return nil, err
	}

	oneverycornerScheduleLookahead, err := durationEnv("ONEVERYCORNER_SCHEDULE_LOOKAHEAD", 36*time.Hour)
	if err != nil {
		return nil, err
	}
	oneverycornerScheduleRefreshInterval, err := durationEnv("ONEVERYCORNER_SCHEDULE_REFRESH_INTERVAL", 15*time.Minute)
	if err != nil {
		return nil, err
	}
	oneverycornerPendingKickoffPollInterval, err := durationEnv("ONEVERYCORNER_PENDING_KICKOFF_POLL_INTERVAL", 30*time.Second)
	if err != nil {
		return nil, err
	}
	oneverycornerPendingKickoffTimeout, err := durationEnv("ONEVERYCORNER_PENDING_KICKOFF_TIMEOUT", time.Hour)
	if err != nil {
		return nil, err
	}
	oneverycornerLivePollInterval, err := durationEnv("ONEVERYCORNER_LIVE_POLL_INTERVAL", 6*time.Second)
	if err != nil {
		return nil, err
	}
	oneverycornerPostLiveGracePeriod, err := durationEnv("ONEVERYCORNER_POST_LIVE_GRACE_PERIOD", 10*time.Minute)
	if err != nil {
		return nil, err
	}
	scoremerPollInterval, err := durationEnv("ONEVERYCORNER_SCOREMER_POLL_INTERVAL", 10*time.Second)
	if err != nil {
		return nil, err
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
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		Port:                   port,
		AmazonAffiliateTag:     amazonAffiliateTag,
		BestBuyAffiliatePrefix: bestBuyAffiliatePrefix,
		DiscordUpdateInterval:  discordUpdateInterval,
		RFDPollInterval:        rfdPollInterval,
		MaxStoredDeals:         maxStoredDeals,
		AllowedDomains:         []string{"redflagdeals.com", "forums.redflagdeals.com", "www.redflagdeals.com", "bestbuy.ca"},
		RFDBaseURL:             "https://forums.redflagdeals.com",
		GeminiAPIKeys:          geminiAPIKeys,
		GeminiLocations:        geminiLocations,
		GeminiFallbackModels: []string{
			"gemini-2.5-flash-lite",
			"gemini-2.5-flash",
			"gemini-3.5-flash",
			"gemini-2.5-pro",
		},
		RFDAdminToken:                           os.Getenv("RFD_ADMIN_TOKEN"),
		DiscordAppID:                            os.Getenv("DISCORD_APP_ID"),
		DiscordPublicKey:                        discordPublicKey,
		DiscordBotToken:                         discordBotToken,
		AllowUnsignedDiscordInteractions:        boolEnv("ALLOW_UNSIGNED_DISCORD_INTERACTIONS", false),
		EbayClientID:                            os.Getenv("EBAY_CLIENT_ID"),
		EbayClientSecret:                        os.Getenv("EBAY_CLIENT_SECRET"),
		EbayCouponBackends:                      csvEnv("EBAY_COUPON_BACKENDS", []string{"http", "external-stealth", "camoufox", "ai-crawler", "paid-trial"}),
		EbayPollInterval:                        ebayPollInterval,
		EbayCouponDiscoveryInterval:             ebayCouponDiscoveryInterval,
		EbayPaidBrowserEnabled:                  boolEnv("EBAY_PAID_BROWSER_ENABLED", false),
		EbayPaidBrowserMaxPerRun:                intEnv("EBAY_PAID_BROWSER_MAX_CALLS_PER_RUN", 1),
		EbayPaidBrowserMaxPerDay:                intEnv("EBAY_PAID_BROWSER_MAX_CALLS_PER_DAY", 6),
		ProxyURL:                                os.Getenv("PROXY_URL"),
		MemoryExpressPollInterval:               memexpressPollInterval,
		MemoryExpressChromePath:                 firstNonEmpty(os.Getenv("MEMEXPRESS_CHROME_PATH"), os.Getenv("CHROME_PATH")),
		MemoryExpressChromeProfile:              os.Getenv("MEMEXPRESS_CHROME_PROFILE_DIR"),
		MemoryExpressBackends:                   csvEnv("MEMEXPRESS_BACKENDS", []string{"http", "external-stealth", "camoufox", "ai-crawler", "paid-trial"}),
		MemoryExpressPaidBrowserEnabled:         boolEnv("MEMEXPRESS_PAID_BROWSER_ENABLED", false),
		MemoryExpressPaidMaxPerRun:              intEnv("MEMEXPRESS_PAID_BROWSER_MAX_CALLS_PER_RUN", 0),
		MemoryExpressPaidMaxPerDay:              intEnv("MEMEXPRESS_PAID_BROWSER_MAX_CALLS_PER_DAY", 0),
		BestBuyBackends:                         csvEnv("BESTBUY_BACKENDS", []string{"bestbuy-algolia", "http"}),
		BestBuyPollInterval:                     bestBuyPollInterval,
		BestBuyComputeEnabled:                   boolEnv("BESTBUY_COMPUTE_ENABLED", false),
		BestBuyComputePollInterval:              bestBuyComputePollInterval,
		BestBuyComputeAlertFirstSeen:            boolEnv("BESTBUY_COMPUTE_ALERT_FIRST_SEEN", false),
		BestBuyComputeEmbedCommand:              os.Getenv("BESTBUY_COMPUTE_EMBED_COMMAND"),
		BestBuyComputeSoldVerifyEnabled:         boolEnv("BESTBUY_COMPUTE_SOLD_VERIFY_ENABLED", false),
		BestBuyComputeSoldBackends:              csvEnv("BESTBUY_COMPUTE_SOLD_BACKENDS", []string{"http", "external-stealth", "camoufox", "ai-crawler"}),
		BestBuyComputeSoldCacheTTL:              bestBuyComputeSoldCacheTTL,
		BestBuyComputeSoldQueryDelay:            bestBuyComputeSoldQueryDelay,
		BestBuyComputeSoldPaidEnabled:           boolEnv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_ENABLED", false),
		BestBuyComputeSoldPaidMaxPerRun:         intEnv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_MAX_CALLS_PER_RUN", 0),
		BestBuyComputeSoldPaidMaxPerDay:         intEnv("BESTBUY_COMPUTE_SOLD_PAID_BROWSER_MAX_CALLS_PER_DAY", 0),
		BestBuySoldCompsEnabled:                 boolEnv("BESTBUY_SOLD_COMPS_ENABLED", true),
		BestBuySoldCompBackends:                 csvEnv("BESTBUY_SOLD_COMP_BACKENDS", []string{"http", "external-stealth", "camoufox", "ai-crawler"}),
		BestBuySoldCompCacheTTL:                 bestBuySoldCompCacheTTL,
		BestBuySoldCompQueryDelay:               bestBuySoldCompQueryDelay,
		BestBuySoldCompMaxPerRun:                intEnv("BESTBUY_SOLD_COMP_MAX_PER_RUN", 10),
		BestBuySoldCompPaidEnabled:              boolEnv("BESTBUY_SOLD_COMP_PAID_BROWSER_ENABLED", false),
		BestBuySoldCompPaidMaxPerRun:            intEnv("BESTBUY_SOLD_COMP_PAID_BROWSER_MAX_CALLS_PER_RUN", 0),
		BestBuySoldCompPaidMaxPerDay:            intEnv("BESTBUY_SOLD_COMP_PAID_BROWSER_MAX_CALLS_PER_DAY", 0),
		LocalSchedulerEnabled:                   boolEnv("LOCAL_SCHEDULER_ENABLED", false),
		CarfaxTokenServiceURL:                   os.Getenv("CARFAX_TOKEN_SERVICE_URL"),
		CarfaxTokenServiceSecret:                os.Getenv("CARFAX_TOKEN_SERVICE_SECRET"),
		RedditServiceURL:                        os.Getenv("REDDIT_SERVICE_URL"),
		RedditServiceSecret:                     os.Getenv("REDDIT_SERVICE_SECRET"),
		XAPIKey:                                 os.Getenv("X_API_KEY"),
		XAPIKeySecret:                           os.Getenv("X_API_KEY_SECRET"),
		XAccessToken:                            os.Getenv("X_ACCESS_TOKEN"),
		XAccessTokenSecret:                      os.Getenv("X_ACCESS_TOKEN_SECRET"),
		X2APIKey:                                os.Getenv("X2_API_KEY"),
		X2APIKeySecret:                          os.Getenv("X2_API_KEY_SECRET"),
		X2AccessToken:                           os.Getenv("X2_ACCESS_TOKEN"),
		X2AccessTokenSecret:                     os.Getenv("X2_ACCESS_TOKEN_SECRET"),
		OnEveryCornerEnabled:                    boolEnv("ONEVERYCORNER_ENABLED", false),
		OnEveryCornerPrimarySource:              firstNonEmpty(os.Getenv("ONEVERYCORNER_PRIMARY_SOURCE"), "totalcorner"),
		OnEveryCornerBackupSources:              csvEnv("ONEVERYCORNER_BACKUP_SOURCES", []string{"scoremer"}),
		OnEveryCornerScheduleCachePath:          firstNonEmpty(os.Getenv("ONEVERYCORNER_SCHEDULE_CACHE_PATH"), "/data/oneverycorner-schedule-cache.json"),
		OnEveryCornerScheduleLookahead:          oneverycornerScheduleLookahead,
		OnEveryCornerScheduleRefreshInterval:    oneverycornerScheduleRefreshInterval,
		OnEveryCornerPendingKickoffPollInterval: oneverycornerPendingKickoffPollInterval,
		OnEveryCornerPendingKickoffTimeout:      oneverycornerPendingKickoffTimeout,
		OnEveryCornerLivePollInterval:           oneverycornerLivePollInterval,
		OnEveryCornerPostLiveGracePeriod:        oneverycornerPostLiveGracePeriod,
		OnEveryCornerTotalCornerAPIToken:        os.Getenv("ONEVERYCORNER_TOTALCORNER_API_TOKEN"),
		OnEveryCornerTotalCornerAPIURL:          firstNonEmpty(os.Getenv("ONEVERYCORNER_TOTALCORNER_API_URL"), "https://api.totalcorner.com/v1"),
		OnEveryCornerTotalCornerLeagueIDs:       csvEnv("ONEVERYCORNER_TOTALCORNER_LEAGUE_IDS", []string{"29754"}),
		OnEveryCornerTotalCornerTimezone:        firstNonEmpty(os.Getenv("ONEVERYCORNER_TOTALCORNER_TIMEZONE"), "Europe/London"),
		OnEveryCornerScoremerURL:                firstNonEmpty(os.Getenv("ONEVERYCORNER_SCOREMER_URL"), "https://lv.scoremer.com/"),
		OnEveryCornerScoremerPollInterval:       scoremerPollInterval,
		OnEveryCornerScoremerLeagueIDs:          csvEnv("ONEVERYCORNER_SCOREMER_LEAGUE_IDS", []string{"3559"}),
		SwordswallowerSecret:                    os.Getenv("SWORDSWALLOWER_SECRET"),
		FacebookEnabled:                         boolEnv("FACEBOOK_ENABLED", false),
		HardwareSwapEnabled:                     boolEnv("HARDWARESWAP_ENABLED", false),
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

func csvEnv(key string, fallback []string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return append([]string(nil), fallback...)
	}
	values := strings.Split(raw, ",")
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return append([]string(nil), fallback...)
	}
	return out
}

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q: %w", key, raw, err)
	}
	return parsed, nil
}

func boolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("Invalid boolean env value; using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return parsed
}

func intEnv(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("Invalid integer env value; using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return parsed
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
