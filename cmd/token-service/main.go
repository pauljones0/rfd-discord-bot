// Token Service — generates reCAPTCHA v3 tokens from a real headed Chrome browser.
//
// This service runs alongside a headed Chrome instance (with a virtual display on
// a GCE VM, or a real display on a local machine). It maintains a persistent page
// on carfax.ca and exposes an HTTP endpoint to generate reCAPTCHA tokens on demand.
//
// Architecture:
//   Browser (Chrome + Xvfb) ←→ CDP ←→ Token Service ←→ Cloud Run bot
//
// The bot calls GET /token to obtain a high-scoring reCAPTCHA v3 token, then uses
// it in direct HTTP calls to Carfax's API. This avoids headless browser detection
// entirely because the Chrome instance is a real headed browser.
//
// ─────────────────────────────────────────────────────────────────────────────────
// GCE VM Setup (e2-micro, ~$6/month):
//
//   1. Create a VM:
//      gcloud compute instances create carfax-token-service \
//        --machine-type=e2-micro --zone=us-central1-a \
//        --image-family=ubuntu-2404-lts-amd64 --image-project=ubuntu-os-cloud
//
//   2. Install Chrome + Xvfb:
//      sudo apt update && sudo apt install -y google-chrome-stable xvfb
//
//   3. Start Xvfb (virtual display):
//      Xvfb :99 -screen 0 1920x1080x24 &
//      export DISPLAY=:99
//
//   4. Run the service:
//      TOKEN_SERVICE_SECRET=your-secret-here ./token-service
//
//   5. Firewall rule (restrict to Cloud Run egress):
//      gcloud compute firewall-rules create allow-token-service \
//        --allow=tcp:8081 --source-ranges=0.0.0.0/0 --target-tags=token-service
//
//   6. Systemd service (auto-restart):
//      See the [Service] section comments below for a systemd unit template.
//
// ─────────────────────────────────────────────────────────────────────────────────
// Local Machine / Home Server Setup:
//
//   No Xvfb needed — Chrome uses your real display.
//
//   1. Set CHROME_PATH to your Chrome/Chromium binary (or leave empty for default)
//   2. Run: TOKEN_SERVICE_SECRET=dev ./token-service
//   3. Expose via Cloudflare Tunnel or ngrok:
//      cloudflared tunnel --url http://localhost:8081
//   4. Set CARFAX_TOKEN_SERVICE_URL in your bot's .env to the tunnel URL
//
// ─────────────────────────────────────────────────────────────────────────────────
// Systemd Unit Template (/etc/systemd/system/token-service.service):
//
//   [Unit]
//   Description=Carfax reCAPTCHA Token Service
//   After=network.target
//
//   [Service]
//   Type=simple
//   User=chrome
//   Environment=DISPLAY=:99
//   Environment=TOKEN_SERVICE_SECRET=your-secret-here
//   ExecStartPre=/usr/bin/Xvfb :99 -screen 0 1920x1080x24
//   ExecStart=/opt/token-service/token-service
//   Restart=always
//   RestartSec=10
//
//   [Install]
//   WantedBy=multi-user.target
//
// ─────────────────────────────────────────────────────────────────────────────────
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const (
	carfaxPageURL = "https://www.carfax.ca/whats-my-car-worth/car-value"
	carfaxSiteKey = "6Le6DI0qAAAAAJwKjPLFgAPZdi8lW9Eg-rNpDv4-"
)

// tokenService manages a persistent Chrome page for reCAPTCHA token generation.
type tokenService struct {
	mu          sync.Mutex
	ctx         context.Context    // chromedp browser context
	cancel      context.CancelFunc // cancels the browser context
	allocCancel context.CancelFunc // cancels the allocator
	pageReady   bool
	secret      string
	proxyURL    string // optional Evomi proxy URL for Canadian IP
}

