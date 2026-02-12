package util

import (
	"net/url"
	"regexp"
	"strings"
)

// Best Buy affiliate constants — swaps any existing Best Buy affiliate link to ours.
const newBestBuyPrefix = "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="

var bestBuyRegex = regexp.MustCompile(`^https://bestbuyca\.o93x\.net/c/\d+/\d+/\d+`)

func CleanReferralLink(rawUrl string, amazonTag string) (string, bool) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl, false
	}

	switch {
	case parsedUrl.Host == "click.linksynergy.com":
		murlParam := parsedUrl.Query().Get("murl")
		if murlParam != "" {
			decodedMURL, decodeErr := url.QueryUnescape(murlParam)
			if decodeErr == nil {
				return decodedMURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "go.redirectingat.com":
		urlParam := parsedUrl.Query().Get("url")
		if urlParam != "" {
			decodedDestURL, decodeErr := url.QueryUnescape(urlParam)
			if decodeErr == nil {
				return decodedDestURL, true
			}
		}
		return rawUrl, false

	case parsedUrl.Host == "bestbuyca.o93x.net" && bestBuyRegex.MatchString(rawUrl):
		// Swap to our Best Buy affiliate link, preserving the destination product URL.
		productURL := parsedUrl.Query().Get("u")
		if productURL == "" {
			return rawUrl, false
		}
		cleanedURL := newBestBuyPrefix + url.QueryEscape(productURL)
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
		return parsedUrl.String(), tagModified

	default:
		return rawUrl, false
	}
}
