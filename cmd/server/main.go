package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/api"
	"github.com/pauljones0/rfd-discord-bot/internal/bestbuy"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/core"
	"github.com/pauljones0/rfd-discord-bot/internal/crux"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/facebook"
	"github.com/pauljones0/rfd-discord-bot/internal/hardwareswap"
	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/oneverycorner"
	"github.com/pauljones0/rfd-discord-bot/internal/paidbrowser"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/reddit"
	"github.com/pauljones0/rfd-discord-bot/internal/scrapebackend"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

type Server struct {
	processor               processor.Processor
	ebayProcessor           *ebay.Processor
	facebookProcessor       *facebook.Processor
	memexpressProcessor     *memoryexpress.Processor
	bestbuyProcessor        *bestbuy.Processor
	bestbuyCompute          *bestbuy.ComputeProcessor
	hwProcessor             *hardwareswap.Processor
	coreProcessor           *core.Processor
	cruxProcessor           *crux.Processor
	onEveryCorner           *oneverycorner.Processor
	onEveryCornerController *oneverycorner.Controller
	aiClient                *ai.Client
	store                   processor.DealStore
	db                      *storage.Client
	wg                      sync.WaitGroup
	sem                     chan struct{} // Semaphore to limit concurrent RFD processing requests
	ebaySem                 chan struct{} // Semaphore to limit concurrent eBay processing requests
	facebookSem             chan struct{} // Semaphore to limit concurrent Facebook processing requests
	facebookRunStart        atomic.Int64  // Unix timestamp (seconds) when the current Facebook run started
	memexpressSem           chan struct{} // Semaphore to limit concurrent Memory Express processing requests
	bestbuySem              chan struct{} // Semaphore to limit concurrent Best Buy processing requests
	bestbuyComputeSem       chan struct{} // Semaphore to limit concurrent Best Buy compute sweeps
	cruxSem                 chan struct{} // Semaphore to limit concurrent Crux Investor sweeps
	hwSem                   chan struct{} // Semaphore to limit concurrent HardwareSwap processing requests
	coreIssueMu             sync.Mutex
	coreIssueLast           map[string]time.Time
}

