package scraper

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/PuerkitoBio/goquery"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var rfdPOWFieldPattern = regexp.MustCompile(`(?m)(challenge_nonce|challenge_hmac|difficulty|difficulty_char|issued_at|cookie_duration)\s*:\s*['"]([^'"]+)['"]`)

type rfdPOWChallenge struct {
	nonce          string
	hmac           string
	difficulty     int
	difficultyChar string
	issuedAt       string
	cookieDuration time.Duration
}

func shouldStopRFDListRetry(attempt int, err error) bool {
	return err != nil && attempt >= rfdListStandardMaxRetries && !isTransientDNSFailure(err)
}

func isTransientDNSFailure(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTemporary || dnsErr.IsTimeout {
			return true
		}
		dnsText := strings.ToLower(dnsErr.Err + " " + dnsErr.Server)
		return strings.Contains(dnsText, "server misbehaving") ||
			strings.Contains(dnsText, "temporary failure") ||
			strings.Contains(dnsText, "try again")
	}

	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "lookup ") && (strings.Contains(errText, "server misbehaving") ||
		strings.Contains(errText, "temporary failure") ||
		strings.Contains(errText, "try again"))
}

// resolveLink finds an <a> element within the selection (or the selection itself),
// returning the href (resolved to absolute if relative) and text content.
func (c *Client) fetchHTMLContent(ctx context.Context, urlStr string) (*goquery.Document, error) {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL %s: %w", urlStr, err)
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("invalid URL scheme %s: only http and https allowed", parsedURL.Scheme)
	}

	hostname := parsedURL.Hostname()
	allowed := false
	for _, domain := range c.config.AllowedDomains {
		if hostname == domain {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("security violation: URL hostname %s is not in allowlist", hostname)
	}
	if c.httpClient == nil {
		return nil, errors.New("RFD HTTP client is not configured")
	}
	if c.httpClient.Jar == nil {
		jar, jarErr := cookiejar.New(nil)
		if jarErr != nil {
			return nil, fmt.Errorf("initialize RFD cookie jar: %w", jarErr)
		}
		c.httpClient.Jar = jar
	}

	profile := c.profile
	if profile.UserAgent == "" {
		profile = randomProfile()
		c.profile = profile
	}
	res, err := c.fetchHTMLResponse(ctx, urlStr, profile)
	if err != nil {
		return nil, err
	}

	if res.StatusCode == http.StatusAccepted {
		body, readErr := io.ReadAll(io.LimitReader(res.Body, rfdPOWChallengeBodyLimit))
		res.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read RFD proof-of-work challenge from %s: %w", urlStr, readErr)
		}

		challenge, parseErr := parseRFDPOWChallenge(string(body))
		if parseErr != nil {
			return nil, fmt.Errorf("failed to fetch URL %s: status code %d: %w", urlStr, res.StatusCode, parseErr)
		}
		solveStart := time.Now()
		cookieValue, attempts, solveErr := solveRFDPOWChallenge(ctx, challenge)
		if solveErr != nil {
			return nil, fmt.Errorf("solve RFD proof-of-work challenge for %s: %w", urlStr, solveErr)
		}
		c.httpClient.Jar.SetCookies(parsedURL, []*http.Cookie{{
			Name:     "pow_bypass",
			Value:    cookieValue,
			Path:     "/",
			MaxAge:   int(challenge.cookieDuration / time.Second),
			Secure:   parsedURL.Scheme == "https",
			SameSite: http.SameSiteLaxMode,
		}})
		slog.Info("Solved RFD proof-of-work challenge",
			"processor", "rfd",
			"attempts", attempts,
			"duration", time.Since(solveStart).Round(time.Millisecond).String(),
		)

		res, err = c.fetchHTMLResponse(ctx, urlStr, profile)
		if err != nil {
			return nil, err
		}
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch URL %s: status code %d", urlStr, res.StatusCode)
	}

	return goquery.NewDocumentFromReader(res.Body)
}

func (c *Client) fetchHTMLResponse(ctx context.Context, urlStr string, profile browserProfile) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request for URL %s: %w", urlStr, err)
	}

	applyStealthHeaders(req, profile)

	res, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL %s: %w", urlStr, err)
	}
	return res, nil
}

func parseRFDPOWChallenge(body string) (rfdPOWChallenge, error) {
	if !strings.Contains(body, "POW_CHALLENGE_DATA") {
		return rfdPOWChallenge{}, errors.New("HTTP 202 response was not a recognized RFD proof-of-work challenge")
	}

	fields := make(map[string]string)
	for _, match := range rfdPOWFieldPattern.FindAllStringSubmatch(body, -1) {
		fields[match[1]] = match[2]
	}
	for _, name := range []string{"challenge_nonce", "challenge_hmac", "difficulty", "difficulty_char", "issued_at"} {
		if strings.TrimSpace(fields[name]) == "" {
			return rfdPOWChallenge{}, fmt.Errorf("RFD proof-of-work challenge is missing %s", name)
		}
	}
	for _, name := range []string{"challenge_nonce", "challenge_hmac", "issued_at"} {
		if strings.ContainsAny(fields[name], "|;\r\n") {
			return rfdPOWChallenge{}, fmt.Errorf("RFD proof-of-work challenge has invalid %s", name)
		}
	}

	difficulty, err := strconv.Atoi(fields["difficulty"])
	if err != nil || difficulty < 1 || difficulty > rfdPOWMaxDifficulty {
		return rfdPOWChallenge{}, fmt.Errorf("unsupported RFD proof-of-work difficulty %q", fields["difficulty"])
	}
	difficultyChar := strings.ToLower(fields["difficulty_char"])
	if len(difficultyChar) != 1 || !strings.Contains("0123456789abcdef", difficultyChar) {
		return rfdPOWChallenge{}, fmt.Errorf("invalid RFD proof-of-work difficulty character %q", fields["difficulty_char"])
	}

	cookieSeconds := 3600
	if fields["cookie_duration"] != "" {
		parsed, parseErr := strconv.Atoi(fields["cookie_duration"])
		if parseErr != nil || parsed < 1 || parsed > 24*60*60 {
			return rfdPOWChallenge{}, fmt.Errorf("invalid RFD proof-of-work cookie duration %q", fields["cookie_duration"])
		}
		cookieSeconds = parsed
	}

	return rfdPOWChallenge{
		nonce:          fields["challenge_nonce"],
		hmac:           fields["challenge_hmac"],
		difficulty:     difficulty,
		difficultyChar: difficultyChar,
		issuedAt:       fields["issued_at"],
		cookieDuration: time.Duration(cookieSeconds) * time.Second,
	}, nil
}

func solveRFDPOWChallenge(ctx context.Context, challenge rfdPOWChallenge) (string, int, error) {
	prefix := strings.Repeat(challenge.difficultyChar, challenge.difficulty)
	for attempt := 1; attempt <= rfdPOWMaxAttempts; attempt++ {
		if attempt%1024 == 0 {
			select {
			case <-ctx.Done():
				return "", attempt, ctx.Err()
			default:
			}
		}

		counter := strconv.Itoa(attempt)
		hash := sha256.Sum256([]byte(challenge.nonce + challenge.issuedAt + counter))
		hashText := fmt.Sprintf("%x", hash)
		if strings.HasPrefix(hashText, prefix) {
			value := strings.Join([]string{challenge.nonce, challenge.issuedAt, counter, hashText, challenge.hmac}, "|")
			return value, attempt, nil
		}
	}
	return "", rfdPOWMaxAttempts, fmt.Errorf("no solution after %d attempts", rfdPOWMaxAttempts)
}
