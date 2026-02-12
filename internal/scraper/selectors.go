package scraper

import (
	"encoding/json"
	"fmt"
	"os"
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

	return config, nil
}

// DefaultSelectors returns the fallback configuration if no JSON file is loaded.
// This is the single source of truth — the embedded selectors.json should be preferred.
func DefaultSelectors() SelectorConfig {
	return SelectorConfig{
		HotDealsList: ListSelectors{
			Container: ListContainer{
				Item:           "li.topic",
				IgnoreModifier: ".sticky",
			},
			Elements: ListElements{
				TitleLink:            ".thread_title_link",
				PostedTime:           ".thread_inner_footer .author_info time",
				AuthorLink:           ".thread_inner_footer .author_info .author",
				AuthorName:           ".author_name",
				ThreadImage:          ".thread_image img",
				LikeCount:            ".thread_inner_footer .votes",
				CommentCount:         ".thread_inner_footer .posts",
				CommentCountFallback: ".posts_count",
				ViewCount:            ".thread_inner_footer .views",
			},
		},
		DealDetails: DetailSelectors{
			PrimaryLink:  ".deal_link a",
			FallbackLink: ".postlink",
		},
	}
}
