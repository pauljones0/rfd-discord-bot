package util

import (
	"net/url"
	"strings"
)

// CleanReferralLink processes deal URLs to strip/replace affiliate tracking.
// bestBuyPrefix is the affiliate redirect prefix for Best Buy links.
func CleanReferralLink(rawUrl string, amazonTag string, bestBuyPrefix string) (string, bool) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl, false
	}

	switch {
	case parsedUrl.Host == "click.linksynergy.com":
		murlParam := parsedUrl.Query().Get("murl")
		if murlParam != "" {
			decodedMURL, decodeErr := url.QueryUnescape(murlParam)
			if decodeErr == nil && isHTTPURL(decodedMURL) {
				return decodedMURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "go.redirectingat.com":
		urlParam := parsedUrl.Query().Get("url")
		if urlParam != "" {
			decodedDestURL, decodeErr := url.QueryUnescape(urlParam)
			if decodeErr == nil && isHTTPURL(decodedDestURL) {
				return decodedDestURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "bestbuyca.o93x.net" && strings.HasPrefix(parsedUrl.Path, "/c/"):
		// Swap to our Best Buy affiliate link, preserving the destination product URL.
		productURL := parsedUrl.Query().Get("u")
		if productURL == "" {
			return rawUrl, false
		}
		cleanedURL := bestBuyPrefix + url.QueryEscape(productURL)
		return cleanedURL, true

	case strings.HasSuffix(parsedUrl.Host, "bestbuy.ca"):
		// Direct bestbuy.ca link - wrap it
		cleanedURL := bestBuyPrefix + url.QueryEscape(rawUrl)
		return cleanedURL, true

	case strings.Contains(parsedUrl.Host, "amazon."):
		queryParams := parsedUrl.Query()
		originalTag := queryParams.Get("tag")
		tagModified := false

		if queryParams.Has("tag") {
			if originalTag != amazonTag {
				queryParams.Del("tag")
				queryParams.Set("tag", amazonTag)
				tagModified = true
			}
		} else {
			queryParams.Set("tag", amazonTag)
			tagModified = true
		}
		if tagModified {
			parsedUrl.RawQuery = queryParams.Encode()
			return parsedUrl.String(), true
		}
		return rawUrl, false

	default:
		return rawUrl, false
	}
}

// isHTTPURL validates that a URL string has an http or https scheme.
func isHTTPURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")
}
