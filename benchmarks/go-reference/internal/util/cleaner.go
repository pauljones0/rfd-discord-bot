package util

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	amazonASINRegex  = regexp.MustCompile(`(?:/dp/|/gp/product/)([\w0-9]+)|/[^/]+/dp/([\w0-9]+)`)
	ebayItemRegex    = regexp.MustCompile(`\/itm\/(?:[^\/]+\/)?(\d{10,13})`)
	ebayProductRegex = regexp.MustCompile(`\/p\/(\d+)`)
)

// CleanProductURL removes tracking parameters and normalizes URLs from
// Amazon, BestBuy, and eBay based on predefined rules.
func CleanProductURL(rawURL string) string {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}

	host := strings.ToLower(parsedURL.Hostname())
	cleanTrackingParams := func(params ...string) string {
		q := parsedURL.Query()
		for key := range q {
			if strings.HasPrefix(strings.ToLower(key), "utm_") {
				q.Del(key)
			}
		}
		for _, key := range params {
			q.Del(key)
		}
		parsedURL.RawQuery = q.Encode()
		return parsedURL.String()
	}

	// Clean query parameters common helper
	cleanQueryParams := func(keepParams ...string) {
		q := parsedURL.Query()
		newQ := url.Values{}
		for _, kp := range keepParams {
			if v := q.Get(kp); v != "" {
				newQ.Set(kp, v)
			}
		}
		parsedURL.RawQuery = newQ.Encode()
	}

	switch {
	case isRetailerHost(host, "amazon.com", "amazon.ca"):
		// Extract ASIN from path using regex
		matches := amazonASINRegex.FindStringSubmatch(parsedURL.Path)
		var asin string
		if len(matches) > 1 && matches[1] != "" {
			asin = matches[1]
		} else if len(matches) > 2 && matches[2] != "" {
			asin = matches[2]
		}

		if asin == "" {
			return cleanTrackingParams("tag", "ref", "ref_", "linkCode", "camp", "creative", "ascsubtag")
		}
		parsedURL.Path = "/dp/" + asin

		// Keep only th, psc, smid for Amazon
		cleanQueryParams("th", "psc", "smid")
		return parsedURL.String()

	case isEbayHost(parsedURL.Hostname()):
		// Prefer a specific item listing when present, including /p/ URLs with iid.
		iMatches := ebayItemRegex.FindStringSubmatch(parsedURL.Path)
		if len(iMatches) > 1 {
			parsedURL.Path = "/itm/" + iMatches[1]
		} else if iid := parsedURL.Query().Get("iid"); isEbayItemID(iid) {
			parsedURL.Path = "/itm/" + iid
		} else if pMatches := ebayProductRegex.FindStringSubmatch(parsedURL.Path); len(pMatches) > 1 {
			parsedURL.Path = "/p/" + pMatches[1]
		} else {
			return cleanTrackingParams("_trkparms", "_trksid", "mkcid", "mkrid", "campid", "toolid", "customid", "mkevt")
		}

		// Strip all query params
		parsedURL.RawQuery = ""
		return parsedURL.String()

	case isRetailerHost(host, "bestbuy.com", "bestbuy.ca"):
		if !strings.Contains(parsedURL.Path, "/product/") &&
			!(strings.HasPrefix(parsedURL.Path, "/site/") && strings.HasSuffix(parsedURL.Path, ".p")) {
			return cleanTrackingParams("cmp", "cmpid", "irclickid", "irgwc", "loc", "ref")
		}
		// Product paths contain the identity; search and filter URLs need their query.
		parsedURL.RawQuery = ""
		return parsedURL.String()
	}

	return rawURL
}

func isRetailerHost(host string, domains ...string) bool {
	for _, domain := range domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
