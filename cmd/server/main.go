package main

import (
	"context"
	"embed"
	"fmt"
	"log"
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
	log.Println("Starting RFD Hot Deals Bot server...")
	cfg := config.Load()

	ctx := context.Background()
	store, err := storage.New(ctx, cfg.ProjectID)
	if err != nil {
		log.Fatalf("Critical error initializing Firestore client: %v", err)
	}
	defer store.Close()

	selectors, err := loadSelectorsWithFallback()
	if err != nil {
		log.Printf("Warning: Failed to load selectors: %v. Using defaults.", err)
		selectors = scraper.DefaultSelectors
	}

	n := notifier.New(cfg.DiscordWebhookURL)
	s := scraper.New(cfg, selectors)
	p := processor.New(store, n, s, cfg)

	srv := &Server{processor: p}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.ProcessDealsHandler)
	mux.HandleFunc("/process-deals", srv.ProcessDealsHandler)

	httpServer := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: mux,
	}

	// Graceful shutdown on SIGTERM/SIGINT
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down gracefully...", sig)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP server shutdown error: %v", err)
		}
	}()

	log.Printf("Listening on port %s", cfg.Port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Failed to listen and serve: %v", err)
	}
	log.Println("Server stopped.")
}

// loadSelectorsWithFallback tries the embedded selectors first,
// then falls back to the external config file.
func loadSelectorsWithFallback() (scraper.SelectorConfig, error) {
	data, err := embeddedSelectors.ReadFile("selectors.json")
	if err == nil {
		sel, parseErr := scraper.LoadSelectorsFromBytes(data)
		if parseErr == nil {
			log.Println("Loaded selectors from embedded config.")
			return sel, nil
		}
		log.Printf("Warning: Embedded selectors failed to parse: %v. Trying file fallback.", parseErr)
	}

	// Fallback to external file
	configPath := os.Getenv("SELECTORS_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/selectors.json"
	}
	return scraper.LoadSelectors(configPath)
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.processor.ProcessDeals(r.Context()); err != nil {
		log.Printf("Error processing deals: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "Deals processed successfully.")
}
