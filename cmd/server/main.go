package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/processor"
	"github.com/pauljones0/rfd-discord-bot/internal/scraper"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

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

	selectors, err := scraper.LoadSelectors("config/selectors.json")
	if err != nil {
		log.Printf("Warning: Failed to load selectors: %v. Using defaults.", err)
		selectors = scraper.DefaultSelectors
	}

	n := notifier.New(cfg.DiscordWebhookURL)
	s := scraper.New(cfg, selectors)
	p := processor.New(store, n, s, cfg)

	srv := &Server{processor: p}

	http.HandleFunc("/", srv.ProcessDealsHandler)
	http.HandleFunc("/process-deals", srv.ProcessDealsHandler)

	log.Printf("Listening on port %s", cfg.Port)
	if err := http.ListenAndServe(":"+cfg.Port, nil); err != nil {
		log.Fatalf("Failed to listen and serve: %v", err)
	}
}

func (s *Server) ProcessDealsHandler(w http.ResponseWriter, r *http.Request) {
	if err := s.processor.ProcessDeals(r.Context()); err != nil {
		log.Printf("Error processing deals: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	fmt.Fprintln(w, "Deals processed successfully.")
}
