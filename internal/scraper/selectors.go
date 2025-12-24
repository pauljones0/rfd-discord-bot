package scraper

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
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
	PostedTime           string `json:"posted_time"`
	AuthorLink           string `json:"author_link"`
	AuthorName           string `json:"author_name"`
	ThreadImage          string `json:"thread_image"`
	LikeCount            string `json:"like_count"`
	CommentCount         string `json:"comment_count"`
	CommentCountFallback string `json:"comment_count_fallback"`
	ViewCount            string `json:"view_count"`
}

type DetailSelectors struct {
	PrimaryLink  string `json:"primary_link"`
	FallbackLink string `json:"fallback_link"`
}

var (
	currentConfig *SelectorConfig
	configMutex   sync.RWMutex
)

// LoadSelectors loads the selector configuration from the specified JSON file.
// If the file cannot be read or parsed, it returns an error.
func LoadSelectors(path string) (*SelectorConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read selector config file: %w", err)
	}

	var config SelectorConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse selector config JSON: %w", err)
	}

	configMutex.Lock()
	currentConfig = &config
	configMutex.Unlock()

	return &config, nil
}

// GetCurrentSelectors returns the currently loaded selectors.
// It returns a default configuration if LoadSelectors hasn't been called successfully yet.
func GetCurrentSelectors() SelectorConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()

	if currentConfig != nil {
		return *currentConfig
	}

	// Fallback to hardcoded defaults if config isn't loaded
	return defaultSelectors
}

var defaultSelectors = SelectorConfig{
	HotDealsList: ListSelectors{
		Container: ListContainer{
			Item:           "li.topic",
			IgnoreModifier: ".sticky",
		},
		Elements: ListElements{
			TitleLink:            ".thread_title_link",
			PostedTime:           ".thread_outer_header .author_info time",
			AuthorLink:           ".thread_outer_header .author_info .author",
			AuthorName:           ".author_name",
			ThreadImage:          ".thread_image img",
			LikeCount:            ".votes",
			CommentCount:         ".posts",
			CommentCountFallback: ".posts_count",
			ViewCount:            ".views",
		},
	},
	DealDetails: DetailSelectors{
		PrimaryLink:  ".get-deal-button",
		FallbackLink: "a.autolinker_link:nth-child(1)",
	},
}
