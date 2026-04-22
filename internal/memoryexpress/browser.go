package memoryexpress

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const browserFallbackWait = 45 * time.Second

func fetchClearanceHTMLWithBrowser(ctx context.Context, pageURL string) (string, error) {
	browserPath, err := findBrowserExecutable()
	if err != nil {
		return "", err
	}

	profileDir, err := os.MkdirTemp("", "memoryexpress-chrome-*")
	if err != nil {
		return "", fmt.Errorf("create browser profile dir: %w", err)
	}
	defer os.RemoveAll(profileDir)

	allocCtx, allocCancel := chromedp.NewExecAllocator(
		ctx,
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(browserPath),
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
		)...,
	)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

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