func main() {
	slog.Info("Starting Carfax reCAPTCHA Token Service")

	secret := os.Getenv("TOKEN_SERVICE_SECRET")
	if secret == "" {
		slog.Error("TOKEN_SERVICE_SECRET environment variable is required")
		os.Exit(1)
	}

	port := os.Getenv("TOKEN_SERVICE_PORT")
	if port == "" {
		port = "8081"
	}

	proxyURL := os.Getenv("PROXY_URL")
	if proxyURL != "" {
		slog.Info("Using proxy for Chrome", "proxy", maskProxy(proxyURL))
	} else {
		slog.Warn("No PROXY_URL set — Chrome will use direct connection. " +
			"Carfax may geo-block non-Canadian IPs. Set PROXY_URL for residential Canadian proxy.")
	}

	svc := &tokenService{secret: secret, proxyURL: proxyURL}
	if err := svc.initBrowser(); err != nil {
		slog.Error("Failed to initialize browser", "error", err)
		os.Exit(1)
	}
	defer svc.cleanup()

	svc.closeExtraTabs()

	if err := svc.warmPage(); err != nil {
		slog.Error("Failed to warm page", "error", err)
		os.Exit(1)
	}

	// Monitor Chrome process — if the user accidentally closes the browser
	// window, automatically relaunch it and re-warm the page.
	go svc.watchChrome()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /token", svc.handleToken)
	mux.HandleFunc("GET /health", svc.handleHealth)

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		slog.Info("Shutting down token service...")
		server.Close()
	}()

	slog.Info("Token service listening", "port", port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		slog.Error("Server error", "error", err)
		os.Exit(1)
	}
}

// initBrowser launches a headed Chrome instance and connects via CDP.
//
// Chrome is launched WITHOUT --headless to get maximum reCAPTCHA trust scores.
// On a server, use Xvfb to provide a virtual display (see setup docs above).
// On a local machine, Chrome opens normally on your display.
func (s *tokenService) initBrowser() error {
	chromePath := os.Getenv("CHROME_PATH")
	if chromePath == "" {
		chromePath = findChrome()
	}

	slog.Info("Launching Chrome", "path", chromePath)

	// Resolve user data dir to an absolute path — Chrome ignores relative paths
	// when another Chrome instance is already running, causing "Opening in
	// existing browser session" errors.
	dataDir := os.Getenv("CHROME_DATA_DIR")
	if dataDir == "" {
		exe, _ := os.Executable()
		dataDir = filepath.Join(filepath.Dir(exe), "carfax-chrome-data")
	}
	slog.Info("Chrome user data dir", "path", dataDir)

	opts := []chromedp.ExecAllocatorOption{
		chromedp.ExecPath(chromePath),

		// Absolute path to a dedicated profile directory. Persists cookies/history
		// across restarts, building reCAPTCHA trust over time. Must be separate
		// from any running Chrome profile to avoid singleton conflicts.
		chromedp.UserDataDir(dataDir),

		// NO headless flag — headed Chrome scores highest on reCAPTCHA v3.
		// If you must run headless (e.g., no display available and no Xvfb),
		// uncomment the next line, but expect lower token scores:
		// chromedp.Flag("headless", "new"),

		// Force a specific debugging port. On Windows, --remote-debugging-port=0
		// (chromedp's default) does NOT bypass Chrome's singleton detection, so
		// Chrome hands off to the existing instance and exits. A specific non-zero
		// port forces Chrome to start a genuinely new instance.
		chromedp.Flag("remote-debugging-port", "9222"),

		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.Flag("disable-hang-monitor", true),

		// Window size — gives reCAPTCHA a realistic viewport
		chromedp.WindowSize(1920, 1080),
	}

	// Add proxy if configured — uses the same Evomi residential proxy format as
	// the main bot. This ensures Chrome's traffic comes from a Canadian residential IP,
	// which is critical for reCAPTCHA scoring on carfax.ca.
	//
	// Chrome's --proxy-server flag does NOT support inline credentials (user:pass@host).
	// We pass just the host:port here, then handle proxy authentication via CDP's
	// Fetch.authRequired event in warmPage().
	if s.proxyURL != "" {
		proxyServer, err := buildChromeProxyServer(s.proxyURL)
		if err != nil {
			slog.Warn("Failed to parse proxy URL, proceeding without proxy", "error", err)
		} else {
			opts = append(opts, chromedp.ProxyServer(proxyServer))
			slog.Info("Chrome configured with proxy", "server", proxyServer)
		}
	}

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	s.allocCancel = allocCancel

	ctx, cancel := chromedp.NewContext(allocCtx, chromedp.WithLogf(slog.Info))
	s.ctx = ctx
	s.cancel = cancel

	// Navigate to a blank page to verify Chrome is running
	if err := chromedp.Run(ctx, chromedp.Navigate("about:blank")); err != nil {
		allocCancel()
		return fmt.Errorf("chrome launch failed: %w", err)
	}

	slog.Info("Chrome launched successfully")
	return nil
}

