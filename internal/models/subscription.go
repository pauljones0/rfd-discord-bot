package models

import "time"

// Subscription represents a Discord server's channel subscription for deal notifications.
// It also contains the DealType to specify the filtering level for the channel.
type Subscription struct {
	GuildID          string    `firestore:"guildID" validate:"required"`
	ChannelID        string    `firestore:"channelID" validate:"required"`
	ChannelName      string    `firestore:"channelName"`                  // The name of the channel
	DealType         string    `firestore:"dealType" validate:"required"` // The type of deals to post in this channel
	AddedBy          string    `firestore:"addedBy"`
	AddedAt          time.Time `firestore:"addedAt"`
	SubscriptionType string   `firestore:"subscriptionType"`             // values: "rfd", "ebay", "facebook"
	City             string   `firestore:"city,omitempty"`
	RadiusKm         int      `firestore:"radiusKm,omitempty"`
	FilterBrands     []string `firestore:"filterBrands,omitempty"`
	StoreCode        string   `firestore:"storeCode,omitempty"` // Memory Express store code (e.g. "SKST")
}

// IsFacebook returns true if this is a Facebook Marketplace subscription.
func (s *Subscription) IsFacebook() bool { return s.SubscriptionType == "facebook" }

// IsRFD returns true if this is an RFD subscription (default type).
func (s *Subscription) IsRFD() bool { return s.SubscriptionType == "" || s.SubscriptionType == "rfd" }

// IsEbay returns true if this is an eBay subscription.
func (s *Subscription) IsEbay() bool { return s.SubscriptionType == "ebay" }

// IsMemoryExpress returns true if this is a Memory Express subscription.
func (s *Subscription) IsMemoryExpress() bool { return s.SubscriptionType == "memoryexpress" }
