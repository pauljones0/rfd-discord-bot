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
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/pauljones0/rfd-discord-standalone/internal/ai"
	"github.com/pauljones0/rfd-discord-standalone/internal/api"
	"github.com/pauljones0/rfd-discord-standalone/internal/config"
	"github.com/pauljones0/rfd-discord-standalone/internal/logger"
	"github.com/pauljones0/rfd-discord-standalone/internal/notifier"
	"github.com/pauljones0/rfd-discord-standalone/internal/processor"
	"github.com/pauljones0/rfd-discord-standalone/internal/scraper"
	"github.com/pauljones0/rfd-discord-standalone/internal/storage"
	"github.com/pauljones0/rfd-discord-standalone/internal/validator"
)

func main() {
	logger.Setup()
	if err := run(); err != nil {
		slog.Error("RFD bot stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	command := "run"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "healthcheck" {
		addr := os.Getenv("LISTEN_ADDR")
		if addr == "" {
			addr = "127.0.0.1:8080"
		}
		client := http.Client{Timeout: 3 * time.Second}
		r, err := client.Get("http://" + addr + "/health")
		if err != nil {
			return err
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return fmt.Errorf("health returned %d", r.StatusCode)
		}
		return nil
	}
	if command == "check-storage" {
		path := os.Getenv("SQLITE_PATH")
		if path == "" {
			path = "data/rfd.sqlite"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		store, err := storage.Open(ctx, path)
		if err != nil {
			return err
		}
		defer store.Close()
		if err = store.Ping(ctx); err != nil {
			return err
		}
		fmt.Println("SQLite storage is ready.")
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if command == "check-config" {
		fmt.Println("RFD configuration is valid (credentials not contacted).")
		return nil
	}
	if command != "run" && command != "register" {
		return fmt.Errorf("usage: rfd-bot [run|register|check-config|check-storage|healthcheck]")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if command == "register" {
		c, stop := context.WithTimeout(ctx, 20*time.Second)
		defer stop()
		if err = api.CheckApplication(c, cfg.DiscordBotToken, cfg.DiscordAppID); err != nil {
			return err
		}
		return api.Register(c, cfg.DiscordBotToken, cfg.DiscordAppID, cfg.DiscordGuildID)
	}
	checkCtx, stopCheck := context.WithTimeout(ctx, 20*time.Second)
	err = api.CheckApplication(checkCtx, cfg.DiscordBotToken, cfg.DiscordAppID)
	stopCheck()
	if err != nil {
		return err
	}
	store, err := storage.Open(ctx, cfg.SQLitePath)
	if err != nil {
		return err
	}
	defer store.Close()
	aiClient, err := ai.NewClient(ctx, "", nil, cfg.GeminiAPIKeys, cfg.GeminiModels, store)
	if err != nil {
		return err
	}
	var analyzer processor.DealAnalyzer
	if aiClient != nil {
		analyzer = aiClient
	}
	selectors, err := scraper.LoadConfig()
	if err != nil {
		return err
	}
	p := processor.New(store, notifier.New(cfg.DiscordBotToken), scraper.New(cfg, selectors), validator.New(), cfg, analyzer)
	gateway, err := api.NewGateway(cfg.DiscordBotToken, api.NewHandler(store))
	if err != nil {
		return err
	}
	var lastOK, lastAttempt atomic.Int64
	var lastFailed atomic.Bool
	mux := http.NewServeMux()
	mux.Handle("GET /health/discord", gateway)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		ok := store.Ping(r.Context()) == nil
		if !ok {
			w.WriteHeader(503)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "last_poll_attempt": lastAttempt.Load(), "last_successful_poll": lastOK.Load(), "last_poll_failed": lastFailed.Load()})
	})
	server := &http.Server{Addr: cfg.ListenAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second}
	var workers sync.WaitGroup
	workers.Go(func() { gateway.Run(ctx) })
	workers.Go(func() {
		timer := time.NewTimer(0)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			// With no subscribers there is no reason to fetch pages or use AI quota.
			subs, err := store.GetAllSubscriptions(ctx)
			if err == nil && len(subs) > 0 {
				lastAttempt.Store(time.Now().Unix())
				pollCtx, stop := context.WithTimeout(ctx, cfg.PollTimeout)
				err = p.ProcessDeals(pollCtx)
				stop()
				lastFailed.Store(err != nil)
				if err == nil {
					lastOK.Store(time.Now().Unix())
				} else {
					slog.Error("RFD poll failed", "error", err)
				}
			} else if err != nil {
				slog.Error("Could not read RFD subscriptions", "error", err)
			}
			timer.Reset(cfg.RFDPollInterval)
		}
	})
	serverError := make(chan error, 1)
	go func() { serverError <- server.ListenAndServe() }()
	slog.Info("RFD bot started", "health_address", cfg.ListenAddr, "poll_interval", cfg.RFDPollInterval, "ai_enabled", aiClient != nil)
	select {
	case <-ctx.Done():
	case err = <-serverError:
		cancel()
	}
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopShutdown()
	_ = server.Shutdown(shutdownCtx)
	workers.Wait()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
