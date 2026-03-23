package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/api"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/ebay"
	"github.com/pauljones0/rfd-discord-bot/internal/facebook"
	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

type Server struct {
	processor           processor.Processor
	ebayProcessor       *ebay.Processor
	facebookProcessor   *facebook.Processor
	memexpressProcessor *memoryexpress.Processor
	aiClient            *ai.Client
	store               processor.DealStore
	wg                  sync.WaitGroup
	sem                 chan struct{} // Semaphore to limit concurrent RFD processing requests
	ebaySem             chan struct{} // Semaphore to limit concurrent eBay processing requests
	facebookSem         chan struct{} // Semaphore to limit concurrent Facebook processing requests
	memexpressSem       chan struct{} // Semaphore to limit concurrent Memory Express processing requests
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
	store, err := storage.New(ctx, cfg.ProjectID)
	if err != nil {
		slog.Error("Critical error initializing Firestore client", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("Error closing Firestore client", "error", err)
		}
	}()

	selectors, err := scraper.LoadConfig()
	if err != nil {
		slog.Warn("Failed to load selectors. Using defaults.", "error", err)
		selectors = scraper.DefaultSelectors()
	}

	n := notifier.New(cfg.DiscordBotToken)
	s := scraper.New(cfg, selectors)
	v := validator.New()

	// Initialize AI client (uses Vertex AI with Application Default Credentials)
	aiClient, err := ai.NewClient(ctx, cfg.ProjectID, cfg.GeminiLocations, cfg.GeminiAPIKey, cfg.GeminiFallbackModels, store)
	if err != nil {
		slog.Warn("Failed to initialize Gemini client (AI features disabled)", "error", err)
	}

	p := processor.New(store, n, s, v, cfg, aiClient)

	// Initialize eBay client (gracefully handles missing credentials)
	ebayClient := ebay.NewClient(cfg.EbayClientID, cfg.EbayClientSecret)
	var ebayProc *ebay.Processor
	if ebayClient != nil {
		ebayProc = ebay.NewProcessor(store, ebayClient, aiClient, n)
		slog.Info("eBay deal processor initialized")
	} else {
		slog.Info("eBay features disabled (EBAY_CLIENT_ID/EBAY_CLIENT_SECRET not set)")
	}

	// Initialize Facebook processor (requires AI client for analysis)
	var fbProc *facebook.Processor
	if aiClient != nil {
		fbProc = facebook.NewProcessor(store, n, aiClient, cfg.ProxyURL)
		if cfg.ProxyURL != "" {
			slog.Info("Facebook Marketplace deal processor initialized with proxy")
		} else {
			slog.Info("Facebook Marketplace deal processor initialized (no proxy)")
		}
	} else {
		slog.Info("Facebook Marketplace features disabled (AI client unavailable)")
	}

	// Initialize Memory Express processor (always available — no special credentials needed)
	meProc := memoryexpress.NewProcessor(store, aiClient, n)
	slog.Info("Memory Express clearance processor initialized")

	srv := &Server{
		processor:           p,
		ebayProcessor:       ebayProc,
		facebookProcessor:   fbProc,
		memexpressProcessor: meProc,
		aiClient:            aiClient,
		store:               store,
		sem:                 make(chan struct{}, 2), // Allow up to 2 concurrent RFD processing attempts
		ebaySem:             make(chan struct{}, 1), // Allow 1 concurrent eBay processing attempt
		facebookSem:         make(chan struct{}, 1), // Allow 1 concurrent Facebook processing attempt
		memexpressSem:       make(chan struct{}, 1), // Allow 1 concurrent Memory Express processing attempt
	}

	apiHandler, err := api.NewHandler(cfg, store)
	if err != nil {
		slog.Error("Failed to initialize API handler", "error", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.ProcessDealsHandler)
	mux.HandleFunc("/process-deals", srv.ProcessDealsHandler)
	mux.HandleFunc("/process-ebay", srv.ProcessEbayHandler)
	mux.HandleFunc("/process-facebook", srv.ProcessFacebookHandler)
	mux.HandleFunc("/process-memoryexpress", srv.ProcessMemoryExpressHandler)
	mux.Handle("/discord/interactions", apiHandler)
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
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok", "firestore": "connected"}); err != nil {
			slog.Error("Failed to encode health response", "error", err)
		}
	})

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           loggingMiddleware(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       60 * time.Second,
	}

	// Graceful shutdown on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		slog.Info("Received signal, shutting down gracefully...", "signal", sig)

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
	slog.Info("Server stopped.")
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	// Non-blocking check to see if we can acquire the semaphore
	select {
	case s.sem <- struct{}{}:
		// acquired
	default:
		slog.Info("ProcessDealsHandler: dropped request due to concurrency limit")
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": "server is busy processing deals"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	// Run processing asynchronously so the HTTP response isn't blocked
	// by scraping, Firestore, and Discord operations that may exceed timeouts.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.sem }() // Release semaphore when done

		defer func() {
			if r := recover(); r != nil {
				slog.Error("Panic in ProcessDeals", "processor", "rfd", "panic", r)
			}
		}()
		slog.Info("Starting RFD deal processing", "processor", "rfd")
		if s.aiClient != nil {
			s.aiClient.LogCurrentState()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		start := time.Now()
		if err := s.processor.ProcessDeals(ctx); err != nil {
			slog.Error("Error processing deals", "processor", "rfd", "error", err)
		}
		slog.Info("RFD deal processing finished", "processor", "rfd", "duration", time.Since(start))
	}()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "Deal processing started.")
}

