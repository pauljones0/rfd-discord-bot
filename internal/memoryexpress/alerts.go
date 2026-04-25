package memoryexpress

import (
	"log/slog"

	"github.com/gen2brain/beeep"
)

// Alerter delivers local notifications when a manual challenge solve is needed.
type Alerter interface {
	Alert(title, message string) error
}

// DesktopAlerter sends native desktop notifications on the local machine.
type DesktopAlerter struct{}

// Alert sends a desktop notification.
func (DesktopAlerter) Alert(title, message string) error {
	return beeep.Notify(title, message, "")
}

// LoggingAlerter is a no-op fallback that only logs alerts.
type LoggingAlerter struct{}

// Alert logs the alert message.
func (LoggingAlerter) Alert(title, message string) error {
	slog.Warn(title, "message", message)
	return nil
}
