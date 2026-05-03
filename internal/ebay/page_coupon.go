package ebay

import (
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var (
	pageCouponFixedRe   = regexp.MustCompile(`(?i)(?:(?:save|get|extra|coupon|discount)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)|(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)\s*(?:off|coupon|discount|savings))`)
	pageCouponPercentRe = regexp.MustCompile(`(?i)(?:save|get|extra)?\s*([0-9]{1,2})\s*%\s*off`)
	pageCouponCapRe     = regexp.MustCompile(`(?i)(?:max(?:imum)?(?: discount)?(?:imum)?(?: of)?|up to)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	pageCouponCodeRe    = regexp.MustCompile(`(?i)(?:code|coupon code|use code|with code)\s*[: ]+\s*([A-Z0-9][A-Z0-9_-]{2,24})`)
)

// PageCoupon is a buyer-visible coupon discovered from an eBay listing page.
type PageCoupon struct {
	DiscountAmount float64
	Code           string
	Message        string
}

func (c PageCoupon) snapshot(source string) couponSnapshot {
	return couponSnapshot{
		DiscountAmount: c.DiscountAmount,
		Code:           c.Code,
		Message:        c.Message,
		Source:         source,
	}
}

// ExtractPageCoupon parses buyer-visible coupon text from an eBay listing page.
// It intentionally returns a single best discount because the price pipeline does
// not assume coupon stacking unless eBay exposes that in a structured API.
func ExtractPageCoupon(html string, basePrice float64) PageCoupon {
	text := pageVisibleText(html)
	normalized := strings.Join(strings.Fields(text), " ")
	if normalized == "" {
		return PageCoupon{}
	}

	best := PageCoupon{}
	if match := pageCouponPercentRe.FindStringSubmatch(normalized); len(match) >= 2 && basePrice > 0 {
		if percent, err := strconv.ParseFloat(match[1], 64); err == nil && percent > 0 {
			discount := basePrice * percent / 100
			if capMatch := pageCouponCapRe.FindStringSubmatch(normalized); len(capMatch) >= 2 {
				if capAmount := parseCouponAmount(capMatch[1]); capAmount > 0 && capAmount < discount {
					discount = capAmount
				}
			}
			best.DiscountAmount = roundCents(discount)
			best.Message = strings.TrimSpace(match[0])
		}
	}

	for _, match := range pageCouponFixedRe.FindAllStringSubmatch(normalized, -1) {
		discount := parseCouponAmount(firstNonEmpty(match[1:]...))
		if basePrice > 0 && discount >= basePrice {
			continue
		}
		if discount <= best.DiscountAmount {
			continue
		}
		best.DiscountAmount = discount
		best.Message = strings.TrimSpace(match[0])
	}

	if best.DiscountAmount <= 0 {
		return PageCoupon{}
	}

	if codeMatch := pageCouponCodeRe.FindStringSubmatch(normalized); len(codeMatch) >= 2 {
		best.Code = strings.ToUpper(strings.TrimSpace(codeMatch[1]))
	}
	return best
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func pageVisibleText(html string) string {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return html
	}
	doc.Find("script,style,noscript,svg").Remove()
	return doc.Text()
}

func parseCouponAmount(raw string) float64 {
	raw = strings.ReplaceAll(raw, ",", "")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return roundCents(value)
}

func roundCents(value float64) float64 {
	return math.Round(value*100) / 100
}
