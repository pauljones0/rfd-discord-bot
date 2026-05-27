package models

import "time"

// CoreRawNotification stores the exact notification content received from the phone listener.
type CoreRawNotification struct {
	EventID       string    `docstore:"eventId"`
	SourcePackage string    `docstore:"sourcePackage"`
	Title         string    `docstore:"title"`
	Message       string    `docstore:"message"`
	Lines         []string  `docstore:"lines"`
	ReceivedAt    time.Time `docstore:"receivedAt"`
}
