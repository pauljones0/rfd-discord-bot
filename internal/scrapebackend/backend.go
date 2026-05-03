package scrapebackend

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/playwright-community/playwright-go"
)

const (
	BackendHTTP               = "http"
	BackendChromedpCloudRun   = "chromedp-cloudrun"
	BackendChromedpPersistent = "chromedp-persistent"
	BackendPlaywright         = "playwright"
	BackendExternalStealth    = "external-stealth"
	BackendPaidTrial          = "paid-trial"

	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// FetchOptions describes one attempt to fetch browser-rendered or raw HTML.
type FetchOptions struct {
	Backend         string
	URL             string
	Timeout         time.Duration
	ChromePath      string
	ChromeProfile   string
	UserAgent       string
	ExternalCommand string
	PaidCommand     string
}

// FetchResult captures the observable result from one backend attempt.
type FetchResult struct {
	Backend     string
	URL         string
	FinalURL    string
	StatusCode  int
	HTML        string
	Duration    time.Duration
	BlockSignal string
	Error       string
}

// FetchHTML fetches a URL using a named backend and records block/challenge signals.
func FetchHTML(ctx context.Context, opts FetchOptions) FetchResult {
	start := time.Now()
	if opts.Timeout <= 0 {
		opts.Timeout = 45 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = DefaultUserAgent
	}

	attemptCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	result := FetchResult{
		Backend: opts.Backend,
		URL:     opts.URL,
	}

	var html string
	var finalURL string
	var statusCode int
	var err error

	switch opts.Backend {
	case BackendHTTP:
		html, finalURL, statusCode, err = fetchHTTP(attemptCtx, opts)
	case BackendChromedpCloudRun, BackendChromedpPersistent:
		html, finalURL, err = fetchChromedp(attemptCtx, opts)
	case BackendPlaywright:
		html, finalURL, statusCode, err = fetchPlaywright(attemptCtx, opts)
	case BackendExternalStealth:
		html, err = fetchCommand(attemptCtx, opts.ExternalCommand, opts.URL)
	case BackendPaidTrial:
		html, err = fetchCommand(attemptCtx, opts.PaidCommand, opts.URL)
	default:
		err = fmt.Errorf("unknown scraper backend %q", opts.Backend)
	}

	result.Duration = time.Since(start)
	result.HTML = html
	result.FinalURL = finalURL
	result.StatusCode = statusCode
	result.BlockSignal = DetectBlockSignal(statusCode, html)
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

// DetectBlockSignal returns a compact label for common challenge/block pages.
func DetectBlockSignal(statusCode int, body string) string {
	lower := strings.ToLower(body)
	switch {
	case strings.Contains(lower, "cf-turnstile"):
		return "cloudflare-turnstile"
	case strings.Contains(lower, "/cdn-cgi/challenge-platform/") ||
		strings.Contains(lower, "__cf_chl_") ||
		(strings.Contains(lower, "just a moment") && strings.Contains(lower, "enable javascript and cookies")):
		return "cloudflare-managed-challenge"
	case strings.Contains(lower, "edgesuite.net") ||
		strings.Contains(lower, "akamai") ||
		(strings.Contains(lower, "access denied") && strings.Contains(lower, "you don't have permission to access")):
		return "akamai-access-denied"
	case strings.Contains(lower, "perimeterx") || strings.Contains(lower, "px-captcha"):
		return "perimeterx-challenge"
	case strings.Contains(lower, "g-recaptcha") ||
		strings.Contains(lower, "hcaptcha") ||
		strings.Contains(lower, "captcha-form") ||
		strings.Contains(lower, "captcha-container") ||
		strings.Contains(lower, "complete the security check") ||
		strings.Contains(lower, "verify you are human") ||
		strings.Contains(lower, "are you a robot") ||
		strings.Contains(lower, "enter the characters you see"):
		return "captcha"
	case statusCode == http.StatusForbidden:
		return "http-403"
	case statusCode == http.StatusTooManyRequests:
		return "http-429"
	default:
		return ""
	}
}

func fetchHTTP(ctx context.Context, opts FetchOptions) (string, string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, opts.URL, nil)
	if err != nil {
		return "", "", 0, err
	}
	req.Header.Set("User-Agent", opts.UserAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-CA,en;q=0.9")

	client := &http.Client{Timeout: opts.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", 0, err
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if readErr != nil {
		return string(body), resp.Request.URL.String(), resp.StatusCode, readErr
	}
	if resp.StatusCode >= 400 {
		return string(body), resp.Request.URL.String(), resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(body), resp.Request.URL.String(), resp.StatusCode, nil
}

func fetchChromedp(ctx context.Context, opts FetchOptions) (string, string, error) {
	chromePath := opts.ChromePath
	if chromePath == "" {
		chromePath = findBrowserExecutable()
	}

	profileDir := opts.ChromeProfile
	if profileDir == "" && opts.Backend == BackendChromedpPersistent {
		profileDir = firstNonEmpty(os.Getenv("SCRAPER_CHROME_PROFILE_DIR"), os.Getenv("SCRAPELAB_CHROME_PROFILE_DIR"))
	}
	removeProfile := false
	if opts.Backend == BackendChromedpCloudRun || profileDir == "" {
		dir, err := os.MkdirTemp("", "scrape-backend-chrome-*")
		if err != nil {
			return "", "", fmt.Errorf("create browser profile dir: %w", err)
		}
		profileDir = dir
		removeProfile = true
	} else if !filepath.IsAbs(profileDir) {
		absProfileDir, err := filepath.Abs(profileDir)
		if err != nil {
			return "", "", fmt.Errorf("resolve browser profile dir: %w", err)
		}
		profileDir = absProfileDir
	}
	if removeProfile {
		defer os.RemoveAll(profileDir)
	} else if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return "", "", fmt.Errorf("create browser profile dir: %w", err)
	}

	display := ""
	stopDisplay := func() {}
	if opts.Backend == BackendChromedpPersistent {
		var err error
		display, stopDisplay, err = startVirtualDisplayIfNeeded(ctx)
		if err != nil {
			return "", "", err
		}
	}
	defer stopDisplay()

	env := os.Environ()
	if display != "" {
		env = append(env, "DISPLAY="+display)
	}

	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chromePath),
		chromedp.Env(env...),
		chromedp.UserDataDir(profileDir),
		chromedp.UserAgent(opts.UserAgent),
		chromedp.Flag("headless", opts.Backend == BackendChromedpCloudRun),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-hang-monitor", true),
		chromedp.WindowSize(1600, 1000),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	var html string
	var finalURL string
	if err := chromedp.Run(browserCtx,
		chromedp.Navigate(opts.URL),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.Location(&finalURL),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	); err != nil {
		return html, finalURL, err
	}
	return html, finalURL, nil
}

func fetchPlaywright(ctx context.Context, opts FetchOptions) (string, string, int, error) {
	pw, err := playwright.Run()
	if err != nil {
		return "", "", 0, err
	}
	defer pw.Stop()

	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{
		Headless: playwright.Bool(true),
	})
	if err != nil {
		return "", "", 0, err
	}
	defer browser.Close()

	page, err := browser.NewPage(playwright.BrowserNewPageOptions{
		UserAgent: playwright.String(opts.UserAgent),
		Locale:    playwright.String("en-CA"),
	})
	if err != nil {
		return "", "", 0, err
	}

	resp, err := page.Goto(opts.URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(float64(opts.Timeout.Milliseconds())),
	})
	if err != nil {
		return "", "", 0, err
	}

	select {
	case <-ctx.Done():
		return "", page.URL(), 0, ctx.Err()
	case <-time.After(2 * time.Second):
	}

	html, err := page.Content()
	status := 0
	if resp != nil {
		status = resp.Status()
	}
	return html, page.URL(), status, err
}

