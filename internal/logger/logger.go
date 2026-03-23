package logger

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

const (
	LevelDebug     = slog.LevelDebug
	LevelInfo      = slog.LevelInfo
	LevelNotice    = slog.Level(2)
	LevelWarn      = slog.LevelWarn
	LevelError     = slog.LevelError
	LevelCritical  = slog.Level(12)
	LevelAlert     = slog.Level(16)
	LevelEmergency = slog.Level(20)
)

// Setup configures the default slog logger for Cloud Run and local development.
func Setup() {
	var programLevel = new(slog.LevelVar) // Info by default
	programLevel.Set(LevelInfo)

	// Allow setting log level via environment variable
	if lvlStr := os.Getenv("LOG_LEVEL"); lvlStr != "" {
		switch strings.ToUpper(lvlStr) {
		case "DEBUG":
			programLevel.Set(LevelDebug)
		case "INFO":
			programLevel.Set(LevelInfo)
		case "NOTICE":
			programLevel.Set(LevelNotice)
		case "WARN", "WARNING":
			programLevel.Set(LevelWarn)
		case "ERROR":
			programLevel.Set(LevelError)
		case "CRITICAL":
			programLevel.Set(LevelCritical)
		case "ALERT":
			programLevel.Set(LevelAlert)
		case "EMERGENCY":
			programLevel.Set(LevelEmergency)
		default:
			// Fallback to error or default
			programLevel.Set(LevelInfo)
		}
	}

	opts := &slog.HandlerOptions{
		Level: programLevel,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Rename the level key to "severity" for Cloud Logging
			if a.Key == slog.LevelKey {
				a.Key = "severity"

				level := a.Value.Any().(slog.Level)
				switch {
				case level < LevelInfo:
					a.Value = slog.StringValue("DEBUG")
				case level < LevelNotice:
					a.Value = slog.StringValue("INFO")
				case level < LevelWarn:
					a.Value = slog.StringValue("NOTICE")
				case level < LevelError:
					a.Value = slog.StringValue("WARNING")
				case level < LevelCritical:
					a.Value = slog.StringValue("ERROR")
				case level < LevelAlert:
					a.Value = slog.StringValue("CRITICAL")
				case level < LevelEmergency:
					a.Value = slog.StringValue("ALERT")
				default:
					a.Value = slog.StringValue("EMERGENCY")
				}
			}
			return a
		},
	}

	var handler slog.Handler
	// Use JSONHandler in Cloud Run for proper structured log parsing
	if os.Getenv("K_SERVICE") != "" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)
}

// Notice logs at LevelNotice.
func Notice(msg string, args ...any) {
	slog.Default().Log(context.Background(), LevelNotice, msg, args...)
}

// Critical logs at LevelCritical.
func Critical(msg string, args ...any) {
	slog.Default().Log(context.Background(), LevelCritical, msg, args...)
}