// closeExtraTabs closes blank/empty browser tabs that accumulate over restarts.
// Chrome opens a default blank/new-tab page on every launch, and on Windows with
// a persistent --user-data-dir, singleton detection can hand off to an already-running
// Chrome, creating yet another blank tab. If the service restart-loops (e.g. proxy
// down), this accumulates thousands of blank tabs. This method cleans them all up.
//
// Only closes tabs whose URL is about:blank, empty, or chrome:// — never touches
// tabs that have real content (e.g. an existing Carfax page).
func (s *tokenService) closeExtraTabs() {
	c := chromedp.FromContext(s.ctx)
	if c == nil || c.Browser == nil {
		slog.Warn("Browser context not ready, skipping tab cleanup")
		return
	}
	browserCtx := cdp.WithExecutor(s.ctx, c.Browser)

	targets, err := target.GetTargets().Do(browserCtx)
	if err != nil {
		slog.Warn("Failed to list browser targets for cleanup", "error", err)
		return
	}

	// Identify chromedp's own tab so we never close it.
	var ownTarget target.ID
	if c.Target != nil {
		ownTarget = c.Target.TargetID
	}

	slog.Info("Browser tab audit", "total_targets", len(targets), "own_target", ownTarget)

	var closed int
	for _, t := range targets {
		if t.Type != "page" || t.TargetID == ownTarget {
			continue
		}
		// Only close blank/empty/chrome tabs — leave real pages alone.
		if isBlankURL(t.URL) {
			if err := target.CloseTarget(t.TargetID).Do(browserCtx); err != nil {
				slog.Warn("Failed to close tab", "target_id", t.TargetID, "url", t.URL, "error", err)
			} else {
				closed++
			}
		} else {
			slog.Info("Keeping non-blank tab", "target_id", t.TargetID, "url", t.URL)
		}
	}
	if closed > 0 {
		slog.Info("Closed stale blank tabs", "count", closed)
	}
}

// isBlankURL returns true for URLs that represent empty/default tabs.
func isBlankURL(u string) bool {
	return u == "" || u == "about:blank" || strings.HasPrefix(u, "chrome://") || strings.HasPrefix(u, "chrome-search://")
}

// warmPage navigates to the Carfax valuation page and waits for reCAPTCHA to load.
// This establishes the persistent page that generates tokens on demand.
func (s *tokenService) warmPage() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	slog.Info("Warming Carfax page...")

	// Set up proxy authentication if a proxy is configured.
	// Chrome's --proxy-server doesn't support inline credentials, so we handle
	// the 407 Proxy Authentication Required challenge via CDP's Fetch domain.
	if s.proxyURL != "" {
		username, password, err := getProxyCredentials(s.proxyURL)
		if err != nil {
			return fmt.Errorf("proxy credentials: %w", err)
		}
		// Enable Fetch domain to intercept auth challenges
		if err := chromedp.Run(s.ctx, fetch.Enable().WithHandleAuthRequests(true)); err != nil {
			return fmt.Errorf("failed to enable fetch for proxy auth: %w", err)
		}
		// Listen for auth challenges and respond with proxy credentials
		chromedp.ListenTarget(s.ctx, func(ev interface{}) {
			switch e := ev.(type) {
			case *fetch.EventAuthRequired:
				go func() {
					_ = chromedp.Run(s.ctx,
						fetch.ContinueWithAuth(e.RequestID, &fetch.AuthChallengeResponse{
							Response: fetch.AuthChallengeResponseResponseProvideCredentials,
							Username: username,
							Password: password,
						}),
					)
				}()
			case *fetch.EventRequestPaused:
				go func() {
					_ = chromedp.Run(s.ctx, fetch.ContinueRequest(e.RequestID))
				}()
			}
		})
		slog.Info("Proxy auth handler installed")
	}

	err := chromedp.Run(s.ctx,
		chromedp.Navigate(carfaxPageURL),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second), // let reCAPTCHA JS fully initialize
	)
	if err != nil {
		return fmt.Errorf("failed to navigate to carfax: %w", err)
	}

	// Verify reCAPTCHA is loaded
	var recaptchaReady bool
	err = chromedp.Run(s.ctx,
		chromedp.Evaluate(`typeof window.grecaptcha !== 'undefined' && typeof window.grecaptcha.execute === 'function'`, &recaptchaReady),
	)
	if err != nil {
		return fmt.Errorf("failed to check recaptcha: %w", err)
	}
	if !recaptchaReady {
		// Wait a bit more and retry
		slog.Warn("reCAPTCHA not ready yet, waiting...")
		err = chromedp.Run(s.ctx, chromedp.Sleep(5*time.Second))
		if err != nil {
			return fmt.Errorf("sleep failed: %w", err)
		}
		err = chromedp.Run(s.ctx,
			chromedp.Evaluate(`typeof window.grecaptcha !== 'undefined' && typeof window.grecaptcha.execute === 'function'`, &recaptchaReady),
		)
		if err != nil || !recaptchaReady {
			return fmt.Errorf("reCAPTCHA failed to load on carfax page")
		}
	}

	// Dismiss cookie banners that might interfere
	chromedp.Run(s.ctx, chromedp.Evaluate(`
		const btn = document.querySelector('#onetrust-accept-btn-handler');
		if (btn) btn.click();
	`, nil))

	s.pageReady = true
	slog.Info("Carfax page warmed, reCAPTCHA ready")
	return nil
}

