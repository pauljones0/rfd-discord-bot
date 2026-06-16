package models

import "time"

// Subscription represents a Discord server's channel subscription for deal notifications.
// It also contains the DealType to specify the filtering level for the channel.
type Subscription struct {
	GuildID          string    `docstore:"guildID" validate:"required"`
	ChannelID        string    `docstore:"channelID" validate:"required"`
	ChannelName      string    `docstore:"channelName"`                  // The name of the channel
	DealType         string    `docstore:"dealType" validate:"required"` // The type of deals to post in this channel
	AddedBy          string    `docstore:"addedBy"`
	AddedAt          time.Time `docstore:"addedAt"`
	SubscriptionType string    `docstore:"subscriptionType"` // values: "rfd", "ebay", "facebook", "memoryexpress", "bestbuy", "core", "oneverycorner"
	City             string    `docstore:"city,omitempty"`
	RadiusKm         int       `docstore:"radiusKm,omitempty"`
	FilterBrands     []string  `docstore:"filterBrands,omitempty"`
	StoreCode        string    `docstore:"storeCode,omitempty"` // Memory Express store code (e.g. "SKST")
}

// IsRFD returns true if this is an RFD subscription (default type).
func (s *Subscription) IsRFD() bool { return s.SubscriptionType == "" || s.SubscriptionType == "rfd" }

// IsEbay returns true if this is an eBay subscription.
func (s *Subscription) IsEbay() bool { return s.SubscriptionType == "ebay" }

// IsMemoryExpress returns true if this is a Memory Express subscription.
func (s *Subscription) IsMemoryExpress() bool { return s.SubscriptionType == "memoryexpress" }

// IsBestBuy returns true if this is a Best Buy subscription.
func (s *Subscription) IsBestBuy() bool { return s.SubscriptionType == "bestbuy" }

// IsCore returns true if this is a Core subscription.
func (s *Subscription) IsCore() bool { return s.SubscriptionType == "core" }

// IsOnEveryCorner returns true if this is an OnEveryCorner subscription.
func (s *Subscription) IsOnEveryCorner() bool { return s.SubscriptionType == "oneverycorner" }
