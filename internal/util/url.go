package util

import (
	"net/url"
	"strings"
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
	hostname := strings.TrimPrefix(parsedURL.Hostname(), "www.")
	parts := strings.Split(hostname, ".")
	if len(parts) <= 2 {
		return hostname
	}

	// Check for two-part TLDs (e.g., co.uk)
	lastTwo := parts[len(parts)-2] + "." + parts[len(parts)-1]
	if KnownTwoPartTLDs[lastTwo] {
		if len(parts) >= 3 {
			return parts[len(parts)-3] + "." + lastTwo
		}
		return hostname
	}

	return parts[len(parts)-2] + "." + parts[len(parts)-1]
}
