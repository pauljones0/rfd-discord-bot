package scraper

import (
	"encoding/json"
	"os"
	"path/filepath"
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