func fetchCommand(ctx context.Context, command, targetURL string) (string, error) {
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("backend command is not configured")
	}

	command = strings.ReplaceAll(command, "{url}", targetURL)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	cmd.Env = append(os.Environ(), "SCRAPELAB_TARGET_URL="+targetURL)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func findBrowserExecutable() string {
	for _, path := range []string{
		os.Getenv("SCRAPER_CHROME_PATH"),
		os.Getenv("CHROME_PATH"),
		os.Getenv("MEMEXPRESS_CHROME_PATH"),
	} {
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}

	for _, name := range []string{
		"google-chrome",
		"google-chrome-stable",
		"chromium",
		"chromium-browser",
		"chrome",
		"msedge",
		"microsoft-edge",
		"chrome.exe",
		"msedge.exe",
	} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}

	if runtime.GOOS == "windows" {
		for _, path := range []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
			`C:\Program Files\Microsoft\Edge\Application\msedge.exe`,
			`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		} {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}

	for _, pattern := range []string{
		"/ms-playwright/chromium-*/chrome-linux64/chrome",
		"/ms-playwright/chromium-*/chrome-linux/chrome",
		"/ms-playwright/chromium-*/chrome-linux/chrome-wrapper",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, path := range matches {
			if _, err := os.Stat(path); err == nil {
				return path
			}
		}
	}

	return "google-chrome"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func startVirtualDisplayIfNeeded(ctx context.Context) (string, func(), error) {
	if runtime.GOOS != "linux" {
		return "", func() {}, nil
	}
	if display := os.Getenv("DISPLAY"); display != "" {
		return display, func() {}, nil
	}

	xvfbPath, err := exec.LookPath("Xvfb")
	if err != nil {
		return "", nil, fmt.Errorf("find Xvfb: %w", err)
	}

	const display = ":99"
	cmd := exec.CommandContext(ctx, xvfbPath, display, "-screen", "0", "1920x1080x24", "-nolisten", "tcp", "-ac")
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start Xvfb: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-waitCh:
		case <-time.After(2 * time.Second):
		}
	}

	socketPath := "/tmp/.X11-unix/X99"
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(socketPath); err == nil {
			return display, cleanup, nil
		}
		select {
		case err := <-waitCh:
			if err == nil {
				err = fmt.Errorf("Xvfb exited before opening display %s", display)
			}
			return "", nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		case <-ctx.Done():
			cleanup()
			return "", nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	cleanup()
	return "", nil, fmt.Errorf("Xvfb did not open display %s within 5s: %s", display, strings.TrimSpace(stderr.String()))
}
