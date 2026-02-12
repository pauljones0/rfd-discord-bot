package util

import (
	"net/url"
	"strings"
)

// NormalizeURL applies RFD-specific normalization (force HTTPS, strip tracking params, etc.)
// only if the URL's hostname is in the provided allowedDomains list.
func NormalizeURL(rawURL string, allowedDomains []string) (string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL, err
	}

	// Only apply RFD-specific normalization to known domains
	hostname := parsedURL.Hostname()
	allowed := false
	for _, d := range allowedDomains {
		if hostname == d {
			allowed = true
			break
		}
	}
	if !allowed {
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

