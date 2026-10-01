package models

import "time"

type Subscription struct {
	GuildID     string
	ChannelID   string
	ChannelName string
	DealType    string
	AddedBy     string
	AddedAt     time.Time
	// Retained for compatibility with the RFD processing interface and imports.
	SubscriptionType string
}

func (s *Subscription) IsRFD() bool { return s.SubscriptionType == "" || s.SubscriptionType == "rfd" }
