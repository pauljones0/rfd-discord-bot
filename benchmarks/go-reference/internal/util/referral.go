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
		return cleanDecodedReferralTarget(rawUrl, parsedUrl.Query().Get("murl"), amazonTag, bestBuyPrefix)

	case parsedUrl.Host == "go.redirectingat.com":
		return cleanDecodedReferralTarget(rawUrl, parsedUrl.Query().Get("url"), amazonTag, bestBuyPrefix)

	case parsedUrl.Host == "bestbuyca.o93x.net" && strings.HasPrefix(parsedUrl.Path, "/c/"):
		// Swap to our Best Buy affiliate link, preserving the destination product URL.
		productURL := parsedUrl.Query().Get("u")
		if productURL == "" {
			return rawUrl, false
		}
		if bestBuyPrefix == "" {
			return productURL, productURL != rawUrl
		}
		cleanedURL := bestBuyPrefix + url.QueryEscape(productURL)
		return cleanedURL, true

	case strings.HasSuffix(parsedUrl.Host, "bestbuy.ca"):
		// Direct bestbuy.ca link - wrap it
		if bestBuyPrefix == "" {
			return rawUrl, false
		}
		cleanedURL := bestBuyPrefix + url.QueryEscape(rawUrl)
		return cleanedURL, true

	case strings.Contains(parsedUrl.Host, "amazon."):
		queryParams := parsedUrl.Query()
		if amazonTag == "" {
			if !queryParams.Has("tag") {
				return rawUrl, false
			}
			queryParams.Del("tag")
			parsedUrl.RawQuery = queryParams.Encode()
			return parsedUrl.String(), true
		}
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

	case isEbayHost(parsedUrl.Hostname()):
		cleanedURL, changed := cleanEbayAffiliateLink(rawUrl, parsedUrl)
		return cleanedURL, changed

	default:
		return rawUrl, false
	}
}

func cleanDecodedReferralTarget(rawUrl string, encodedTarget string, amazonTag string, bestBuyPrefix string) (string, bool) {
	if encodedTarget == "" {
		return rawUrl, false
	}

	// Query().Get already decoded the wrapper's parameter. A second unescape
	// changes escaped path separators and query delimiters inside the destination.
	decodedTarget := encodedTarget
	if !isHTTPURL(decodedTarget) {
		return rawUrl, false
	}

	if cleanedURL, changed := CleanReferralLink(decodedTarget, amazonTag, bestBuyPrefix); changed {
		return cleanedURL, true
	}

	return decodedTarget, true
}

func isEbayHost(host string) bool {
	host = strings.ToLower(host)
	return host == "ebay.com" || host == "ebay.ca" ||
		strings.HasSuffix(host, ".ebay.com") || strings.HasSuffix(host, ".ebay.ca")
}

func cleanEbayAffiliateLink(rawUrl string, parsedUrl *url.URL) (string, bool) {
	marketplaceHost := ""

	switch host := strings.ToLower(parsedUrl.Hostname()); {
	case host == "ebay.ca" || strings.HasSuffix(host, ".ebay.ca"):
		marketplaceHost = "www.ebay.ca"
	case host == "ebay.com" || strings.HasSuffix(host, ".ebay.com"):
		marketplaceHost = "www.ebay.com"
	default:
		return rawUrl, false
	}

	itemID := extractEbayItemID(parsedUrl)
	if itemID == "" {
		return rawUrl, false
	}

	cleanedURL := "https://" + marketplaceHost + "/itm/" + itemID
	return cleanedURL, cleanedURL != rawUrl
}

func extractEbayItemID(parsedUrl *url.URL) string {
	if matches := ebayItemRegex.FindStringSubmatch(parsedUrl.Path); len(matches) > 1 {
		return matches[1]
	}

	itemID := strings.TrimSpace(parsedUrl.Query().Get("iid"))
	if isEbayItemID(itemID) {
		return itemID
	}

	return ""
}

func isEbayItemID(itemID string) bool {
	if len(itemID) < 10 || len(itemID) > 13 {
		return false
	}

	for _, ch := range itemID {
		if ch < '0' || ch > '9' {
			return false
		}
	}

	return true
}

// isHTTPURL validates that a URL string has an http or https scheme.
func isHTTPURL(rawURL string) bool {
	return strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://")
}
