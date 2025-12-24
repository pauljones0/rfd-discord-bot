package util

import (
	"net/url"
	"strings"

	"golang.org/x/net/publicsuffix"
)

func NormalizeURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}
	parsedURL.Scheme = "https"
	if strings.HasPrefix(parsedURL.Host, "www.") {
		if parsedURL.Host == "www.forums.redflagdeals.com" || parsedURL.Host == "www.redflagdeals.com" {
			parsedURL.Host = strings.TrimPrefix(parsedURL.Host, "www.")
		}
	}
	if parsedURL.Host == "redflagdeals.com" {
		parsedURL.Host = "forums.redflagdeals.com"
	}
	if len(parsedURL.Path) > 1 && strings.HasSuffix(parsedURL.Path, "/") {
		parsedURL.Path = parsedURL.Path[:len(parsedURL.Path)-1]
		// Clear RawPath to ensure String() regenerates the URL path without the trailing slash
		parsedURL.RawPath = ""
	}
	queryParams := parsedURL.Query()
	utmParams := []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content", "rfd_sk", "sd", "sk"}
	for _, param := range utmParams {
		if queryParams.Has(param) {
			queryParams.Del(param)
		}
	}
	parsedURL.RawQuery = queryParams.Encode()
	return parsedURL.String(), nil
}

func GetDomain(urlStr string) string {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}

	// Use publicsuffix library for robust TLD handling (handles co.uk, co.kr, etc.)
	domain, err := publicsuffix.EffectiveTLDPlusOne(parsedURL.Hostname())
	if err != nil {
		// Fallback to simple hostname if publicsuffix fails (e.g. localhost or IP)
		return strings.TrimPrefix(parsedURL.Hostname(), "www.")
	}
	return domain
}
