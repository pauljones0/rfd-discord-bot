package scraper

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type SelectorConfig struct {
	HotDealsList ListSelectors   `json:"hot_deals_list"`
	DealDetails  DetailSelectors `json:"deal_details"`
}

type ListSelectors struct {
	Container ListContainer `json:"container"`
	Elements  ListElements  `json:"elements"`
}

type ListContainer struct {
	Item           string `json:"item"`            // e.g., "li.topic"
	IgnoreModifier string `json:"ignore_modifier"` // e.g., ".sticky"
}

type ListElements struct {
	TitleLink            string `json:"title_link"`
	TitleText            string `json:"title_text"`
	Retailer             string `json:"retailer"`
	PostedTime           string `json:"posted_time"`
	ThreadImage          string `json:"thread_image"`
	LikeCount            string `json:"like_count"`
	CommentCount         string `json:"comment_count"`
	CommentCountFallback string `json:"comment_count_fallback"`
	ViewCount            string `json:"view_count"`
}

type DetailSelectors struct {
	PrimaryLink  string `json:"primary_link"`
	FallbackLink string `json:"fallback_link"`
	Category     string `json:"category"`
}

// LoadSelectors loads the selector configuration from the specified JSON file.
func LoadSelectors(path string) (SelectorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SelectorConfig{}, fmt.Errorf("failed to read selector config file: %w", err)
	}

	return LoadSelectorsFromBytes(data)
}

// LoadSelectorsFromBytes parses selector configuration from raw JSON bytes.
// This supports loading from embedded data via go:embed.
func LoadSelectorsFromBytes(data []byte) (SelectorConfig, error) {
	var config SelectorConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return SelectorConfig{}, fmt.Errorf("failed to parse selector config JSON: %w", err)
	}

	if err := config.Validate(); err != nil {
		return SelectorConfig{}, fmt.Errorf("invalid selector config: %w", err)
	}

	return config, nil
}

// Validate checks that critical selector fields are non-empty.
func (c SelectorConfig) Validate() error {
	var missing []string
	if c.HotDealsList.Container.Item == "" {
		missing = append(missing, "hot_deals_list.container.item")
	}
	if c.HotDealsList.Elements.TitleLink == "" {
		missing = append(missing, "hot_deals_list.elements.title_link")
	}
	if c.HotDealsList.Elements.PostedTime == "" {
		missing = append(missing, "hot_deals_list.elements.posted_time")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required selectors: %s", strings.Join(missing, ", "))
	}
	return nil
}

// DefaultSelectors returns the fallback configuration if no JSON file is loaded.
// This is the single source of truth — the embedded selectors.json should be preferred.
func DefaultSelectors() SelectorConfig {
	return SelectorConfig{
		HotDealsList: ListSelectors{
			Container: ListContainer{
				Item:           "li.topic-card.topic",
				IgnoreModifier: ".sticky, :has(.sponsored-offer)",
			},
			Elements: ListElements{
				TitleLink:            "a.topic-card-info.thread_info",
				TitleText:            ".thread_title",
				Retailer:             ".thread_dealer",
				PostedTime:           "time.topic_time",
				ThreadImage:          ".thread_image img",
				LikeCount:            ".thread_extra_info .votes",
				CommentCount:         ".thread_extra_info .posts",
				CommentCountFallback: ".posts_count",
				ViewCount:            ".thread_extra_info .views",
			},
		},
		DealDetails: DetailSelectors{
			PrimaryLink:  ".deal_link a",
			FallbackLink: ".postlink",
			Category:     ".thread_category",
		},
	}
}
