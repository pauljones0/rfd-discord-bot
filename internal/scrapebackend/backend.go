package scrapebackend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	BackendCamoufox           = "camoufox"
	BackendAICrawler          = "ai-crawler"
	BackendPaidTrial          = "paid-trial"

	DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// FetchOptions describes one attempt to fetch browser-rendered or raw HTML.
type FetchOptions struct {
	Backend             string
	URL                 string
	Timeout             time.Duration
	ChromePath          string
	ChromeProfile       string
	UserAgent           string
	ExternalCommand     string
	CamoufoxCommand     string
	AICrawlerCommand    string
	PaidCommand         string
	ExternalCommandArgs []string
	CamoufoxCommandArgs []string
	AICrawlerArgs       []string
	PaidCommandArgs     []string
	PaidEnabled         bool
	PaidAttempt         func(context.Context) error
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

	backendResult := fetchBackend(attemptCtx, opts)
	result := FetchResult{
		Backend:     opts.Backend,
		URL:         opts.URL,
		FinalURL:    backendResult.finalURL,
		StatusCode:  backendResult.statusCode,
		HTML:        backendResult.html,
		Duration:    time.Since(start),
		BlockSignal: DetectBlockSignal(backendResult.statusCode, backendResult.html),
	}
	if backendResult.err != nil {
		result.Error = backendResult.err.Error()
	}
	return result
}

type backendFetchResult struct {
	html       string
	finalURL   string
	statusCode int
	err        error
}

func fetchBackend(ctx context.Context, opts FetchOptions) backendFetchResult {
	switch opts.Backend {
	case BackendHTTP:
		html, finalURL, statusCode, err := fetchHTTP(ctx, opts)
		return backendFetchResult{html: html, finalURL: finalURL, statusCode: statusCode, err: err}
	case BackendChromedpCloudRun, BackendChromedpPersistent:
		html, finalURL, err := fetchChromedp(ctx, opts)
		return backendFetchResult{html: html, finalURL: finalURL, err: err}
	case BackendPlaywright:
		html, finalURL, statusCode, err := fetchPlaywright(ctx, opts)
		return backendFetchResult{html: html, finalURL: finalURL, statusCode: statusCode, err: err}
	case BackendExternalStealth:
		html, err := fetchCommand(ctx, opts.ExternalCommand, opts.ExternalCommandArgs, opts.URL)
		return backendFetchResult{html: html, err: err}
	case BackendCamoufox:
		html, err := fetchCommand(ctx, opts.CamoufoxCommand, opts.CamoufoxCommandArgs, opts.URL)
		return backendFetchResult{html: html, err: err}
	case BackendAICrawler:
		html, err := fetchCommand(ctx, opts.AICrawlerCommand, opts.AICrawlerArgs, opts.URL)
		return backendFetchResult{html: html, err: err}
	case BackendPaidTrial:
		html, err := fetchPaidCommand(ctx, opts)
		return backendFetchResult{html: html, err: err}
	default:
		return backendFetchResult{err: fmt.Errorf("unknown scraper backend %q", opts.Backend)}
	}
}

func fetchPaidCommand(ctx context.Context, opts FetchOptions) (string, error) {
	if !opts.PaidEnabled {
		return "", fmt.Errorf("paid browser backend is disabled")
	}
	if opts.PaidAttempt != nil {
		if err := opts.PaidAttempt(ctx); err != nil {
			slog.Warn("Paid browser attempt skipped", "backend", opts.Backend, "url", opts.URL, "reason", err)
			return "", err
		}
	}
	return fetchCommand(ctx, opts.PaidCommand, opts.PaidCommandArgs, opts.URL)
}

// FilterBackendsForPaidEnabled removes paid backends unless the caller
// explicitly enables paid browser usage for that site.
func FilterBackendsForPaidEnabled(backends []string, paidEnabled bool) []string {
	if paidEnabled {
		return append([]string(nil), backends...)
	}
	out := make([]string, 0, len(backends))
	for _, backend := range backends {
		if backend == BackendPaidTrial {
			continue
		}
		out = append(out, backend)
	}
	return out
}

type AttemptCounter struct {
	attempts    map[string]int
	failures    map[string]int
	errors      map[string]int
	blocks      map[string]int
	parseErrors map[string]int
	verdicts    map[string]int
}

func NewAttemptCounter() *AttemptCounter {
	return &AttemptCounter{}
}

func (c *AttemptCounter) RecordAttempt(backend string) {
	if c == nil {
		return
	}
	incrementCount(&c.attempts, backend)
}

func (c *AttemptCounter) RecordFetchResult(backend string, result FetchResult) string {
	if c == nil {
		return fetchResultIssue(result)
	}
	issue := fetchResultIssue(result)
	if issue == "" {
		return ""
	}
	incrementCount(&c.failures, backend)
	if result.Error != "" {
		incrementCount(&c.errors, backend)
	}
	if result.BlockSignal != "" {
		incrementCount(&c.blocks, backend+":"+result.BlockSignal)
	}
	return issue
}

func (c *AttemptCounter) RecordError(backend string) {
	if c == nil {
		return
	}
	incrementCount(&c.failures, backend)
	incrementCount(&c.errors, backend)
}

