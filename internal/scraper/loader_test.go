package scraper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitSelectorsOverrideAndInvalidConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selectors.json")
	selectors := DefaultSelectors()
	selectors.HotDealsList.Container.Item = ".new-rfd-layout"
	data, err := json.Marshal(selectors)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SELECTORS_CONFIG_PATH", path)
	got, err := LoadConfig()
	if err != nil || got.HotDealsList.Container.Item != ".new-rfd-layout" {
		t.Fatalf("explicit override ignored: %+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("invalid explicit config must fail visibly")
	}
	t.Setenv("SELECTORS_CONFIG_PATH", "")
	if got, err := LoadConfig(); err != nil || got != DefaultSelectors() {
		t.Fatalf("embedded defaults: %+v %v", got, err)
	}
}

func TestExplicitSelectorsOverrideRejectsInvalidCSS(t *testing.T) {
	path := filepath.Join(t.TempDir(), "selectors.json")
	t.Setenv("SELECTORS_CONFIG_PATH", path)
	for _, tc := range []struct {
		name   string
		change func(*SelectorConfig)
	}{
		{"hot_deals_list.elements.title_link", func(c *SelectorConfig) { c.HotDealsList.Elements.TitleLink = "[" }},
		{"deal_details.fallback_link", func(c *SelectorConfig) { c.DealDetails.FallbackLink = "[" }},
		{"hot_deals_list.container.item", func(c *SelectorConfig) { c.HotDealsList.Container.Item = "   " }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectors := DefaultSelectors()
			tc.change(&selectors)
			data, err := json.Marshal(selectors)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("invalid override must identify selector at startup: %v", err)
			}
		})
	}
}

func TestSelectorsAllowBlankOptionalFieldsAndDefaultHasSelector(t *testing.T) {
	selectors := DefaultSelectors()
	selectors.DealDetails = DetailSelectors{}
	selectors.HotDealsList.Elements = ListElements{TitleLink: "a", PostedTime: "time"}
	if err := selectors.Validate(); err != nil {
		t.Fatalf("optional empty fields and default :has selector must be supported: %v", err)
	}
}
