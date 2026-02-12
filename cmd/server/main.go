package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

//go:embed selectors.json
var embeddedSelectors embed.FS

type Server struct {
	processor processor.Processor
}

func main() {
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

	selectors, err := loadSelectorsWithFallback()
	if err != nil {
		slog.Warn("Failed to load selectors. Using defaults.", "error", err)
		selectors = scraper.DefaultSelectors()
	}

	n := notifier.New(cfg.DiscordWebhookURL)
	s := scraper.New(cfg, selectors)
	p := processor.New(store, n, s, cfg)

	srv := &Server{processor: p}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.ProcessDealsHandler)
	mux.HandleFunc("/process-deals", srv.ProcessDealsHandler)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, `{"status":"ok"}`)
	})

	httpServer := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      mux,
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
	}()

	slog.Info("Listening on port", "port", cfg.Port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("Failed to listen and serve", "error", err)
		os.Exit(1)
	}
	slog.Info("Server stopped.")
}

// loadSelectorsWithFallback tries the embedded selectors first,
// then falls back to the external config file.
func loadSelectorsWithFallback() (scraper.SelectorConfig, error) {
	data, err := embeddedSelectors.ReadFile("selectors.json")
	if err == nil {
		sel, parseErr := scraper.LoadSelectorsFromBytes(data)
		if parseErr == nil {
			slog.Info("Loaded selectors from embedded config.")
			return sel, nil
		}
		slog.Warn("Embedded selectors failed to parse. Trying file fallback.", "error", parseErr)
	}

	// Fallback to external file
	configPath := os.Getenv("SELECTORS_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/selectors.json"
	}
	return scraper.LoadSelectors(configPath)
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	// Run processing asynchronously so the HTTP response isn't blocked
	// by scraping, Firestore, and Discord operations that may exceed timeouts.
	go func() {
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