func main() {
	logger.Setup()
	slog.Info("Starting RFD Hot Deals Bot server...")
	cfg, err := config.Load()
	if err != nil {
		slog.Error("Critical error loading configuration", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()
	schedulerCtx, schedulerCancel := context.WithCancel(ctx)
	store, err := storage.New(ctx)
	if err != nil {
		slog.Error("Critical error initializing storage client", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("Error closing storage client", "error", err)
		}
	}()

	selectors, err := scraper.LoadConfig()
	if err != nil {
		slog.Warn("Failed to load selectors. Using defaults.", "error", err)
		selectors = scraper.DefaultSelectors()
	}

	n := notifier.New(cfg.DiscordBotToken,
		cfg.XAPIKey, cfg.XAPIKeySecret, cfg.XAccessToken, cfg.XAccessTokenSecret,
		cfg.X2APIKey, cfg.X2APIKeySecret, cfg.X2AccessToken, cfg.X2AccessTokenSecret)
	s := scraper.New(cfg, selectors)
	v := validator.New()

	// Initialize AI client (uses Vertex AI with Application Default Credentials)
	aiClient, err := ai.NewClient(ctx, cfg.ProjectID, cfg.GeminiLocations, cfg.GeminiAPIKeys, cfg.GeminiFallbackModels, store)
	if err != nil {
		slog.Warn("Failed to initialize Gemini client (AI features disabled)", "error", err)
	}

	p := processor.New(store, n, s, v, cfg, aiClient)

	// Initialize eBay client (gracefully handles missing credentials)
	ebayClient := ebay.NewClient(cfg.EbayClientID, cfg.EbayClientSecret)
	var ebayProc *ebay.Processor
	if ebayClient != nil {
		ebayCouponBackends := scrapebackend.FilterBackendsForPaidEnabled(cfg.EbayCouponBackends, cfg.EbayPaidBrowserEnabled)
		ebayClient.SetCouponBackends(ebayCouponBackends)
		ebayClient.SetPaidBrowserEnabled(cfg.EbayPaidBrowserEnabled)
		ebayProc = ebay.NewProcessor(store, ebayClient, n)
		ebayProc.SetCouponDiscoveryInterval(cfg.EbayCouponDiscoveryInterval)
		ebayProc.SetPaidLimiter(paidbrowser.NewLimiter(store, "ebay", cfg.EbayPaidBrowserMaxPerRun, cfg.EbayPaidBrowserMaxPerDay))
		slog.Info("eBay deal processor initialized", "coupon_backends", ebayCouponBackends, "paid_browser_enabled", cfg.EbayPaidBrowserEnabled)
	} else {
		slog.Info("eBay features disabled (EBAY_CLIENT_ID/EBAY_CLIENT_SECRET not set)")
	}

	// Initialize Facebook processor only when explicitly enabled.
	var fbProc *facebook.Processor
	if cfg.FacebookEnabled && aiClient != nil {
		fbProc = facebook.NewProcessor(store, n, aiClient, cfg.ProxyURL, cfg.CarfaxTokenServiceURL, cfg.CarfaxTokenServiceSecret)
		if cfg.CarfaxTokenServiceURL != "" {
			slog.Info("Facebook Marketplace deal processor initialized with Carfax token service",
				"token_service_url", cfg.CarfaxTokenServiceURL)
		} else if cfg.ProxyURL != "" {
			slog.Info("Facebook Marketplace deal processor initialized with proxy (Carfax Playwright fallback)")
		} else {
			slog.Info("Facebook Marketplace deal processor initialized (no proxy, no token service)")
		}
	} else if !cfg.FacebookEnabled {
		slog.Info("Facebook Marketplace features disabled (FACEBOOK_ENABLED=false)")
	} else {
		slog.Info("Facebook Marketplace features disabled (AI client unavailable)")
	}

	// Initialize Memory Express processor (always available — no special credentials needed)
	memexpressPaidLimiter := paidbrowser.NewLimiter(store, "memoryexpress", cfg.MemoryExpressPaidMaxPerRun, cfg.MemoryExpressPaidMaxPerDay)
	memexpressBackends := scrapebackend.FilterBackendsForPaidEnabled(cfg.MemoryExpressBackends, cfg.MemoryExpressPaidBrowserEnabled)
	meProc := memoryexpress.NewProcessor(
		store,
		aiClient,
		n,
		memoryexpress.WithScrapeFunc(memoryexpress.ScrapeWithConfiguredBackends(memexpressBackends, cfg.MemoryExpressChromeProfile, cfg.MemoryExpressPaidBrowserEnabled, memexpressPaidLimiter.BeforeAttempt)),
		memoryexpress.WithBeforeRun(memexpressPaidLimiter.BeginRun),
	)
	slog.Info("Memory Express clearance processor initialized", "backends", memexpressBackends, "paid_browser_enabled", cfg.MemoryExpressPaidBrowserEnabled)

	// Initialize Best Buy processor (always available — no special credentials needed)
	bbClient := bestbuy.NewClient()
	bbClient.SetBackends(cfg.BestBuyBackends)
	bbProc := bestbuy.NewProcessor(store, bbClient, aiClient, n, cfg.BestBuyAffiliatePrefix)
	bbSoldCompBackends := scrapebackend.FilterBackendsForPaidEnabled(cfg.BestBuySoldCompBackends, cfg.BestBuySoldCompPaidEnabled)
	if cfg.BestBuySoldCompsEnabled {
		bbSoldCompPaidLimiter := paidbrowser.NewLimiter(store, "bestbuy_seller_ebay_sold", cfg.BestBuySoldCompPaidMaxPerRun, cfg.BestBuySoldCompPaidMaxPerDay)
		bbProc.SetSoldCompEnricher(bestbuy.NewSoldCompEnricher(bestbuy.SoldCompEnricherOptions{
			Enabled:     true,
			Store:       store,
			Backends:    bbSoldCompBackends,
			CacheTTL:    cfg.BestBuySoldCompCacheTTL,
			MaxPerRun:   cfg.BestBuySoldCompMaxPerRun,
			QueryDelay:  cfg.BestBuySoldCompQueryDelay,
			PaidEnabled: cfg.BestBuySoldCompPaidEnabled,
			PaidAttempt: bbSoldCompPaidLimiter.BeforeAttempt,
			BeforeRun:   bbSoldCompPaidLimiter.BeginRun,
		}))
	}
	bbComputeProc := bestbuy.NewComputeProcessor(store, bbClient, n, cfg.BestBuyAffiliatePrefix, cfg.BestBuyComputeAlertFirstSeen, bestbuy.NewComputeEmbedder(cfg.BestBuyComputeEmbedCommand))
	bbComputeSoldBackends := scrapebackend.FilterBackendsForPaidEnabled(cfg.BestBuyComputeSoldBackends, cfg.BestBuyComputeSoldPaidEnabled)
	if cfg.BestBuyComputeSoldVerifyEnabled {
		bbComputeSoldPaidLimiter := paidbrowser.NewLimiter(store, "bestbuy_compute_ebay_sold", cfg.BestBuyComputeSoldPaidMaxPerRun, cfg.BestBuyComputeSoldPaidMaxPerDay)
		bbComputeProc.SetSoldVerifier(bestbuy.NewEbaySoldVerifier(bestbuy.EbaySoldVerifierOptions{
			Enabled:     true,
			Backends:    bbComputeSoldBackends,
			CacheTTL:    cfg.BestBuyComputeSoldCacheTTL,
			QueryDelay:  cfg.BestBuyComputeSoldQueryDelay,
			PaidEnabled: cfg.BestBuyComputeSoldPaidEnabled,
			PaidAttempt: bbComputeSoldPaidLimiter.BeforeAttempt,
			BeforeRun:   bbComputeSoldPaidLimiter.BeginRun,
		}))
	}
	slog.Info("Best Buy Marketplace processor initialized", "backends", cfg.BestBuyBackends, "sold_comps_enabled", cfg.BestBuySoldCompsEnabled, "sold_comp_backends", bbSoldCompBackends, "sold_comp_paid_browser_enabled", cfg.BestBuySoldCompPaidEnabled)
	slog.Info("Best Buy compute outlier processor initialized",
		"enabled", cfg.BestBuyComputeEnabled,
		"interval", cfg.BestBuyComputePollInterval.String(),
		"alert_first_seen", cfg.BestBuyComputeAlertFirstSeen,
		"ebay_sold_verify_enabled", cfg.BestBuyComputeSoldVerifyEnabled,
		"ebay_sold_backends", bbComputeSoldBackends,
		"ebay_sold_paid_enabled", cfg.BestBuyComputeSoldPaidEnabled,
	)

	cruxBackends := scrapebackend.FilterBackendsForPaidEnabled(cfg.CruxBackends, cfg.CruxPaidEnabled)
	cruxClient := crux.NewClient(crux.ClientConfig{
		BaseURL:     cfg.CruxBaseURL,
		Backends:    cruxBackends,
		Exchanges:   cfg.CruxExchanges,
		Timeout:     cfg.CruxFetchTimeout,
		PageDelay:   cfg.CruxPageDelay,
		PageJitter:  cfg.CruxPageJitter,
		MaxPages:    cfg.CruxMaxPages,
		PaidEnabled: cfg.CruxPaidEnabled,
	})
	cruxProc := crux.NewProcessor(store, cruxClient, n, cfg.CruxExchanges)
	slog.Info("Crux Investor processor initialized",
		"enabled", cfg.CruxEnabled,
		"interval", cfg.CruxPollInterval.String(),
		"backends", cruxBackends,
		"page_delay", cfg.CruxPageDelay.String(),
		"page_jitter", cfg.CruxPageJitter.String(),
		"max_pages", cfg.CruxMaxPages,
	)

	// Initialize HardwareSwap processor only when explicitly enabled.
	var hwProc *hardwareswap.Processor
	if cfg.HardwareSwapEnabled && aiClient != nil {
		hwStore := hardwareswapStore(store)
		redditClient := reddit.NewClient(cfg.RedditServiceURL, cfg.RedditServiceSecret, store)
		hwProc = hardwareswap.NewProcessor(hwStore, redditClient, aiClient, cfg.DiscordBotToken)
		slog.Info("HardwareSwap processor initialized")
	} else if !cfg.HardwareSwapEnabled {
		slog.Info("HardwareSwap features disabled (HARDWARESWAP_ENABLED=false)")
	} else {
		slog.Info("HardwareSwap features disabled (AI client unavailable)")
	}

	// Initialize Core processor with RateManager
	rates := core.NewRateManager()
	if err := rates.FetchRates(ctx); err != nil {
		slog.Error("Failed to fetch initial exchange rates, using defaults", "error", err)
	}
	rates.StartAutoRefresh(schedulerCtx, 24*time.Hour)

	coreProc := core.NewProcessor(store, n, rates)
	onEveryCornerProc := oneverycorner.NewProcessor(store, n)
	onEveryCornerController := oneverycorner.NewController(onEveryCornerProc, oneverycorner.ControllerConfig{
		Enabled:                    cfg.OnEveryCornerEnabled,
		PrimarySource:              cfg.OnEveryCornerPrimarySource,
		BackupSources:              cfg.OnEveryCornerBackupSources,
		ScheduleCachePath:          cfg.OnEveryCornerScheduleCachePath,
		ScheduleLookahead:          cfg.OnEveryCornerScheduleLookahead,
		ScheduleRefreshInterval:    cfg.OnEveryCornerScheduleRefreshInterval,
		PendingKickoffPollInterval: cfg.OnEveryCornerPendingKickoffPollInterval,
		PendingKickoffTimeout:      cfg.OnEveryCornerPendingKickoffTimeout,
		LivePollInterval:           cfg.OnEveryCornerLivePollInterval,
		PostLiveGracePeriod:        cfg.OnEveryCornerPostLiveGracePeriod,
		TotalCornerAPI: oneverycorner.TotalCornerAPIConfig{
			BaseURL:   cfg.OnEveryCornerTotalCornerAPIURL,
			Token:     cfg.OnEveryCornerTotalCornerAPIToken,
			LeagueIDs: cfg.OnEveryCornerTotalCornerLeagueIDs,
			Timezone:  cfg.OnEveryCornerTotalCornerTimezone,
		},
		Scoremer: oneverycorner.ScoremerConfig{
			URL:          cfg.OnEveryCornerScoremerURL,
			PollInterval: cfg.OnEveryCornerScoremerPollInterval,
			LeagueIDs:    cfg.OnEveryCornerScoremerLeagueIDs,
		},
	})

	srv := &Server{
		processor:               p,
		ebayProcessor:           ebayProc,
		facebookProcessor:       fbProc,
		memexpressProcessor:     meProc,
		bestbuyProcessor:        bbProc,
		bestbuyCompute:          bbComputeProc,
		hwProcessor:             hwProc,
		coreProcessor:           coreProc,
		cruxProcessor:           cruxProc,
		onEveryCorner:           onEveryCornerProc,
		onEveryCornerController: onEveryCornerController,
		aiClient:                aiClient,
		store:                   store,
		db:                      store,
		sem:                     make(chan struct{}, 2), // Allow up to 2 concurrent RFD processing attempts
		ebaySem:                 make(chan struct{}, 1), // Allow 1 concurrent eBay processing attempt
		facebookSem:             make(chan struct{}, 1), // Allow 1 concurrent Facebook processing attempt
		memexpressSem:           make(chan struct{}, 1), // Allow 1 concurrent Memory Express processing attempt
		bestbuySem:              make(chan struct{}, 1), // Allow 1 concurrent Best Buy processing attempt
		bestbuyComputeSem:       make(chan struct{}, 1), // Allow 1 concurrent Best Buy compute sweep
		cruxSem:                 make(chan struct{}, 1), // Allow 1 concurrent Crux Investor sweep
		hwSem:                   make(chan struct{}, 1), // Allow 1 concurrent HardwareSwap processing attempt
		coreIssueLast:           make(map[string]time.Time),
	}

	// Build HardwareSwap store for the API handler (may be nil if AI is unavailable)
	var hwStoreForAPI *hardwareswap.Store
	if hwProc != nil {
		hwStoreForAPI = hardwareswapStore(store)
	}
	apiHandler, err := api.NewHandler(cfg, store, hwStoreForAPI, aiClient)
	if err != nil {
		slog.Error("Failed to initialize API handler", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)
	adminHandle := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, adminOnly(cfg.RFDAdminToken, handler))
	}
	adminHandle("GET /process-deals", srv.ProcessDealsHandler)
	adminHandle("GET /process-ebay", srv.ProcessEbayHandler)
	if cfg.FacebookEnabled {
		adminHandle("GET /process-facebook", srv.ProcessFacebookHandler)
	}
	adminHandle("GET /process-memoryexpress", srv.ProcessMemoryExpressHandler)
	adminHandle("GET /process-bestbuy", srv.ProcessBestBuyHandler)
	adminHandle("GET /process-bestbuy-compute", srv.ProcessBestBuyComputeHandler)
	adminHandle("GET /process-crux", srv.ProcessCruxHandler)
	adminHandle("POST /prime-bestbuy-baseline", srv.PrimeBestBuyBaselineHandler)
	mux.Handle("POST /ingest/discord-notification", swordswallowerOnly(cfg.RFDAdminToken, cfg.SwordswallowerSecret, http.HandlerFunc(srv.DiscordNotificationIngestHandler)))
	adminHandle("POST /core/rebin", srv.CoreRebinHandler)
	adminHandle("GET /core/raw-notifications", srv.CoreRawNotificationsHandler)
	if cfg.HardwareSwapEnabled {
		adminHandle("GET /process-hardwareswap", srv.ProcessHardwareSwapHandler)
	}
	mux.Handle("/discord/interactions", apiHandler)
	mux.HandleFunc("POST /register-token-service", func(w http.ResponseWriter, r *http.Request) {
		if !cfg.FacebookEnabled || cfg.CarfaxTokenServiceSecret == "" {
			http.Error(w, "disabled", http.StatusNotFound)
			return
		}
		// Authenticate with the token service secret
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || authHeader != "Bearer "+cfg.CarfaxTokenServiceSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			http.Error(w, "invalid request: must provide {\"url\": \"...\"}", http.StatusBadRequest)
			return
		}

		if err := store.SaveTokenServiceURL(r.Context(), body.URL); err != nil {
			slog.Error("Failed to save token service URL", "error", err, "url", body.URL)
			http.Error(w, "failed to save URL", http.StatusInternalServerError)
			return
		}

		slog.Info("Token service URL registered", "url", body.URL)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "url": body.URL})
	})
	mux.HandleFunc("POST /register-reddit-service", func(w http.ResponseWriter, r *http.Request) {
		if !cfg.HardwareSwapEnabled || cfg.RedditServiceSecret == "" {
			http.Error(w, "disabled", http.StatusNotFound)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || authHeader != "Bearer "+cfg.RedditServiceSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		var body struct {
			URL string `json:"url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
			http.Error(w, "invalid request: must provide {\"url\": \"...\"}", http.StatusBadRequest)
			return
		}

		if err := store.SaveRedditServiceURL(r.Context(), body.URL); err != nil {
			slog.Error("Failed to save reddit service URL", "error", err, "url", body.URL)
			http.Error(w, "failed to save URL", http.StatusInternalServerError)
			return
		}

		slog.Info("Reddit service URL registered", "url", body.URL)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "url": body.URL})
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if err := srv.store.Ping(r.Context()); err != nil {
			slog.Error("Health check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			if encErr := json.NewEncoder(w).Encode(map[string]string{"status": "error", "details": err.Error()}); encErr != nil {
				slog.Error("Failed to encode health response", "error", encErr)
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok", "storage": store.Backend(), "details": "connected"}); err != nil {
			slog.Error("Failed to encode health response", "error", err)
		}
	})

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           loggingMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	srv.StartLocalScheduler(schedulerCtx, cfg)

	// Graceful shutdown on SIGTERM/SIGINT
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully...", "signal", sig)
		schedulerCancel()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}

		// Wait for any in-flight ProcessDeals goroutines to finish
		slog.Info("Waiting for in-flight deal processing to complete...")
		srv.wg.Wait()
		slog.Info("All in-flight processing completed.")
	}()

	slog.Info("Listening on port", "port", cfg.Port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Failed to listen and serve", "error", err)
		os.Exit(1)
	}
	<-shutdownDone
	slog.Info("Server stopped.")
}

func hardwareswapStore(store *storage.Client) *hardwareswap.Store {
	if store == nil {
		return nil
	}
	return hardwareswap.NewDocumentStore(store)
}

type manualProcessOptions struct {
	processorName string
	startMessage  string
	finishMessage string
	errorMessage  string
	panicMessage  string
	successText   string
	busyDetails   string
	sem           chan struct{}
	timeout       time.Duration
	fn            func(context.Context) error
	logAIState    bool
	runStart      *atomic.Int64
}

func (s *Server) runManualProcess(w http.ResponseWriter, r *http.Request, opts manualProcessOptions) {
	select {
	case opts.sem <- struct{}{}:
	default:
		attrs := []any{"processor", opts.processorName}
		if opts.runStart != nil {
			if started := opts.runStart.Load(); started > 0 {
				attrs = append(attrs, "running_for", time.Since(time.Unix(started, 0)).Round(time.Second).String())
			}
		}
		slog.Warn("Manual processor request skipped because previous run is active", attrs...)
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": opts.busyDetails}); err != nil {
			slog.Error("Failed to encode response", "processor", opts.processorName, "error", err)
		}
		return
	}

	if opts.runStart != nil {
		opts.runStart.Store(time.Now().Unix())
	}
	defer func() {
		if opts.runStart != nil {
			opts.runStart.Store(0)
		}
		<-opts.sem
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error(opts.panicMessage, "processor", opts.processorName, "panic", recovered)
			http.Error(w, opts.errorMessage+" panicked", http.StatusInternalServerError)
		}
	}()

	slog.Info(opts.startMessage, "processor", opts.processorName)
	if opts.logAIState && s.aiClient != nil {
		s.aiClient.LogCurrentState()
	}
	ctx, cancel := context.WithTimeout(r.Context(), opts.timeout)
	defer cancel()
	start := time.Now()
	if err := opts.fn(ctx); err != nil {
		slog.Error("Manual processor failed", "processor", opts.processorName, "error", err)
		http.Error(w, opts.errorMessage+" failed", http.StatusInternalServerError)
		return
	}
	slog.Info(opts.finishMessage, "processor", opts.processorName, "duration", time.Since(start))

	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, opts.successText)
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "rfd",
		startMessage:  "Starting RFD deal processing",
		finishMessage: "RFD deal processing finished",
		errorMessage:  "deal processing",
		panicMessage:  "Panic in ProcessDeals",
		successText:   "Deal processing finished.",
		busyDetails:   "server is busy processing deals",
		sem:           s.sem,
		timeout:       4 * time.Minute,
		fn: func(ctx context.Context) error {
			if s.processor == nil {
				return nil
			}
			return s.processor.ProcessDeals(ctx)
		},
		logAIState: true,
	})
}

func (s *Server) ProcessEbayHandler(w http.ResponseWriter, r *http.Request) {
	if s.ebayProcessor == nil {
		writeSkipped(w, "ebay", "eBay features not configured")
		return
	}
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "ebay",
		startMessage:  "Starting eBay deal processing",
		finishMessage: "eBay deal processing finished",
		errorMessage:  "eBay deal processing",
		panicMessage:  "Panic in ProcessEbayDeals",
		successText:   "eBay deal processing finished.",
		busyDetails:   "server is busy processing eBay deals",
		sem:           s.ebaySem,
		timeout:       4 * time.Minute,
		fn:            s.ebayProcessor.ProcessEbayDeals,
	})
}

func (s *Server) ProcessFacebookHandler(w http.ResponseWriter, r *http.Request) {
	if s.facebookProcessor == nil {
		writeSkipped(w, "facebook", "Facebook features not configured")
		return
	}
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "facebook",
		startMessage:  "Starting Facebook deal processing",
		finishMessage: "Facebook deal processing finished",
		errorMessage:  "Facebook deal processing",
		panicMessage:  "Panic in ProcessFacebookDeals",
		successText:   "Facebook deal processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.facebookSem,
		timeout:       4 * time.Minute,
		fn:            s.facebookProcessor.ProcessFacebookDeals,
		logAIState:    true,
		runStart:      &s.facebookRunStart,
	})
}

func (s *Server) ProcessMemoryExpressHandler(w http.ResponseWriter, r *http.Request) {
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "memoryexpress",
		startMessage:  "Starting Memory Express deal processing",
		finishMessage: "Memory Express deal processing finished",
		errorMessage:  "Memory Express deal processing",
		panicMessage:  "Panic in ProcessMemExpressDeals",
		successText:   "Memory Express deal processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.memexpressSem,
		timeout:       2 * time.Minute,
		fn:            s.memexpressProcessor.ProcessMemExpressDeals,
		logAIState:    true,
	})
}

func (s *Server) ProcessBestBuyHandler(w http.ResponseWriter, r *http.Request) {
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "bestbuy",
		startMessage:  "Starting Best Buy deal processing",
		finishMessage: "Best Buy deal processing finished",
		errorMessage:  "Best Buy deal processing",
		panicMessage:  "Panic in ProcessBestBuyDeals",
		successText:   "Best Buy deal processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.bestbuySem,
		timeout:       8 * time.Minute,
		fn:            s.bestbuyProcessor.ProcessBestBuyDeals,
		logAIState:    true,
	})
}

func (s *Server) ProcessBestBuyComputeHandler(w http.ResponseWriter, r *http.Request) {
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "bestbuy_compute",
		startMessage:  "Starting Best Buy compute outlier processing",
		finishMessage: "Best Buy compute outlier processing finished",
		errorMessage:  "Best Buy compute outlier processing",
		panicMessage:  "Panic in ProcessBestBuyComputeOutliers",
		successText:   "Best Buy compute outlier processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.bestbuyComputeSem,
		timeout:       20 * time.Minute,
		fn:            s.bestbuyCompute.ProcessComputeOutliers,
		logAIState:    false,
	})
}

func (s *Server) ProcessCruxHandler(w http.ResponseWriter, r *http.Request) {
	if s.cruxProcessor == nil {
		writeSkipped(w, "crux", "Crux processor not configured")
		return
	}
	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "crux",
		startMessage:  "Starting Crux Investor processing",
		finishMessage: "Crux Investor processing finished",
		errorMessage:  "Crux Investor processing",
		panicMessage:  "Panic in ProcessCruxChanges",
		successText:   "Crux Investor processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.cruxSem,
		timeout:       30 * time.Minute,
		fn:            s.cruxProcessor.ProcessCruxChanges,
		logAIState:    false,
	})
}

func (s *Server) PrimeBestBuyBaselineHandler(w http.ResponseWriter, r *http.Request) {
	if s.bestbuyProcessor == nil {
		slog.Info("PrimeBestBuyBaselineHandler: Best Buy processor not configured, skipping", "processor", "bestbuy")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "details": "Best Buy processor not configured"}); err != nil {
			slog.Error("Failed to encode response", "processor", "bestbuy", "error", err)
		}
		return
	}

	select {
	case s.bestbuySem <- struct{}{}:
	default:
		slog.Warn("PrimeBestBuyBaselineHandler: previous run still active, skipping",
			"processor", "bestbuy",
		)
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": "previous run still active"}); err != nil {
			slog.Error("Failed to encode response", "processor", "bestbuy", "error", err)
		}
		return
	}

	defer func() { <-s.bestbuySem }()
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Error("Panic in PrimeBestBuyBaseline", "processor", "bestbuy", "panic", recovered)
			http.Error(w, "Best Buy baseline prime panicked", http.StatusInternalServerError)
		}
	}()

	slog.Info("Starting Best Buy baseline prime", "processor", "bestbuy")
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Minute)
	defer cancel()
	start := time.Now()
	stats, err := s.bestbuyProcessor.PrimeBaseline(ctx)
	if err != nil {
		slog.Error("Error priming Best Buy baseline", "processor", "bestbuy", "error", err)
		http.Error(w, "Best Buy baseline prime failed", http.StatusInternalServerError)
		return
	}
	slog.Info("Best Buy baseline prime finished", "processor", "bestbuy", "duration", time.Since(start), "saved", stats.Saved)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]any{"status": "ok", "stats": stats}); err != nil {
		slog.Error("Failed to encode response", "processor", "bestbuy", "error", err)
	}
}

func (s *Server) ProcessHardwareSwapHandler(w http.ResponseWriter, r *http.Request) {
	if s.hwProcessor == nil {
		writeSkipped(w, "hardwareswap", "HardwareSwap features not configured")
		return
	}

	s.runManualProcess(w, r, manualProcessOptions{
		processorName: "hardwareswap",
		startMessage:  "Starting HardwareSwap deal processing",
		finishMessage: "HardwareSwap deal processing finished",
		errorMessage:  "HardwareSwap deal processing",
		panicMessage:  "Panic in ProcessHardwareSwapDeals",
		successText:   "HardwareSwap deal processing finished.",
		busyDetails:   "previous run still active",
		sem:           s.hwSem,
		timeout:       4 * time.Minute,
		fn:            s.hwProcessor.ProcessHardwareSwapDeals,
		logAIState:    true,
	})
}

func writeSkipped(w http.ResponseWriter, processorName, details string) {
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "details": details}); err != nil {
		slog.Error("Failed to encode response", "processor", processorName, "error", err)
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		slog.Info("HTTP Request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		rec := &statusResponseWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		slog.Info("HTTP Request Completed",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.statusCode(),
			"bytes", rec.bytes,
			"duration", time.Since(start),
		)
	})
}

type statusResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *statusResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
		slog.Error("Failed to encode root response", "error", err)
	}
}

// CoreRawNotificationsHandler returns recent raw notifications for rule-building and audit purposes.
func (s *Server) CoreRawNotificationsHandler(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		http.Error(w, "storage client not initialized", http.StatusInternalServerError)
		return
	}

	daysStr := r.URL.Query().Get("days")
	days := 7
	if daysStr != "" {
		if val, err := strconv.Atoi(daysStr); err == nil && val > 0 {
			days = val
		}
	}
	if days > 30 {
		days = 30
	}

	duration := time.Duration(days) * 24 * time.Hour
	list, err := s.db.GetRecentCoreRawNotifications(r.Context(), duration)
	if err != nil {
		slog.Error("Failed to retrieve core raw notifications", "error", err)
		http.Error(w, "internal server error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(list); err != nil {
		slog.Error("Failed to encode core raw notifications response", "error", err)
	}
}