// generateToken calls grecaptcha.execute() on the persistent Carfax page.
func (s *tokenService) generateToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.pageReady {
		// Page crashed or was never initialized — try to recover
		s.mu.Unlock()
		if err := s.warmPage(); err != nil {
			s.mu.Lock()
			return "", fmt.Errorf("page recovery failed: %w", err)
		}
		s.mu.Lock()
	}

	var token string
	// grecaptcha.execute() returns a Promise, so we need to resolve it.
	// chromedp.Evaluate with chromedp.EvalAsValue handles Promises by
	// awaiting them automatically when the expression returns a thenable.
	jsExpr := fmt.Sprintf(
		`grecaptcha.execute('%s', {action: 'submit'}).then(t => t).catch(e => 'ERROR:' + e.message)`,
		carfaxSiteKey,
	)

	err := chromedp.Run(s.ctx,
		chromedp.Evaluate(jsExpr, &token, func(p *cdpruntime.EvaluateParams) *cdpruntime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)
	if err != nil {
		s.pageReady = false
		return "", fmt.Errorf("failed to execute grecaptcha: %w", err)
	}

	if strings.HasPrefix(token, "ERROR:") {
		s.pageReady = false
		return "", fmt.Errorf("grecaptcha.execute failed: %s", token[6:])
	}

	if len(token) < 100 {
		s.pageReady = false
		return "", fmt.Errorf("token suspiciously short (%d chars)", len(token))
	}

	return token, nil
}

// handleToken serves GET /token — returns a reCAPTCHA token.
// Retries up to 3 times internally: if the first token generation fails (page
// crashed, JS error), it refreshes the page and tries again before returning an error.
func (s *tokenService) handleToken(w http.ResponseWriter, r *http.Request) {
	if !s.checkAuth(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	start := time.Now()
	reqID := fmt.Sprintf("%d", start.UnixMilli()%100000) // short request ID for log correlation
	slog.Info("Token request received",
		"request_id", reqID,
		"remote_addr", r.RemoteAddr)

	var token string
	var lastErr error
	for attempt := range 3 {
		token, lastErr = s.generateToken()
		if lastErr == nil {
			break
		}
		slog.Warn("Token generation attempt failed, retrying",
			"request_id", reqID,
			"attempt", attempt+1, "error", lastErr)
		// Page is likely stale — warmPage will be called on next generateToken
		time.Sleep(time.Duration(500*(attempt+1)) * time.Millisecond)
	}

	if lastErr != nil {
		slog.Error("Token generation failed — unable to send token",
			"request_id", reqID,
			"error", lastErr,
			"duration_ms", time.Since(start).Milliseconds())
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": lastErr.Error()})
		return
	}

	slog.Info("Token sent successfully",
		"request_id", reqID,
		"token_length", len(token),
		"duration_ms", time.Since(start).Milliseconds())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"token":      token,
		"expires_at": time.Now().Add(2 * time.Minute).Format(time.RFC3339),
	})

	// Post-token refresh: reload the Carfax page in the background so the
	// next token request gets a fresh reCAPTCHA context. Without this,
	// successive tokens are generated from a stale idle page, which lowers
	// the reCAPTCHA v3 score and causes Carfax to reject them.
	go s.refreshPageAfterToken(reqID)
}

