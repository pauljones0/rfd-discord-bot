package facebook

import (
	"fmt"
	"log/slog"
	"math/rand"
	"strings"

	"github.com/playwright-community/playwright-go"
)

// browserProfile bundles a user agent with a matching platform and device pixel ratio
// so that the UA, viewport, and navigator properties tell a consistent story.
type browserProfile struct {
	userAgent  string
	platform   string
	deviceRatio float64
	// macOS reports different screen sizes and OS versions
	isMac bool
}

// profiles covers the three major desktop OS families with current Firefox versions.
// We rotate across versions 146-148 (the three most recent stable releases) so we
// never look like we're running an ancient or bleeding-edge build.
var profiles = []browserProfile{
	// Windows 10/11 — most common desktop OS
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0", platform: "Win32", deviceRatio: 1.0},
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0", platform: "Win32", deviceRatio: 1.25},
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:147.0) Gecko/20100101 Firefox/147.0", platform: "Win32", deviceRatio: 1.5},
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:147.0) Gecko/20100101 Firefox/147.0", platform: "Win32", deviceRatio: 1.0},
	{userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:146.0) Gecko/20100101 Firefox/146.0", platform: "Win32", deviceRatio: 1.0},

	// macOS — second most common
	{userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 15.3; rv:148.0) Gecko/20100101 Firefox/148.0", platform: "MacIntel", deviceRatio: 2.0, isMac: true},
	{userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14.7; rv:148.0) Gecko/20100101 Firefox/148.0", platform: "MacIntel", deviceRatio: 2.0, isMac: true},
	{userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 15.3; rv:147.0) Gecko/20100101 Firefox/147.0", platform: "MacIntel", deviceRatio: 2.0, isMac: true},

	// Linux — less common but realistic
	{userAgent: "Mozilla/5.0 (X11; Linux x86_64; rv:148.0) Gecko/20100101 Firefox/148.0", platform: "Linux x86_64", deviceRatio: 1.0},
	{userAgent: "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:147.0) Gecko/20100101 Firefox/147.0", platform: "Linux x86_64", deviceRatio: 1.0},
}

// viewports for Windows/Linux — common desktop monitor resolutions.
var desktopViewports = []playwright.Size{
	{Width: 1920, Height: 1080},
	{Width: 1366, Height: 768},
	{Width: 1536, Height: 864},
	{Width: 1440, Height: 900},
	{Width: 1680, Height: 1050},
	{Width: 1280, Height: 720},
	{Width: 1600, Height: 900},
	{Width: 2560, Height: 1440},
}

// viewports for macOS — Retina-native logical sizes.
var macViewports = []playwright.Size{
	{Width: 1440, Height: 900},
	{Width: 1680, Height: 1050},
	{Width: 1280, Height: 800},
	{Width: 1512, Height: 982},
	{Width: 1728, Height: 1117},
}

