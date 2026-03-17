package scraper

import (
	"math/rand/v2"
	"net/http"
)

// browserProfile represents a realistic browser fingerprint for stealth scraping.
type browserProfile struct {
	UserAgent    string
	SecChUa      string
	SecChMobile  string
	SecChPlatform string
}

// Modern browser profiles — kept current to avoid detection by stale UA strings.
// Each profile pairs the User-Agent with matching Sec-Ch-Ua headers that a real
// browser would send together. Mismatched values are a common bot fingerprint.
var browserProfiles = []browserProfile{
	{
		UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		SecChUa:       `"Chromium";v="134", "Google Chrome";v="134", "Not:A-Brand";v="24"`,
		SecChMobile:   "?0",
		SecChPlatform: `"Windows"`,
	},
	{
		UserAgent:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		SecChUa:       `"Chromium";v="134", "Google Chrome";v="134", "Not:A-Brand";v="24"`,
		SecChMobile:   "?0",
		SecChPlatform: `"macOS"`,
	},
	{
		UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36",
		SecChUa:       `"Chromium";v="133", "Google Chrome";v="133", "Not(A:Brand";v="99"`,
		SecChMobile:   "?0",
		SecChPlatform: `"Windows"`,
	},
	{
		UserAgent:     "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/134.0.0.0 Safari/537.36",
		SecChUa:       `"Chromium";v="134", "Google Chrome";v="134", "Not:A-Brand";v="24"`,
		SecChMobile:   "?0",
		SecChPlatform: `"Linux"`,
	},
	{
		UserAgent:     "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:135.0) Gecko/20100101 Firefox/135.0",
		SecChUa:       "", // Firefox doesn't send Sec-Ch-Ua
		SecChMobile:   "",
		SecChPlatform: "",
	},
	{
		UserAgent:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15",
		SecChUa:       "", // Safari doesn't send Sec-Ch-Ua
		SecChMobile:   "",
		SecChPlatform: "",
	},
}

// randomProfile returns a random browser profile for this request.
func randomProfile() browserProfile {
	return browserProfiles[rand.IntN(len(browserProfiles))]
}

// applyStealthHeaders sets realistic browser headers on the request.
// This mimics what a real browser sends, including header ordering cues
// and the Sec-Fetch-* family that Cloudflare and similar WAFs check.
func applyStealthHeaders(req *http.Request, profile browserProfile) {
	req.Header.Set("User-Agent", profile.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	// Accept-Encoding is intentionally omitted — Go's http.Transport adds
	// "gzip" automatically and transparently decompresses the response.
	// Setting it manually would require us to handle decompression ourselves.
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("Upgrade-Insecure-Requests", "1")

	// Sec-Fetch headers — these tell the server this is a top-level navigation
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")

	// Client Hints — only Chromium-based browsers send these
	if profile.SecChUa != "" {
		req.Header.Set("Sec-Ch-Ua", profile.SecChUa)
		req.Header.Set("Sec-Ch-Ua-Mobile", profile.SecChMobile)
		req.Header.Set("Sec-Ch-Ua-Platform", profile.SecChPlatform)
	}
}
