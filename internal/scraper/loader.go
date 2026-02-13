package scraper

import (
	"embed"
	"log/slog"
	"os"
)

//go:embed selectors.json
var embeddedSelectors embed.FS

// LoadConfig tries to load selectors in the following order:
// 1. Embedded selectors.json
// 2. External file defined by SELECTORS_CONFIG_PATH (or default "config/selectors.json")
// 3. Hardcoded defaults (if all else fails, though this function returns error in that case, caller handles fallback)
func LoadConfig() (SelectorConfig, error) {
	// 1. Try embedded
	data, err := embeddedSelectors.ReadFile("selectors.json")
	if err == nil {
		sel, parseErr := LoadSelectorsFromBytes(data)
		if parseErr == nil {
			slog.Info("Loaded selectors from embedded config.")
			return sel, nil
		}
		slog.Warn("Embedded selectors failed to parse. Trying file fallback.", "error", parseErr)
	}

	// 2. Fallback to external file
	configPath := os.Getenv("SELECTORS_CONFIG_PATH")
	if configPath == "" {
		configPath = "config/selectors.json"
	}

	// Try loading from file
	if fileSel, err := LoadSelectors(configPath); err == nil {
		slog.Info("Loaded selectors from external file", "path", configPath)
		return fileSel, nil
	} else {
		slog.Warn("Failed to load external selectors, falling back to defaults", "path", configPath, "error", err)
	}

	// 3. Fallback to hardcoded defaults
	slog.Info("Using hardcoded default selectors")
	return DefaultSelectors(), nil
}
