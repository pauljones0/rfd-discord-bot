package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pauljones0/rfd-discord-bot/internal/ai"
	"github.com/pauljones0/rfd-discord-bot/internal/config"
	"github.com/pauljones0/rfd-discord-bot/internal/logger"
	"github.com/pauljones0/rfd-discord-bot/internal/memoryexpress"
	"github.com/pauljones0/rfd-discord-bot/internal/notifier"
	"github.com/pauljones0/rfd-discord-bot/internal/storage"
)

func main() {
	logger.Setup()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("Failed to load configuration", "error", err)
		os.Exit(1)
	}

	store, err := storage.New(ctx, cfg.ProjectID)
	if err != nil {
		slog.Error("Failed to initialize Firestore", "error", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(); err != nil {
			slog.Error("Failed to close Firestore client", "error", err)
		}
	}()

	aiClient, err := ai.NewClient(ctx, cfg.ProjectID, cfg.GeminiLocations, cfg.GeminiAPIKeys, cfg.GeminiFallbackModels, store)
	if err != nil {
		slog.Warn("Failed to initialize Gemini client; Memory Express runner will save items without analysis", "error", err)
	}

	localSession, err := memoryexpress.NewLocalBrowserSession(ctx, memoryexpress.LocalBrowserSessionOptions{
		ChromePath:    cfg.MemoryExpressChromePath,
		ChromeProfile: cfg.MemoryExpressChromeProfile,
		Alerter:       memoryexpress.DesktopAlerter{},
	})
	if err != nil {
		slog.Error("Failed to start local Memory Express browser session", "error", err)
		os.Exit(1)
	}
	defer localSession.Close()

	processor := memoryexpress.NewProcessor(
		store,
		aiClient,
		notifier.New(cfg.DiscordBotToken),
		memoryexpress.WithScrapeFunc(localSession.ScrapeStore),
	)

	runner := memoryexpress.NewLocalRunner(store, processor, localSession, cfg.MemoryExpressPollInterval)

	slog.Info("Starting local Memory Express runner",
		"poll_interval", cfg.MemoryExpressPollInterval.String(),
	)

	if err := runner.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("Local Memory Express runner stopped with error", "error", err)
		os.Exit(1)
	}

	slog.Info("Local Memory Express runner stopped")
}
