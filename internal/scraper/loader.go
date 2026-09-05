package scraper

import (
	_ "embed"
	"os"
	"strings"
)

//go:embed selectors.json
var embeddedSelectors []byte

// LoadConfig uses an explicit override when supplied. An invalid override is an
// error, rather than silently scraping with a different configuration.
func LoadConfig() (SelectorConfig, error) {
	if path := strings.TrimSpace(os.Getenv("SELECTORS_CONFIG_PATH")); path != "" {
		return LoadSelectors(path)
	}
	return LoadSelectorsFromBytes(embeddedSelectors)
}

// DefaultSelectors is the compiled configuration, also used by local fixtures.
func DefaultSelectors() SelectorConfig {
	selectors, err := LoadSelectorsFromBytes(embeddedSelectors)
	if err != nil {
		panic("invalid embedded RFD selectors: " + err.Error())
	}
	return selectors
}
