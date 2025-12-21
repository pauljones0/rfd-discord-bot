package util

import (
	"net/url"
	"regexp"
	"strings"
)

func CleanReferralLink(rawUrl string) (string, bool) {
	parsedUrl, err := url.Parse(rawUrl)
	if err != nil {
		return rawUrl, false
	}

	// Best Buy specific constants
	const newBestBuyPrefix = "https://bestbuyca.o93x.net/c/5215192/2035226/10221?u="
	bestBuyRegex := regexp.MustCompile(`^https://bestbuyca\.o93x\.net/c/\d+/\d+/\d+\?u=`)

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
		uIndex := strings.Index(rawUrl, "?u=")
		if uIndex == -1 {
			return rawUrl, false
		}
		productURLPart := rawUrl[uIndex+len("?u="):]
		cleanedURL := newBestBuyPrefix + productURLPart
		return cleanedURL, true

	case strings.Contains(parsedUrl.Host, "amazon."):
		queryParams := parsedUrl.Query()
		originalTag := queryParams.Get("tag")
		const newTag = "beauahrens0d-20"
		tagModified := false

		if queryParams.Has("tag") {
			if originalTag != newTag {
				queryParams.Del("tag")
				queryParams.Set("tag", newTag)
				tagModified = true
			}
		} else {
			queryParams.Set("tag", newTag)
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
