package scraper

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/andybalholm/cascadia"
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

// Validate checks required fields and compiles every configured CSS selector.
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
	selectors := []struct{ name, value string }{
		{"hot_deals_list.container.item", c.HotDealsList.Container.Item},
		{"hot_deals_list.container.ignore_modifier", c.HotDealsList.Container.IgnoreModifier},
		{"hot_deals_list.elements.title_link", c.HotDealsList.Elements.TitleLink},
		{"hot_deals_list.elements.title_text", c.HotDealsList.Elements.TitleText},
		{"hot_deals_list.elements.retailer", c.HotDealsList.Elements.Retailer},
		{"hot_deals_list.elements.posted_time", c.HotDealsList.Elements.PostedTime},
		{"hot_deals_list.elements.thread_image", c.HotDealsList.Elements.ThreadImage},
		{"hot_deals_list.elements.like_count", c.HotDealsList.Elements.LikeCount},
		{"hot_deals_list.elements.comment_count", c.HotDealsList.Elements.CommentCount},
		{"hot_deals_list.elements.comment_count_fallback", c.HotDealsList.Elements.CommentCountFallback},
		{"hot_deals_list.elements.view_count", c.HotDealsList.Elements.ViewCount},
		{"deal_details.primary_link", c.DealDetails.PrimaryLink},
		{"deal_details.fallback_link", c.DealDetails.FallbackLink},
		{"deal_details.category", c.DealDetails.Category},
	}
	for _, selector := range selectors {
		if selector.value == "" {
			continue
		}
		if _, err := cascadia.Compile(selector.value); err != nil {
			return fmt.Errorf("invalid CSS selector %s: %w", selector.name, err)
		}
	}
	return nil
}
