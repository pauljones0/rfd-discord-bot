package facebook

import (
	"fmt"
	"log/slog"
	"math/rand"
	"strings"

	"github.com/playwright-community/playwright-go"
)

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:131.0) Gecko/20100101 Firefox/131.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:130.0) Gecko/20100101 Firefox/130.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 14.7; rv:131.0) Gecko/20100101 Firefox/131.0",
	"Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0",
	"Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
}

var viewports = []playwright.Size{
	{Width: 1920, Height: 1080},
	{Width: 1366, Height: 768},
	{Width: 1536, Height: 864},
	{Width: 1440, Height: 900},
	{Width: 1680, Height: 1050},
	{Width: 1280, Height: 720},
}

// BrowserManager wraps a Playwright browser instance with stealth configuration.
type BrowserManager struct {
	pw       *playwright.Playwright
	browser  playwright.Browser
	logger   *slog.Logger
	proxyURL string
}

// NewBrowserManager creates a new stealth-configured Playwright browser manager.
func NewBrowserManager(logger *slog.Logger, proxyURL string) (*BrowserManager, error) {
	if proxyURL != "" {
		logger.Info("Initializing Playwright with proxy support", "proxy", MaskProxyURL(proxyURL))
	} else {
		logger.Info("Initializing Playwright...")
	}

	err := playwright.Install()
	if err != nil {
		logger.Warn("playwright install returned (can usually be ignored if already installed)", "error", err)
	}

	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("could not start playwright: %w", err)
	}

	launchOpts := playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
		Args: []string{
			"--no-sandbox",
			"--disable-dev-shm-usage",
		},
		FirefoxUserPrefs: map[string]interface{}{
			"dom.webdriver.enabled":        false,
			"useAutomationExtension":       false,
			"privacy.resistFingerprinting": true,
			"webgl.disabled":               true,
			"media.peerconnection.enabled": false,
			"intl.accept_languages":        "en-US,en;q=0.9",
		},
	}

	logger.Info("Launching headless Firefox with stealth preferences")
	browser, err := pw.Firefox.Launch(launchOpts)
	if err != nil {
		pw.Stop()
		return nil, fmt.Errorf("could not launch firefox: %w", err)
	}

	return &BrowserManager{
		pw:       pw,
		browser:  browser,
		logger:   logger,
		proxyURL: proxyURL,
	}, nil
}

// NewContext creates a new browser context with a randomized desktop viewport and User Agent.
func (m *BrowserManager) NewContext() (playwright.BrowserContext, error) {
	ua := userAgents[rand.Intn(len(userAgents))]
	vp := viewports[rand.Intn(len(viewports))]

	opts := playwright.BrowserNewContextOptions{
		Viewport:  &vp,
		UserAgent: playwright.String(ua),
	}

	if m.proxyURL != "" {
		m.logger.Debug("Applying proxy to new browser context")
		opts.Proxy = &playwright.Proxy{
			Server: m.proxyURL,
		}
	}

	return m.browser.NewContext(opts)
}

// MaskProxyURL redacts credentials from a proxy URL for safe logging.
func MaskProxyURL(raw string) string {
	if idx := strings.Index(raw, "://"); idx != -1 {
		afterScheme := raw[idx+3:]
		if atIdx := strings.LastIndex(afterScheme, "@"); atIdx != -1 {
			return raw[:idx+3] + "***:***@" + afterScheme[atIdx+1:]
		}
	}
	return raw
}

// Close shuts down the browser and Playwright runtime.
func (m *BrowserManager) Close() error {
	m.logger.Info("Shutting down Playwright...")
	var err error
	if m.browser != nil {
		err = m.browser.Close()
	}
	if m.pw != nil {
		if stopErr := m.pw.Stop(); stopErr != nil {
			err = stopErr
		}
	}
	return err
}
