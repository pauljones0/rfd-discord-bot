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
	pageCouponFixedRe               = regexp.MustCompile(`(?i)(?:(?:save|get|extra|coupon|discount)\s*(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)|(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)\s*(?:off|coupon|discount|savings))`)
	pageCouponPercentRe             = regexp.MustCompile(`(?i)(?:save|get|extra)?\s*([0-9]{1,2})\s*%\s*off`)
	pageCouponCapRe                 = regexp.MustCompile(`(?i)(?:max(?:imum)?(?: discount)?(?:imum)?(?: of)?|up to)\s*(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{2})?)`)
	pageCouponCodeRe                = regexp.MustCompile(`(?i)(?:code|coupon code|use code|with code)\s*[: ]+\s*([A-Z0-9][A-Z0-9_-]{2,24})`)
	priceDetailsBareCodeRe          = regexp.MustCompile(`(?i)\b(?:coupon|promo(?:tion)?|discounts?)\s+([A-Z0-9][A-Z0-9_-]{3,24})\b`)
	priceDetailsAmountCodeRe        = regexp.MustCompile(`(?i)[-−]\s*(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?\s+([A-Z0-9][A-Z0-9_-]{3,24})\b\s+(?:automatically|applied|during|checkout)`)
	pageCouponExpiryRe              = regexp.MustCompile(`(?i)(?:ends|expires|valid until|valid through)\s+([A-Za-z]{3,9}\s+\d{1,2},?\s+\d{4}|\d{1,2}/\d{1,2}/\d{2,4})`)
	priceDetailsMarkerRe            = regexp.MustCompile(`(?i)(price\s+details|item\s+price|order\s+total|subtotal|seller\s+coupon|store\s+coupon|coupon\s+savings)`)
	priceDetailsNegativeDiscountRe  = regexp.MustCompile(`(?i)((?:seller\s+|store\s+)?(?:coupon|coupons?|promo(?:tion)?|discounts?|savings)[^$]{0,90}?[-−]\s*(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?))`)
	priceDetailsCouponLabelAmountRe = regexp.MustCompile(`(?i)((?:seller\s+|store\s+)?(?:coupon|coupons?|promo(?:tion)?|discounts?)[^$]{0,90}?(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?))`)
	priceDetailsAmountCouponLabelRe = regexp.MustCompile(`(?i)((?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)[^a-zA-Z0-9]{0,12}(?:seller\s+|store\s+)?(?:coupon|coupons?|promo(?:tion)?|discounts?|savings))`)
	priceDetailsDiscountedPriceRe   = regexp.MustCompile(`(?i)(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s+with\s+coupon\s+code`)
	priceDetailsFormulaHintRe       = regexp.MustCompile(`(?i)(\d{1,2}(?:\.\d{1,2})?)\s*%\s*off|(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*off`)
	priceDetailsSignatureCleanupRe  = regexp.MustCompile(`(?i)(?:[-−]?\s*)?(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*[0-9][0-9,]*(?:\.[0-9]{1,2})?`)
	priceDetailsSignatureNonWordRe  = regexp.MustCompile(`[^a-z0-9%$]+`)
	pageOriginalPriceRe             = regexp.MustCompile(`(?i)(?:list\s+price|was|original\s+price)[:\s]+(?:us\s*\$|c\s*\$|ca\s*\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
)

// PageCoupon is a buyer-visible coupon discovered from an eBay listing page.
type PageCoupon struct {
	DiscountAmount float64
	DiscountType   string
	DiscountValue  float64
	MaxDiscount    float64
	Code           string
	Message        string
	EvidenceText   string
	ExpiresAt      time.Time
	Scope          string
	Signature      string
	Confidence     float64
	OriginalPrice  float64
}

func (c PageCoupon) snapshot(source string) couponSnapshot {
	return couponSnapshot{
		DiscountAmount: c.DiscountAmount,
		OriginalPrice:  c.OriginalPrice,
		Code:           c.Code,
		Message:        c.Message,
		Source:         source,
		Signature:      c.Signature,
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

	var originalPrice float64
	if match := pageOriginalPriceRe.FindStringSubmatch(normalized); len(match) >= 2 {
		originalPrice = parseCouponAmount(match[1])
	}

	coupon := PageCoupon{}
	if priceDetailsCoupon := extractPriceDetailsCoupon(normalized, basePrice); priceDetailsCoupon.DiscountAmount > 0 {
		coupon = priceDetailsCoupon
	} else {
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
		coupon = best
	}

	if coupon.DiscountAmount <= 0 {
		if originalPrice > 0 {
			return PageCoupon{OriginalPrice: originalPrice}
		}
		return PageCoupon{}
	}

	if coupon.Code == "" {
		if codeMatch := pageCouponCodeRe.FindStringSubmatch(normalized); len(codeMatch) >= 2 {
			coupon.Code = normalizeCouponCode(codeMatch[1])
		}
	}
	coupon.OriginalPrice = originalPrice
	coupon.ExpiresAt = parseCouponExpiry(normalized)
	coupon.Scope = inferCouponScope(normalized)
	coupon.EvidenceText = couponEvidenceWindow(normalized, coupon.Message)
	coupon.Confidence = couponConfidence(coupon, normalized)
	coupon.Signature = NormalizeCouponSignature(coupon)
	return coupon
}

type priceDetailsDiscount struct {
	amount float64
	label  string
	start  int
	end    int
}

func extractPriceDetailsCoupon(text string, basePrice float64) PageCoupon {
	if !priceDetailsMarkerRe.MatchString(text) {
		return PageCoupon{}
	}

	var discounts []priceDetailsDiscount
	for _, re := range []*regexp.Regexp{
		priceDetailsNegativeDiscountRe,
		priceDetailsCouponLabelAmountRe,
		priceDetailsAmountCouponLabelRe,
	} {
		for _, match := range re.FindAllStringSubmatchIndex(text, -1) {
			if len(match) < 6 {
				continue
			}
			raw := text[match[2]:match[3]]
			amount := parseCouponAmount(text[match[4]:match[5]])
			if amount <= 0 {
				continue
			}
			if basePrice > 0 && amount >= basePrice {
				continue
			}
			discounts = appendPriceDetailsDiscount(discounts, priceDetailsDiscount{
				amount: amount,
				label:  cleanPriceDetailsLabel(raw),
				start:  match[0],
				end:    match[1],
			})
		}
	}
	if len(discounts) == 0 && basePrice > 0 {
		if match := priceDetailsDiscountedPriceRe.FindStringSubmatchIndex(text); len(match) >= 4 {
			discountedPrice := parseCouponAmount(text[match[2]:match[3]])
			discount := roundCents(basePrice - discountedPrice)
			if discountedPrice > 0 && discount > 0 && discount < basePrice {
				discounts = append(discounts, priceDetailsDiscount{
					amount: discount,
					label:  "coupon code",
					start:  match[0],
					end:    match[1],
				})
			}
		}
	}
	if len(discounts) == 0 {
		return PageCoupon{}
	}

	var total float64
	var labels []string
	for _, discount := range discounts {
		total += discount.amount
		if discount.label != "" {
			labels = append(labels, discount.label)
		}
	}
	total = roundCents(total)
	if total <= 0 || (basePrice > 0 && total >= basePrice) {
		return PageCoupon{}
	}

	message := strings.Join(uniqueStrings(labels), "; ")
	if message == "" {
		message = "Price details coupon savings"
	}
	if hint := priceDetailsFormulaHintRe.FindString(text); hint != "" && !strings.Contains(strings.ToLower(message), strings.ToLower(hint)) {
		message = strings.TrimSpace(message + " (" + hint + ")")
	}
	anchor := message
	if len(labels) > 0 {
		anchor = labels[0]
	}

	coupon := PageCoupon{
		DiscountAmount: total,
		DiscountType:   "fixed",
		DiscountValue:  total,
		Message:        message,
		EvidenceText:   couponEvidenceWindow(text, anchor),
		ExpiresAt:      parseCouponExpiry(text),
		Scope:          inferCouponScope(text),
		Confidence:     0.86,
	}
	if codeMatch := pageCouponCodeRe.FindStringSubmatch(text); len(codeMatch) >= 2 {
		coupon.Code = normalizeCouponCode(codeMatch[1])
		if coupon.Code != "" {
			coupon.Confidence += 0.04
		}
	} else if codeMatch := priceDetailsBareCodeRe.FindStringSubmatch(message); len(codeMatch) >= 2 {
		coupon.Code = normalizeCouponCode(codeMatch[1])
		if coupon.Code != "" {
			coupon.Confidence += 0.04
		}
	} else if codeMatch := priceDetailsAmountCodeRe.FindStringSubmatch(text); len(codeMatch) >= 2 {
		coupon.Code = normalizeCouponCode(codeMatch[1])
		if coupon.Code != "" {
			coupon.Confidence += 0.04
		}
	}
	if coupon.Scope == "store" {
		coupon.Confidence += 0.05
	}
	if coupon.Confidence > 0.95 {
		coupon.Confidence = 0.95
	}
	coupon.Signature = priceDetailsSignature(coupon, labels)
	return coupon
}

func appendPriceDetailsDiscount(discounts []priceDetailsDiscount, candidate priceDetailsDiscount) []priceDetailsDiscount {
	for _, existing := range discounts {
		overlaps := candidate.start < existing.end && existing.start < candidate.end
		sameNearAmount := existing.amount == candidate.amount && absInt(existing.start-candidate.start) < 40
		if overlaps || sameNearAmount {
			return discounts
		}
	}
	return append(discounts, candidate)
}

func cleanPriceDetailsLabel(raw string) string {
	label := priceDetailsSignatureCleanupRe.ReplaceAllString(raw, "")
	label = strings.Join(strings.Fields(label), " ")
	label = strings.Trim(label, " :-•|")
	if len(label) > 80 {
		label = strings.TrimSpace(label[:80])
	}
	return label
}

func priceDetailsSignature(coupon PageCoupon, labels []string) string {
	if coupon.Code != "" {
		return "price-details|" + strings.ToLower(coupon.Code)
	}
	parts := make([]string, 0, len(labels))
	for _, label := range uniqueStrings(labels) {
		label = strings.ToLower(priceDetailsSignatureCleanupRe.ReplaceAllString(label, ""))
		label = priceDetailsSignatureNonWordRe.ReplaceAllString(label, "-")
		label = strings.Trim(label, "-")
		if label != "" {
			parts = append(parts, label)
		}
	}
	if len(parts) == 0 {
		return "price-details|coupon"
	}
	return "price-details|" + strings.Join(parts, "|")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, value)
	}
	return out
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func couponEvidenceWindow(text, anchor string) string {
	if strings.TrimSpace(anchor) == "" {
		return text
	}
	lowerText := strings.ToLower(text)
	lowerAnchor := strings.ToLower(anchor)
	index := strings.Index(lowerText, lowerAnchor)
	if index < 0 {
		return text
	}
	start := index - 180
	if start < 0 {
		start = 0
	}
	end := index + len(anchor) + 220
	if end > len(text) {
		end = len(text)
	}
	return strings.TrimSpace(text[start:end])
}

func normalizeCouponCode(raw string) string {
	code := strings.ToUpper(strings.TrimSpace(raw))
	code = strings.TrimSuffix(code, "SEE")
	code = strings.TrimSuffix(code, "DETAILS")
	switch code {
	case "", "AND", "THE", "USE", "CODE", "OFF", "SAVE", "GET", "WITH", "COUPON", "SAVINGS", "PRICE", "DETAIL", "DETAILS":
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