// jsStealthOverrides is injected into every page before any other script runs.
// It patches the navigator and window objects to look like a real browser session.
const jsStealthOverrides = `() => {
	// Hide webdriver flag
	Object.defineProperty(navigator, 'webdriver', { get: () => undefined });

	// Spoof hardwareConcurrency to a realistic value (real machines have 4-16)
	const cores = [4, 6, 8, 12, 16];
	Object.defineProperty(navigator, 'hardwareConcurrency', {
		get: () => cores[Math.floor(Math.random() * cores.length)]
	});

	// Spoof deviceMemory (Chrome-only but some fingerprinters check it)
	Object.defineProperty(navigator, 'deviceMemory', {
		get: () => [4, 8, 16][Math.floor(Math.random() * 3)]
	});

	// Spoof plugins — real Firefox has a small set
	Object.defineProperty(navigator, 'plugins', {
		get: () => [
			{ name: "PDF Viewer", filename: "internal-pdf-viewer", description: "Portable Document Format" },
			{ name: "Chrome PDF Viewer", filename: "internal-pdf-viewer", description: "" },
			{ name: "Chromium PDF Viewer", filename: "internal-pdf-viewer", description: "" },
		]
	});

	// Spoof languages to match the Accept-Language header
	Object.defineProperty(navigator, 'languages', {
		get: () => ['en-US', 'en']
	});

	// Prevent headless detection via missing Notification permission
	const originalQuery = window.Notification && Notification.permission;
	if (originalQuery === 'denied' || !originalQuery) {
		try {
			Object.defineProperty(Notification, 'permission', { get: () => 'default' });
		} catch(e) {}
	}

	// Prevent detection via chrome.runtime (some fingerprinters look for this)
	if (!window.chrome) {
		window.chrome = { runtime: {} };
	}

	// --- Additional stealth patches from playwright-stealth ---

	// WebGL vendor/renderer spoofing — headless browsers expose "Google Inc." / "ANGLE"
	// which is a dead giveaway. Override to return realistic GPU strings.
	const getParameter = WebGLRenderingContext.prototype.getParameter;
	WebGLRenderingContext.prototype.getParameter = function(param) {
		// UNMASKED_VENDOR_WEBGL = 0x9245, UNMASKED_RENDERER_WEBGL = 0x9246
		if (param === 0x9245) return 'Intel Inc.';
		if (param === 0x9246) return 'Intel Iris OpenGL Engine';
		return getParameter.call(this, param);
	};
	if (typeof WebGL2RenderingContext !== 'undefined') {
		const getParameter2 = WebGL2RenderingContext.prototype.getParameter;
		WebGL2RenderingContext.prototype.getParameter = function(param) {
			if (param === 0x9245) return 'Intel Inc.';
			if (param === 0x9246) return 'Intel Iris OpenGL Engine';
			return getParameter2.call(this, param);
		};
	}

	// Permissions API override — automated browsers return different results for
	// navigator.permissions.query({name: 'notifications'}) which fingerprinters check.
	if (navigator.permissions) {
		const origQuery = navigator.permissions.query.bind(navigator.permissions);
		navigator.permissions.query = function(params) {
			if (params.name === 'notifications') {
				return Promise.resolve({ state: Notification.permission });
			}
			return origQuery(params);
		};
	}

	// Media codecs spoofing — headless can report different codec support.
	// Ensure common codecs report as supported (matches real Firefox behavior).
	if (typeof MediaSource !== 'undefined' && MediaSource.isTypeSupported) {
		const origIsTypeSupported = MediaSource.isTypeSupported.bind(MediaSource);
		MediaSource.isTypeSupported = function(type) {
			// Common codecs that real browsers support
			if (type.includes('avc1') || type.includes('mp4a') || type.includes('vp9') || type.includes('opus')) {
				return true;
			}
			return origIsTypeSupported(type);
		};
	}

	// chrome.csi and chrome.loadTimes stubs — some fingerprinters specifically
	// check for the presence of these Chrome-specific timing APIs.
	if (window.chrome) {
		window.chrome.csi = function() {
			return {
				startE: Date.now(),
				onloadT: Date.now(),
				pageT: Math.random() * 1000 + 100,
				tran: 15
			};
		};
		window.chrome.loadTimes = function() {
			return {
				requestTime: Date.now() / 1000,
				startLoadTime: Date.now() / 1000,
				commitLoadTime: Date.now() / 1000 + 0.1,
				finishDocumentLoadTime: Date.now() / 1000 + 0.3,
				finishLoadTime: Date.now() / 1000 + 0.5,
				firstPaintTime: Date.now() / 1000 + 0.15,
				firstPaintAfterLoadTime: 0,
				navigationType: 'Other',
				wasFetchedViaSpdy: false,
				wasNpnNegotiated: true,
				npnNegotiatedProtocol: 'h2',
				wasAlternateProtocolAvailable: false,
				connectionInfo: 'h2'
			};
		};
	}
}`

// blockedExtensions lists resource file extensions to block for bandwidth savings.
// Only raw HTML and JavaScript are allowed through — everything else is noise.
var blockedExtensions = []string{
	".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".ico", ".bmp", ".tiff",
	".mp4", ".webm", ".mp3", ".ogg", ".avi", ".mov", ".flv",
	".woff", ".woff2", ".ttf", ".eot", ".otf",
	".css",
}

// blockedDomains lists ad/tracking domains whose requests are aborted outright.
var blockedDomains = []string{
	"doubleclick.net",
	"google-analytics.com",
	"googletagmanager.com",
	"googlesyndication.com",
	"googleadservices.com",
	"facebook.com/tr",
	"connect.facebook.net/en_US/fbevents",
	"ads.linkedin.com",
	"analytics.tiktok.com",
}

// BrowserManager wraps a Playwright browser instance with stealth configuration.
type BrowserManager struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	logger  *slog.Logger
}

// NewBrowserManager creates a new stealth-configured Playwright browser manager.
func NewBrowserManager(logger *slog.Logger) (*BrowserManager, error) {
	logger.Info("Initializing Playwright (no proxy)")

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
			// Core anti-detection
			"dom.webdriver.enabled":  false,
			"useAutomationExtension": false,

			// Disable WebRTC to prevent IP leaking
			"media.peerconnection.enabled":                  false,
			"media.navigator.enabled":                       false,
			"media.peerconnection.ice.default_address_only": true,

			// Language/locale — Canadian English
			"intl.accept_languages":    "en-CA,en-US;q=0.9,en;q=0.8",
			"general.useragent.locale": "en-CA",

			// Disable telemetry and beacon (reduces noise, looks more like a privacy-aware user)
			"toolkit.telemetry.enabled": false,
			"beacon.enabled":            false,
			"dom.battery.enabled":       false,
			"dom.gamepad.enabled":       false,

			// Canvas/WebGL fingerprinting mitigation
			"webgl.disabled":                    false,
			"webgl.enable-debug-renderer-info":  false, // Hides GPU model string
			"privacy.resistFingerprinting":      false, // OFF — too detectable on its own
			"privacy.trackingprotection.enabled": false, // Don't block FB tracking scripts (they check)

			// Font fingerprinting — limit to system fonts so enumeration returns less data
			"browser.display.use_document_fonts": 1,

			// Disable service workers — marketplace doesn't need them and they leak info
			"dom.serviceWorkers.enabled": false,
		},
	}

	logger.Info("Launching headless Firefox with stealth preferences")
	browser, err := pw.Firefox.Launch(launchOpts)
	if err != nil {
		pw.Stop()
		return nil, fmt.Errorf("could not launch firefox: %w", err)
	}

	return &BrowserManager{
		pw:      pw,
		browser: browser,
		logger:  logger,
	}, nil
}

