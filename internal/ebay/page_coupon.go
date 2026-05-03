package ebay

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

var (
	pageCouponFixedRe   = regexp.MustCompile(`(?i)(?:(?:save|get|extra|coupon|discount)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)|(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)\s*(?:off|coupon|discount|savings))`)
	pageCouponPercentRe = regexp.MustCompile(`(?i)(?:save|get|extra)?\s*([0-9]{1,2})\s*%\s*off`)
	pageCouponCapRe     = regexp.MustCompile(`(?i)(?:max(?:imum)?(?: discount)?(?:imum)?(?: of)?|up to)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	pageCouponCodeRe    = regexp.MustCompile(`(?i)(?:code|coupon code|use code|with code)\s*[: ]+\s*([A-Z0-9][A-Z0-9_-]{2,24})`)
	pageCouponExpiryRe  = regexp.MustCompile(`(?i)(?:ends|expires|valid until|valid through)\s+([A-Za-z]{3,9}\s+\d{1,2},?\s+\d{4}|\d{1,2}/\d{1,2}/\d{2,4})`)
)

// PageCoupon is a buyer-visible coupon discovered from an eBay listing page.
type PageCoupon struct {
	DiscountAmount float64
	DiscountType   string
	DiscountValue  float64
	MaxDiscount    float64
	Code           string
	Message        string
	ExpiresAt      time.Time
	Scope          string
	Signature      string
	Confidence     float64
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
			maxDiscount := 0.0
			if capMatch := pageCouponCapRe.FindStringSubmatch(normalized); len(capMatch) >= 2 {
				if capAmount := parseCouponAmount(capMatch[1]); capAmount > 0 && capAmount < discount {
					discount = capAmount
					maxDiscount = capAmount
				}
			}
			best.DiscountAmount = roundCents(discount)
			best.DiscountType = "percent"
			best.DiscountValue = percent
			best.MaxDiscount = maxDiscount
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
		best.DiscountType = "fixed"
		best.DiscountValue = discount
		best.MaxDiscount = 0
		best.Message = strings.TrimSpace(match[0])
	}

	if best.DiscountAmount <= 0 {
		return PageCoupon{}
	}

	if codeMatch := pageCouponCodeRe.FindStringSubmatch(normalized); len(codeMatch) >= 2 {
		best.Code = normalizeCouponCode(codeMatch[1])
	}
	best.ExpiresAt = parseCouponExpiry(normalized)
	best.Scope = inferCouponScope(normalized)
	best.Confidence = couponConfidence(best, normalized)
	best.Signature = NormalizeCouponSignature(best)
	return best
}

func normalizeCouponCode(raw string) string {
	code := strings.ToUpper(strings.TrimSpace(raw))
	code = strings.TrimSuffix(code, "SEE")
	code = strings.TrimSuffix(code, "DETAILS")
	switch code {
	case "", "AND", "THE", "USE", "CODE", "OFF", "SAVE", "GET", "WITH", "COUPON":
		return ""
	default:
		return code
	}
}

func NormalizeCouponSignature(coupon PageCoupon) string {
	if coupon.DiscountType == "" || coupon.DiscountAmount <= 0 {
		return "none"
	}
	parts := []string{coupon.DiscountType, strconv.FormatFloat(coupon.DiscountValue, 'f', 2, 64)}
	if coupon.MaxDiscount > 0 {
		parts = append(parts, "cap", strconv.FormatFloat(coupon.MaxDiscount, 'f', 2, 64))
	}
	if coupon.Code != "" {
		parts = append(parts, strings.ToLower(coupon.Code))
	}
	return strings.Join(parts, "|")
}

func inferCouponScope(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "seller's store"), strings.Contains(lower, "seller store"),
		strings.Contains(lower, "everything in this store"), strings.Contains(lower, "entire store"),
		strings.Contains(lower, "storewide"), strings.Contains(lower, "store-wide"):
		return "store"
	case strings.Contains(lower, "selected items"), strings.Contains(lower, "eligible items"):
		return "unknown"
	default:
		return "unknown"
	}
}

func couponConfidence(coupon PageCoupon, text string) float64 {
	if coupon.DiscountAmount <= 0 {
		return 0
	}
	confidence := 0.55
	if coupon.Code != "" {
		confidence += 0.15
	}
	if coupon.Scope == "store" {
		confidence += 0.2
	}
	if !coupon.ExpiresAt.IsZero() {
		confidence += 0.1
	}
	if confidence > 0.95 {
		return 0.95
	}
	return confidence
}

func parseCouponExpiry(text string) time.Time {
	match := pageCouponExpiryRe.FindStringSubmatch(text)
	if len(match) < 2 {
		return time.Time{}
	}
	raw := strings.TrimSpace(strings.ReplaceAll(match[1], ",", ""))
	for _, layout := range []string{"January 2 2006", "Jan 2 2006", "1/2/2006", "01/02/2006", "1/2/06"} {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed
		}
	}
	return time.Time{}
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