func (s *Server) ProcessEbayHandler(w http.ResponseWriter, r *http.Request) {
	if s.ebayProcessor == nil {
		slog.Info("ProcessEbayHandler: eBay processor not configured, skipping")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "details": "eBay features not configured"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	select {
	case s.ebaySem <- struct{}{}:
	default:
		slog.Info("ProcessEbayHandler: dropped request due to concurrency limit")
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": "server is busy processing eBay deals"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.ebaySem }()

		defer func() {
			if r := recover(); r != nil {
				slog.Error("Panic in ProcessEbayDeals", "processor", "ebay", "panic", r)
			}
		}()
		slog.Info("Starting eBay deal processing", "processor", "ebay")
		if s.aiClient != nil {
			s.aiClient.LogCurrentState()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		start := time.Now()
		if err := s.ebayProcessor.ProcessEbayDeals(ctx); err != nil {
			slog.Error("Error processing eBay deals", "processor", "ebay", "error", err)
		}
		slog.Info("eBay deal processing finished", "processor", "ebay", "duration", time.Since(start))
	}()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "eBay deal processing started.")
}

func (s *Server) ProcessFacebookHandler(w http.ResponseWriter, r *http.Request) {
	if s.facebookProcessor == nil {
		slog.Info("ProcessFacebookHandler: Facebook processor not configured, skipping")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "skipped", "details": "Facebook features not configured"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	select {
	case s.facebookSem <- struct{}{}:
	default:
		slog.Warn("ProcessFacebookHandler: previous run still active, skipping",
			"processor", "facebook",
		)
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": "previous run still active"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.facebookSem }()

		defer func() {
			if r := recover(); r != nil {
				slog.Error("Panic in ProcessFacebookDeals", "processor", "facebook", "panic", r)
			}
		}()
		slog.Info("Starting Facebook deal processing", "processor", "facebook")
		if s.aiClient != nil {
			s.aiClient.LogCurrentState()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
		defer cancel()
		start := time.Now()
		if err := s.facebookProcessor.ProcessFacebookDeals(ctx); err != nil {
			slog.Error("Error processing Facebook deals", "processor", "facebook", "error", err)
		}
		slog.Info("Facebook deal processing finished", "processor", "facebook", "duration", time.Since(start))
	}()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "Facebook deal processing started.")
}

func (s *Server) ProcessMemoryExpressHandler(w http.ResponseWriter, r *http.Request) {
	select {
	case s.memexpressSem <- struct{}{}:
	default:
		slog.Warn("ProcessMemoryExpressHandler: previous run still active, skipping",
			"processor", "memoryexpress",
		)
		w.WriteHeader(http.StatusTooManyRequests)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "busy", "details": "previous run still active"}); err != nil {
			slog.Error("Failed to encode response", "error", err)
		}
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { <-s.memexpressSem }()

		defer func() {
			if r := recover(); r != nil {
				slog.Error("Panic in ProcessMemExpressDeals", "processor", "memoryexpress", "panic", r)
			}
		}()
		slog.Info("Starting Memory Express deal processing", "processor", "memoryexpress")
		if s.aiClient != nil {
			s.aiClient.LogCurrentState()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		start := time.Now()
		if err := s.memexpressProcessor.ProcessMemExpressDeals(ctx); err != nil {
			slog.Error("Error processing Memory Express deals", "processor", "memoryexpress", "error", err)
		}
		slog.Info("Memory Express deal processing finished", "processor", "memoryexpress", "duration", time.Since(start))
	}()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "Memory Express deal processing started.")
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		slog.Info("HTTP Request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
		slog.Info("HTTP Request Completed", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