// NewContext creates a new browser context with a randomized but internally consistent
// fingerprint — the UA, viewport, device pixel ratio, locale, and timezone all match
// what a real Canadian user on that OS would produce.
// Request interception is set up to block images, videos, fonts, CSS, and ad-tracking
// domains for bandwidth savings.
func (m *BrowserManager) NewContext() (playwright.BrowserContext, error) {
	profile := profiles[rand.Intn(len(profiles))]

	// Pick a viewport that matches the OS
	var vp playwright.Size
	if profile.isMac {
		vp = macViewports[rand.Intn(len(macViewports))]
	} else {
		vp = desktopViewports[rand.Intn(len(desktopViewports))]
	}

	// Randomize Canadian timezone (most population is in Eastern/Central/Pacific)
	timezones := []string{"America/Toronto", "America/Vancouver", "America/Edmonton", "America/Winnipeg", "America/Halifax"}
	tz := timezones[rand.Intn(len(timezones))]

	opts := playwright.BrowserNewContextOptions{
		Viewport:          &vp,
		UserAgent:         playwright.String(profile.userAgent),
		DeviceScaleFactor: playwright.Float(profile.deviceRatio),
		Locale:            playwright.String("en-CA"),
		TimezoneId:        playwright.String(tz),
		// Screen size should be >= viewport — set it to a common monitor size
		Screen: &playwright.Size{Width: vp.Width, Height: vp.Height + 120}, // +120 for taskbar/dock
		// Color scheme — most users are on light mode
		ColorScheme: playwright.ColorSchemeDark,
	}

	// Randomly pick light or dark mode (70/30 split matching real usage)
	if rand.Float64() < 0.7 {
		opts.ColorScheme = playwright.ColorSchemeLight
	}

	ctx, err := m.browser.NewContext(opts)
	if err != nil {
		return nil, err
	}

	// Inject stealth overrides before any page script runs
	err = ctx.AddInitScript(playwright.Script{Content: playwright.String(jsStealthOverrides)})
	if err != nil {
		m.logger.Warn("Failed to inject stealth script", "error", err)
	}

	// Block images, videos, fonts, CSS, and ad-tracking to save bandwidth
	if err := setupRequestInterception(ctx); err != nil {
		m.logger.Warn("Failed to set up request interception", "error", err)
	}

	return ctx, nil
}

// setupRequestInterception installs a route handler that aborts requests for
// heavy resource types (images, video, fonts, CSS) and known ad/tracking domains.
// Only HTML and JavaScript pass through — everything the scraper actually needs.
func setupRequestInterception(ctx playwright.BrowserContext) error {
	return ctx.Route("**/*", func(route playwright.Route) {
		req := route.Request()
		rawURL := req.URL()
		lowerURL := strings.ToLower(rawURL)

		// Strip query string for extension matching
		urlPath := lowerURL
		if qIdx := strings.Index(urlPath, "?"); qIdx != -1 {
			urlPath = urlPath[:qIdx]
		}

		// Block by file extension
		for _, ext := range blockedExtensions {
			if strings.HasSuffix(urlPath, ext) {
				_ = route.Abort()
				return
			}
		}

		// Block by ad/tracking domain
		for _, domain := range blockedDomains {
			if strings.Contains(lowerURL, domain) {
				_ = route.Abort()
				return
			}
		}

		_ = route.Continue()
	})
}

// SimulateHumanBehavior performs random mouse movements and a scroll on the page
// to mimic human interaction patterns. Anti-bot systems like Facebook's track
// whether a session has any mouse/scroll events — a session with zero interaction
// before JS extraction is a strong signal of automation.
func SimulateHumanBehavior(page playwright.Page) {
	vp := page.ViewportSize()
	if vp == nil {
		return
	}

	// 2-4 random mouse movements across the page
	moves := 2 + rand.Intn(3)
	for i := 0; i < moves; i++ {
		x := float64(100 + rand.Intn(vp.Width-200))
		y := float64(100 + rand.Intn(vp.Height-200))
		_ = page.Mouse().Move(x, y, playwright.MouseMoveOptions{
			Steps: playwright.Int(5 + rand.Intn(10)),
		})
	}

	// Scroll down a bit (100-400px) then back up slightly, like a human scanning
	scrollDown := 100 + rand.Intn(300)
	_ = page.Mouse().Wheel(0, float64(scrollDown))
	scrollUp := 30 + rand.Intn(70)
	_ = page.Mouse().Wheel(0, float64(-scrollUp))
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
