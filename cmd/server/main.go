package main

import (
	"context"

	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
	"github.com/pauljones0/rfd-discord-bot/internal/validator"
)

type Server struct {
	processor processor.Processor
	store     processor.DealStore
	wg        sync.WaitGroup
	sem       chan struct{} // Semaphore to limit concurrent processing requests
}

func main() {
	setupLogger()
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
	defer store.Close()

	selectors, err := scraper.LoadConfig()
	if err != nil {
		slog.Warn("Failed to load selectors. Using defaults.", "error", err)
		selectors = scraper.DefaultSelectors()
	}

	n := notifier.New(cfg.DiscordWebhookURL)
	s := scraper.New(cfg, selectors)
	v := validator.New()

	// Initialize AI client (gracefully handles missing key)
	aiClient, err := ai.NewClient(ctx, cfg.GeminiAPIKey, cfg.GeminiModelID)
	if err != nil {
		slog.Warn("Failed to initialize Gemini client (AI features disabled)", "error", err)
	}

	p := processor.New(store, n, s, v, cfg, aiClient)

	srv := &Server{
		processor: p,
		store:     store,
		sem:       make(chan struct{}, 1), // Allow only 1 concurrent request processing attempt
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.ProcessDealsHandler)
	mux.HandleFunc("/process-deals", srv.ProcessDealsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if err := srv.store.Ping(r.Context()); err != nil {
			slog.Error("Health check failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"error", "details": "%v"}`, err)
			return
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok", "firestore": "connected"}`)
	})

	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      loggingMiddleware(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 5 * time.Minute,
		IdleTimeout:  60 * time.Second,
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
		slog.Warn("ProcessDealsHandler: dropped request due to concurrency limit")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprintln(w, `{"status":"busy", "details": "server is busy processing deals"}`)
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
				slog.Error("Panic in ProcessDeals", "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if err := s.processor.ProcessDeals(ctx); err != nil {
			slog.Error("Error processing deals", "error", err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintln(w, "Deal processing started.")
}

func setupLogger() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	// Use TextHandler for now, can be switched to JSONHandler for production
	logger := slog.New(slog.NewTextHandler(os.Stdout, opts))
	slog.SetDefault(logger)
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		slog.Info("HTTP Request", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr)
		next.ServeHTTP(w, r)
		slog.Info("HTTP Request Completed", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
