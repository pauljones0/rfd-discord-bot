package util

import (
	"net/url"
	"strings"
)

// rfdDomains lists domains where NormalizeURL should force HTTPS and apply RFD-specific normalization.
var rfdDomains = []string{
	"redflagdeals.com",
	"forums.redflagdeals.com",
	"www.redflagdeals.com",
	"www.forums.redflagdeals.com",
}

func isRFDDomain(host string) bool {
	for _, d := range rfdDomains {
		if host == d {
			return true
		}
	}
	return false
}

func NormalizeURL(rawURL string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}

	// Only apply RFD-specific normalization to known RFD domains
	if !isRFDDomain(parsedURL.Hostname()) {
		return rawURL, nil
	}

	parsedURL.Scheme = "https"
	parsedURL.Host = strings.TrimPrefix(parsedURL.Host, "www.")
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
