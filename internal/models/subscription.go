package models

import "time"

// Subscription represents a Discord server's channel subscription for deal notifications.
// It also contains the DealType to specify the filtering level for the channel.
type Subscription struct {
	GuildID     string    `firestore:"guildID" validate:"required"`
	ChannelID   string    `firestore:"channelID" validate:"required"`
	ChannelName string    `firestore:"channelName"`                  // The name of the channel
	DealType    string    `firestore:"dealType" validate:"required"` // The type of deals to post in this channel
	AddedBy     string    `firestore:"addedBy"`
	AddedAt     time.Time `firestore:"addedAt"`
}
