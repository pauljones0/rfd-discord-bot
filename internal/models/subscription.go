package models

import "time"

// Subscription represents a Discord server's channel subscription for deal notifications.
type Subscription struct {
	GuildID   string    `firestore:"guildID" validate:"required"`
	ChannelID string    `firestore:"channelID" validate:"required"`
	AddedBy   string    `firestore:"addedBy"`
	AddedAt   time.Time `firestore:"addedAt"`
}