// refreshPageAfterToken reloads the Carfax page after a token is sent.
// This gives the next token request a fresh reCAPTCHA context with a new
// page load event, which improves the v3 score. A short delay simulates
// natural browsing behavior (user reading the page before navigating).
func (s *tokenService) refreshPageAfterToken(reqID string) {
	// Brief delay — simulates a user spending time on the page before
	// navigating. Immediate reloads look bot-like to reCAPTCHA.
	time.Sleep(2 * time.Second)

	s.mu.Lock()
	defer s.mu.Unlock()

	start := time.Now()

	// Reload the current tab (not Navigate, which opens a new tab in the
	// browser context). Reload triggers a fresh page load event which resets
	// the reCAPTCHA interaction timer, while preserving cookies and session.
	err := chromedp.Run(s.ctx,
		chromedp.Reload(),
		chromedp.WaitReady("body"),
		chromedp.Sleep(1*time.Second),
	)
	if err != nil {
		slog.Warn("Post-token page refresh failed",
			"request_id", reqID,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds())
		s.pageReady = false
		return
	}

	// Verify reCAPTCHA is still ready
	var ready bool
	err = chromedp.Run(s.ctx,
		chromedp.Evaluate(`typeof window.grecaptcha !== 'undefined' && typeof window.grecaptcha.execute === 'function'`, &ready),
	)
	if err != nil || !ready {
		slog.Warn("reCAPTCHA not ready after refresh, will recover on next request",
			"request_id", reqID)
		s.pageReady = false
		return
	}

	slog.Info("Post-token page refresh complete",
		"request_id", reqID,
		"duration_ms", time.Since(start).Milliseconds())
}

// handleHealth serves GET /health — reports service and page status.
func (s *tokenService) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "ok",
		"page_ready": s.pageReady,
	})
}

// checkAuth validates the Bearer token against TOKEN_SERVICE_SECRET.
func (s *tokenService) checkAuth(r *http.Request) bool {
	auth := r.Header.Get("Authorization")
	return auth == "Bearer "+s.secret
}

// cleanup shuts down Chrome and the CDP connection.
func (s *tokenService) cleanup() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.allocCancel != nil {
		s.allocCancel()
	}
}

// watchChrome polls the Chrome process every 5 seconds and relaunches it if
// the user accidentally closes the browser window (or if Chrome crashes).
func (s *tokenService) watchChrome() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		// Quick health check: try to evaluate a trivial expression via CDP.
		// If Chrome is gone, this will fail immediately.
		var ok bool
		err := chromedp.Run(s.ctx, chromedp.Evaluate(`true`, &ok))
		if err == nil && ok {
			continue // Chrome is alive
		}

		slog.Warn("Chrome appears to have closed, relaunching...", "error", err)

		s.mu.Lock()
		s.pageReady = false
		// Tear down old allocator/context
		if s.cancel != nil {
			s.cancel()
		}
		if s.allocCancel != nil {
			s.allocCancel()
		}
		s.mu.Unlock()

		// Brief pause before relaunch to avoid tight loops
		time.Sleep(2 * time.Second)

		if err := s.initBrowser(); err != nil {
			slog.Error("Failed to relaunch Chrome", "error", err)
			continue
		}

		s.closeExtraTabs()

		if err := s.warmPage(); err != nil {
			slog.Error("Failed to warm page after Chrome relaunch", "error", err)
			continue
		}

		slog.Info("Chrome relaunched and page warmed successfully")
	}
}

// findChrome locates the Chrome binary on common paths.
// Override with CHROME_PATH env var if your Chrome is elsewhere.
func findChrome() string {
	candidates := []string{
		// Linux (GCE VM)
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
		}
	}

	if runtime.GOOS == "darwin" {
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	}

	for _, path := range candidates {
		if _, err := exec.LookPath(path); err == nil {
			return path
		}
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	// Fall back to PATH lookup
	if path, err := exec.LookPath("google-chrome"); err == nil {
		return path
	}
	if path, err := exec.LookPath("chrome"); err == nil {
		return path
	}

	return "google-chrome" // let chromedp handle the error
}

// buildChromeProxyServer returns just the scheme://host:port for Chrome's --proxy-server flag.
// Chrome does NOT support inline credentials in --proxy-server — auth is handled
// separately via CDP's Fetch.authRequired event.
func buildChromeProxyServer(baseURL string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("invalid proxy URL: %w", err)
	}
	return fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host), nil
}

// getProxyCredentials extracts username and password from the proxy URL,
// appending Evomi session parameters for a long-lived Canadian session.
func getProxyCredentials(baseURL string) (username, password string, err error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid proxy URL: %w", err)
	}
	username = parsed.User.Username()
	password, _ = parsed.User.Password()
	// Append Evomi session parameters: country=CA, long session lifetime (300s)
	sessionID := fmt.Sprintf("token%d", time.Now().UnixNano()%100000)
	password += fmt.Sprintf("_country-CA_session-%s_lifetime-300", sessionID)
	return username, password, nil
}

// maskProxy redacts the password from a proxy URL for safe logging.
func maskProxy(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid>"
	}
	return fmt.Sprintf("%s://%s:***@%s", parsed.Scheme, parsed.User.Username(), parsed.Host)
}