func (c *AttemptCounter) RecordParseError(backend string) {
	if c == nil {
		return
	}
	incrementCount(&c.failures, backend)
	incrementCount(&c.parseErrors, backend)
}

func (c *AttemptCounter) RecordVerdict(backend, verdict string) {
	if c == nil || strings.TrimSpace(verdict) == "" {
		return
	}
	incrementCount(&c.verdicts, backend+":"+verdict)
}

func (c *AttemptCounter) TotalAttempts() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, count := range c.attempts {
		total += count
	}
	return total
}

func (c *AttemptCounter) HasFailures() bool {
	if c == nil {
		return false
	}
	return len(c.failures) > 0 || len(c.errors) > 0 || len(c.blocks) > 0 || len(c.parseErrors) > 0
}

func (c *AttemptCounter) Attrs() []any {
	if c == nil {
		return nil
	}
	return []any{
		"backend_attempts", FormatCounts(c.attempts),
		"backend_failures", FormatCounts(c.failures),
		"backend_errors", FormatCounts(c.errors),
		"backend_blocks", FormatCounts(c.blocks),
		"backend_parse_errors", FormatCounts(c.parseErrors),
		"backend_verdicts", FormatCounts(c.verdicts),
	}
}

func fetchResultIssue(result FetchResult) string {
	switch {
	case result.Error != "" && result.BlockSignal != "":
		return strings.TrimSpace(result.Error + " " + result.BlockSignal)
	case result.Error != "":
		return result.Error
	case result.BlockSignal != "":
		return "blocked by " + result.BlockSignal
	default:
		return ""
	}
}

func incrementCount(counts *map[string]int, key string) {
	key = strings.TrimSpace(key)
	if key == "" {
		key = "unknown"
	}
	if *counts == nil {
		*counts = make(map[string]int)
	}
	(*counts)[key]++
}

func FormatCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ",")
}

// DetectBlockSignal returns a compact label for common challenge/block pages.
func DetectBlockSignal(statusCode int, body string) string {
	lower := strings.ToLower(body)
	for _, rule := range blockSignalRules {
		if rule.matches(lower) {
			return rule.signal
		}
	}
	switch statusCode {
	case http.StatusForbidden:
		return "http-403"
	case http.StatusTooManyRequests:
		return "http-429"
	default:
		return ""
	}
}

type blockSignalRule struct {
	signal string
	any    []string
	all    []string
}

var blockSignalRules = []blockSignalRule{
	{signal: "cloudflare-turnstile", any: []string{"cf-turnstile"}},
	{signal: "cloudflare-managed-challenge", any: []string{"/cdn-cgi/challenge-platform/", "__cf_chl_"}, all: []string{"just a moment", "enable javascript and cookies"}},
	{signal: "akamai-access-denied", any: []string{"edgesuite.net", "akamai"}, all: []string{"access denied", "you don't have permission to access"}},
	{signal: "perimeterx-challenge", any: []string{"perimeterx", "px-captcha"}},
	{signal: "captcha", any: []string{"g-recaptcha", "hcaptcha", "captcha-form", "captcha-container", "complete the security check", "verify you are human", "are you a robot", "enter the characters you see"}},
}

func (r blockSignalRule) matches(body string) bool {
	return containsAny(body, r.any) || containsAll(body, r.all)
}

func containsAny(value string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func containsAll(value string, needles []string) bool {
	if len(needles) == 0 {
		return false
	}
	for _, needle := range needles {
		if !strings.Contains(value, needle) {
			return false
		}
	}
	return true
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
	} else if err := removeStaleBrowserLocks(profileDir); err != nil {
		return "", "", fmt.Errorf("remove stale browser profile locks: %w", err)
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

func CommandArgsFromEnv(keys ...string) []string {
	for _, key := range keys {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			slog.Warn("Invalid scraper command args env; expected JSON string array", "key", key, "error", err)
			return nil
		}
		out := make([]string, 0, len(args))
		for _, arg := range args {
			if strings.TrimSpace(arg) != "" {
				out = append(out, arg)
			}
		}
		return out
	}
	return nil
}

func fetchCommand(ctx context.Context, command string, args []string, targetURL string) (string, error) {
	if len(args) > 0 {
		return fetchCommandArgs(ctx, args, targetURL)
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("backend command is not configured")
	}
	slog.Warn("Using legacy shell-string scraper command; migrate to *_COMMAND_ARGS JSON argv env")
	command = strings.ReplaceAll(command, "{url}", targetURL)
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	return runCommand(cmd, targetURL)
}

func fetchCommandArgs(ctx context.Context, args []string, targetURL string) (string, error) {
	resolved := commandArgsWithTarget(args, targetURL)
	if len(resolved) == 0 || strings.TrimSpace(resolved[0]) == "" {
		return "", fmt.Errorf("backend command args are not configured")
	}
	cmd := exec.CommandContext(ctx, resolved[0], resolved[1:]...)
	return runCommand(cmd, targetURL)
}

func commandArgsWithTarget(args []string, targetURL string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "{url}" {
			out = append(out, targetURL)
			continue
		}
		out = append(out, arg)
	}
	return out
}

func runCommand(cmd *exec.Cmd, targetURL string) (string, error) {
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

func removeStaleBrowserLocks(profileDir string) error {
	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie", "lockfile"} {
		path := filepath.Join(profileDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
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
