package memoryexpress

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const challengePollInterval = 5 * time.Second

// LocalBrowserSessionOptions configures the persistent local Chrome session.
type LocalBrowserSessionOptions struct {
	ChromePath    string
	ChromeProfile string
	Alerter       Alerter
}

type storeTab struct {
	ctx             context.Context
	cancel          context.CancelFunc
	challengeActive bool
}

// LocalBrowserSession keeps one headed Chrome instance alive with one tab per subscribed store.
type LocalBrowserSession struct {
	browserCtx    context.Context
	browserCancel context.CancelFunc
	allocCancel   context.CancelFunc
	alerter       Alerter
	rootTab       *storeTab
	rootStore     string
	tabs          map[string]*storeTab
}

// NewLocalBrowserSession launches a persistent headed Chrome session for local scraping.
func NewLocalBrowserSession(parent context.Context, opts LocalBrowserSessionOptions) (*LocalBrowserSession, error) {
	chromePath := opts.ChromePath
	if chromePath == "" {
		chromePath = findLocalChromeExecutable()
	}

	chromeProfile := opts.ChromeProfile
	if chromeProfile == "" {
		var err error
		chromeProfile, err = defaultChromeProfileDir()
		if err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(chromeProfile, 0o755); err != nil {
		return nil, fmt.Errorf("create Chrome profile dir: %w", err)
	}

	alert := opts.Alerter
	if alert == nil {
		alert = DesktopAlerter{}
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(
		parent,
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(chromePath),
			chromedp.UserDataDir(chromeProfile),
			chromedp.Flag("headless", false),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
			chromedp.Flag("hide-crash-restore-bubble", true),
			chromedp.Flag("disable-session-crashed-bubble", true),
			chromedp.Flag("disable-background-timer-throttling", true),
			chromedp.Flag("disable-backgrounding-occluded-windows", true),
			chromedp.Flag("disable-renderer-backgrounding", true),
			chromedp.Flag("disable-hang-monitor", true),
			chromedp.WindowSize(1600, 1000),
		)...,
	)

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(browserCtx, chromedp.Navigate("about:blank")); err != nil {
		browserCancel()
		allocCancel()
		return nil, fmt.Errorf("launch local Chrome session: %w", err)
	}

	session := &LocalBrowserSession{
		browserCtx:    browserCtx,
		browserCancel: browserCancel,
		allocCancel:   allocCancel,
		alerter:       alert,
		rootTab:       &storeTab{ctx: browserCtx},
		tabs:          make(map[string]*storeTab),
	}

	slog.Info("Memory Express local browser session started",
		"processor", "memoryexpress",
		"chrome_path", chromePath,
		"chrome_profile", chromeProfile,
	)

	return session, nil
}

// Close shuts down the local browser session and any open tabs.
func (s *LocalBrowserSession) Close() {
	for _, tab := range s.tabs {
		if tab.cancel != nil {
			tab.cancel()
		}
	}
	s.browserCancel()
	s.allocCancel()
}

// SyncStores keeps exactly one browser tab open for each subscribed store.
func (s *LocalBrowserSession) SyncStores(ctx context.Context, storeCodes []string) error {
	desired := normalizeStoreCodes(storeCodes)
	desiredSet := make(map[string]struct{}, len(desired))
	for _, code := range desired {
		desiredSet[code] = struct{}{}
	}

	var toOpen []string
	var toClose []string
	resetRoot := false

	if s.rootStore != "" {
		if _, ok := desiredSet[s.rootStore]; !ok {
			delete(s.tabs, s.rootStore)
			s.rootStore = ""

			for _, code := range desired {
				tab, ok := s.tabs[code]
				if !ok || tab.cancel == nil {
					continue
				}

				tab.cancel()
				delete(s.tabs, code)
				toClose = append(toClose, code)
				s.rootStore = code
				s.tabs[code] = s.rootTab
				toOpen = append(toOpen, code)
				break
			}

			if s.rootStore == "" {
				resetRoot = true
			}
		}
	}

	for code, tab := range s.tabs {
		if code == s.rootStore {
			continue
		}
		if _, ok := desiredSet[code]; ok {
			continue
		}
		if tab.cancel != nil {
			tab.cancel()
		}
		delete(s.tabs, code)
		toClose = append(toClose, code)
	}

	if s.rootStore == "" && len(desired) > 0 {
		s.rootStore = desired[0]
		s.tabs[s.rootStore] = s.rootTab
		toOpen = append(toOpen, s.rootStore)
		resetRoot = false
	}

	for _, code := range desired {
		if _, ok := s.tabs[code]; ok {
			continue
		}
		tabCtx, tabCancel := chromedp.NewContext(s.browserCtx)
		s.tabs[code] = &storeTab{ctx: tabCtx, cancel: tabCancel}
		toOpen = append(toOpen, code)
	}

	if resetRoot {
		if err := s.resetRootTab(); err != nil {
			return fmt.Errorf("reset root tab: %w", err)
		}
	}

	for _, code := range toOpen {
		if err := s.navigateStoreTab(ctx, code); err != nil {
			if s.tabs[code].cancel != nil {
				s.tabs[code].cancel()
			}
			delete(s.tabs, code)
			if code == s.rootStore {
				s.rootStore = ""
			}
			return fmt.Errorf("open store tab for %s: %w", code, err)
		}
	}

	slices.Sort(toClose)
	slices.Sort(toOpen)

	if len(toOpen) > 0 || len(toClose) > 0 {
		slog.Info("Memory Express local browser tabs updated",
			"processor", "memoryexpress",
			"opened", toOpen,
			"closed", toClose,
		)
	}

	return nil
}

func (s *LocalBrowserSession) resetRootTab() error {
	return chromedp.Run(s.rootTab.ctx,
		chromedp.Navigate("about:blank"),
		chromedp.WaitReady("body", chromedp.ByQuery),
	)
}

// ScrapeStore scrapes a store from its persistent local browser tab.
func (s *LocalBrowserSession) ScrapeStore(ctx context.Context, storeCode string) ([]Product, error) {
	tab, ok := s.tabs[storeCode]
	if !ok {
		if err := s.SyncStores(ctx, []string{storeCode}); err != nil {
			return nil, err
		}
		tab = s.tabs[storeCode]
	}

	targetURL, err := ClearanceURL(storeCode)
	if err != nil {
		return nil, err
	}

	challenged, currentURL, err := s.pageState(tab.ctx)
	if err != nil {
		if err := s.navigateStoreTab(ctx, storeCode); err != nil {
			return nil, fmt.Errorf("navigate store tab for %s: %w", storeCode, err)
		}
	} else if challenged {
		slog.Warn("Memory Express tab is waiting on Cloudflare challenge",
			"processor", "memoryexpress",
			"store", storeCode,
			"url", currentURL,
		)
	} else if currentURL != targetURL {
		if err := s.navigateStoreTab(ctx, storeCode); err != nil {
			return nil, fmt.Errorf("navigate store tab for %s: %w", storeCode, err)
		}
	} else if err := s.reloadStoreTab(ctx, storeCode); err != nil {
		return nil, fmt.Errorf("reload store tab for %s: %w", storeCode, err)
	}

	html, err := s.waitForReadyHTML(ctx, storeCode, tab)
	if err != nil {
		return nil, err
	}

	return ParseClearanceHTML(storeCode, html)
}

func (s *LocalBrowserSession) navigateStoreTab(ctx context.Context, storeCode string) error {
	tab, ok := s.tabs[storeCode]
	if !ok {
		return fmt.Errorf("store tab not found for %s", storeCode)
	}

	targetURL, err := ClearanceURL(storeCode)
	if err != nil {
		return err
	}

	if err := chromedp.Run(tab.ctx,
		chromedp.Navigate(targetURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return err
	}

	return nil
}

func (s *LocalBrowserSession) reloadStoreTab(ctx context.Context, storeCode string) error {
	tab, ok := s.tabs[storeCode]
	if !ok {
		return fmt.Errorf("store tab not found for %s", storeCode)
	}

	if err := chromedp.Run(tab.ctx,
		chromedp.Reload(),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return err
	}

	return nil
}

func (s *LocalBrowserSession) pageState(tabCtx context.Context) (bool, string, error) {
	var html string
	var currentURL string
	if err := chromedp.Run(tabCtx,
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
		chromedp.Location(&currentURL),
	); err != nil {
		return false, "", err
	}

	return hasCloudflareChallenge(html), currentURL, nil
}

func (s *LocalBrowserSession) waitForReadyHTML(ctx context.Context, storeCode string, tab *storeTab) (string, error) {
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		var html string
		if err := chromedp.Run(tab.ctx, chromedp.OuterHTML("html", &html, chromedp.ByQuery)); err != nil {
			return "", fmt.Errorf("read browser HTML for %s: %w", storeCode, err)
		}

		if !hasCloudflareChallenge(html) {
			if tab.challengeActive {
				slog.Info("Memory Express Cloudflare challenge cleared",
					"processor", "memoryexpress",
					"store", storeCode,
				)
			}
			tab.challengeActive = false
			return html, nil
		}

		if !tab.challengeActive {
			tab.challengeActive = true
			s.alertForChallenge(storeCode, tab.ctx)
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(challengePollInterval):
		}
	}
}

func (s *LocalBrowserSession) alertForChallenge(storeCode string, tabCtx context.Context) {
	storeName := StoreName(storeCode)
	message := fmt.Sprintf("%s needs attention in Chrome. Solve the Cloudflare challenge and scraping will resume automatically.", storeName)

	_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		return page.BringToFront().Do(ctx)
	}))

	slog.Warn("Memory Express Cloudflare challenge detected",
		"processor", "memoryexpress",
		"store", storeCode,
		"store_name", storeName,
	)

	if err := s.alerter.Alert("Memory Express challenge", message); err != nil {
		slog.Warn("Failed to send desktop alert for Memory Express challenge",
			"processor", "memoryexpress",
			"store", storeCode,
			"error", err,
		)
	}
}

func defaultChromeProfileDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve local cache dir: %w", err)
	}
	return filepath.Join(base, "rfd-discord-bot", "memoryexpress-chrome-profile"), nil
}

func findLocalChromeExecutable() string {
	candidates := []string{
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
	}

	if runtime.GOOS == "windows" {
		candidates = []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			os.Getenv("LOCALAPPDATA") + `\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			os.Getenv("LOCALAPPDATA") + `\Microsoft\Edge\Application\msedge.exe`,
		}
	}

	if runtime.GOOS == "darwin" {
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	}

	for _, candidate := range candidates {
		if _, err := exec.LookPath(candidate); err == nil {
			return candidate
		}
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}

	for _, name := range []string{"google-chrome", "chrome", "chrome.exe", "msedge", "msedge.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	return "google-chrome"
}
