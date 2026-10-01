// Package logger configures structured container logs.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

func Setup() {
	level := slog.LevelInfo
	switch strings.ToUpper(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN", "WARNING":
		level = slog.LevelWarn
	case "ERROR", "CRITICAL", "ALERT", "EMERGENCY":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if attr.Key == slog.LevelKey {
				attr.Key = "severity"
			}
			return attr
		},
	})))
}
