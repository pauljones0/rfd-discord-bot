package models

import "time"

// CoreRawNotification stores the exact notification content received from the phone listener.
type CoreRawNotification struct {
	EventID       string                       `docstore:"eventId"`
	SourcePackage string                       `docstore:"sourcePackage"`
	Title         string                       `docstore:"title"`
	Message       string                       `docstore:"message"`
	Lines         []string                     `docstore:"lines"`
	Messages      []CoreRawNotificationMessage `docstore:"messages,omitempty"`
	ReceivedAt    time.Time                    `docstore:"receivedAt"`
}

type CoreRawNotificationMessage struct {
	Sender string `docstore:"sender"`
	Text   string `docstore:"text"`
	Time   int64  `docstore:"time"`
	URI    string `docstore:"uri,omitempty"`
}
