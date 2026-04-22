package memoryexpress

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"net/url"

	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/chromedp"
)

const browserFallbackWait = 45 * time.Second

func fetchClearanceHTMLWithBrowser(ctx context.Context, pageURL string) (string, error) {
	browserPath, err := findBrowserExecutable()
	if err != nil {
		return "", err
	}

	display, stopDisplay, err := startVirtualDisplayIfNeeded(ctx)
	if err != nil {
		return "", err
	}
	defer stopDisplay()

	profileDir, err := os.MkdirTemp("", "memoryexpress-chrome-*")
	if err != nil {
		return "", fmt.Errorf("create browser profile dir: %w", err)
	}
	defer os.RemoveAll(profileDir)

	env := os.Environ()
	if display != "" {
		env = append(env, "DISPLAY="+display)
	}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.Env(env...),
		chromedp.UserDataDir(profileDir),
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
		chromedp.WindowSize(1920, 1080),
	)

	proxyURL := os.Getenv("PROXY_URL")
	if proxyURL != "" {
		proxyServer, err := buildChromeProxyServer(proxyURL)
		if err != nil {
			return "", err
		}
		opts = append(opts, chromedp.ProxyServer(proxyServer))
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(
		ctx,
		opts...,
	)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	if proxyURL != "" {
		username, password, err := getProxyCredentials(proxyURL)
		if err != nil {
			return "", err
		}
		if err := chromedp.Run(browserCtx, fetch.Enable().WithHandleAuthRequests(true)); err != nil {
			return "", fmt.Errorf("enable proxy auth handling: %w", err)
		}
		chromedp.ListenTarget(browserCtx, func(ev interface{}) {
			switch e := ev.(type) {
			case *fetch.EventAuthRequired:
				go func() {
					_ = chromedp.Run(browserCtx,
						fetch.ContinueWithAuth(e.RequestID, &fetch.AuthChallengeResponse{
							Response: fetch.AuthChallengeResponseResponseProvideCredentials,
							Username: username,
							Password: password,
						}),
					)
				}()
			case *fetch.EventRequestPaused:
				go func() {
					_ = chromedp.Run(browserCtx, fetch.ContinueRequest(e.RequestID))
				}()
			}
		})
	}

	if err := chromedp.Run(
		browserCtx,
		chromedp.Navigate(pageURL),
		chromedp.WaitReady("body", chromedp.ByQuery),
	); err != nil {
		return "", fmt.Errorf("navigate to clearance page: %w", err)
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	timeout := time.NewTimer(browserFallbackWait)
	defer timeout.Stop()

	for {
		var html string
		if err := chromedp.Run(browserCtx, chromedp.OuterHTML("html", &html, chromedp.ByQuery)); err != nil {
			return "", fmt.Errorf("read browser-rendered clearance html: %w", err)
		}
		if !hasCloudflareChallenge(html) {
			return html, nil
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timeout.C:
			return "", fmt.Errorf("cloudflare challenge did not clear within %s", browserFallbackWait)
		case <-ticker.C:
		}
	}
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

func buildChromeProxyServer(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid proxy URL: %w", err)
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), nil
}

func getProxyCredentials(rawURL string) (string, string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid proxy URL: %w", err)
	}
	username := parsed.User.Username()
	password, _ := parsed.User.Password()
	sessionID := fmt.Sprintf("memoryexpress%d", time.Now().UnixNano()%100000)
	password += fmt.Sprintf("_country-CA_session-%s_lifetime-300", sessionID)
	return username, password, nil
}

func findBrowserExecutable() (string, error) {
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
			return path, nil
		}
	}

	type candidate struct {
		path  string
		score int
	}
	var found []candidate
	scoreForName := map[string]int{
		"chrome":                0,
		"chrome.exe":            0,
		"chromium":              1,
		"chromium.exe":          1,
		"msedge":                2,
		"msedge.exe":            2,
		"chrome-headless-shell": 3,
	}

	for _, root := range browserSearchRoots() {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := strings.ToLower(filepath.Base(path))
			score, ok := scoreForName[name]
			if !ok {
				return nil
			}
			found = append(found, candidate{path: path, score: score})
			return nil
		})
	}

	if len(found) == 0 {
		return "", fmt.Errorf("no Chromium-compatible browser found for Memory Express browser scrape")
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].score != found[j].score {
			return found[i].score < found[j].score
		}
		return found[i].path < found[j].path
	})

	return found[0].path, nil
}

func browserSearchRoots() []string {
	var roots []string

	if env := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); env != "" && env != "0" {
		roots = append(roots, env)
	}

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		switch runtime.GOOS {
		case "windows":
			roots = append(roots, filepath.Join(home, "AppData", "Local", "ms-playwright"))
		default:
			roots = append(roots, filepath.Join(home, ".cache", "ms-playwright"))
		}
	}

	roots = append(roots, "/ms-playwright")
	return roots
}
