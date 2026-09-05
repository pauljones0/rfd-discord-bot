package dealquality

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/pauljones0/rfd-discord-bot/internal/models"
)

const (
	rfdMinDiscountPct   = 5.0
	rfdMinSavingsAmount = 5.0
)

var (
	currencyAmountRe = regexp.MustCompile(`(?i)(?:ca\$|c\$|us\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	amountRe         = regexp.MustCompile(`([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	percentRe        = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%`)

	rfdNegativeDealPhrases = []string{
		"above market",
		"above msrp",
		"full price",
		"inflated price",
		"negative discount",
		"no discount",
		"not a deal",
		"not warm",
		"over market",
		"over msrp",
		"overpriced",
		"price goug",
		"price hike",
		"price increase",
		"rip off",
		"ripoff",
		"scalped",
		"scalper",
	}

	rfdPositiveDiscountPhrases = []string{
		"% off",
		"after cashback",
		"after coupon",
		"after rebate",
		"all time low",
		"atl",
		"cashback",
		"clearance",
		"coupon",
		"discount",
		"lowest price",
		"markdown",
		"price drop",
		"price error",
		"promo code",
		"rebate",
		"rollback",
		"sale price",
		"save ",
		"savings",
	}
)

type RFDDiscountEvidence struct {
	Eligible        bool
	Reason          string
	CurrentPrice    float64
	OriginalPrice   float64
	SavingsAmount   float64
	DiscountPercent float64
}

func RFDWarmHotEligible(deal models.DealInfo) bool {
	return EvaluateRFDWarmHotDiscount(deal).Eligible
}

func EvaluateRFDWarmHotDiscount(deal models.DealInfo) RFDDiscountEvidence {
	text := strings.ToLower(strings.Join([]string{
		deal.Title,
		deal.Summary,
		deal.Description,
		deal.Savings,
	}, " "))

	for _, phrase := range rfdNegativeDealPhrases {
		if strings.Contains(text, phrase) {
			return RFDDiscountEvidence{Reason: "negative value language: " + phrase}
		}
	}

	current, hasCurrent := parseAmount(deal.Price)
	original, hasOriginal := parseAmount(deal.OriginalPrice)
	if hasCurrent && hasOriginal {
		if current >= original {
			return RFDDiscountEvidence{
				CurrentPrice:  current,
				OriginalPrice: original,
				Reason:        "current price is not below original price",
			}
		}
		savings := original - current
		discountPct := savings / original * 100
		if discountPct >= rfdMinDiscountPct || savings >= rfdMinSavingsAmount {
			return RFDDiscountEvidence{
				Eligible:        true,
				Reason:          "current price is below original price",
				CurrentPrice:    current,
				OriginalPrice:   original,
				SavingsAmount:   savings,
				DiscountPercent: discountPct,
			}
		}
	}

	if pct, ok := parsePercent(deal.Savings); ok && pct >= rfdMinDiscountPct {
		return RFDDiscountEvidence{Eligible: true, Reason: "savings field has discount percent", DiscountPercent: pct}
	}
	if amount, ok := parseAmount(deal.Savings); ok && amount >= rfdMinSavingsAmount {
		return RFDDiscountEvidence{Eligible: true, Reason: "savings field has discount amount", SavingsAmount: amount}
	}
	if pct, ok := parsePercent(deal.Title + " " + deal.Summary); ok && pct >= rfdMinDiscountPct {
		return RFDDiscountEvidence{Eligible: true, Reason: "title or summary has discount percent", DiscountPercent: pct}
	}

	for _, phrase := range rfdPositiveDiscountPhrases {
		if strings.Contains(text, phrase) {
			return RFDDiscountEvidence{Eligible: true, Reason: "discount language: " + phrase}
		}
	}

	return RFDDiscountEvidence{Reason: "no discount evidence"}
}

func parseAmount(text string) (float64, bool) {
	for _, re := range []*regexp.Regexp{currencyAmountRe, amountRe} {
		matches := re.FindAllStringSubmatch(text, -1)
		for _, match := range matches {
			if len(match) < 2 {
				continue
			}
			value, ok := parseNumber(match[1])
			if ok {
				return value, true
			}
		}
	}
	return 0, false
}

func parsePercent(text string) (float64, bool) {
	match := percentRe.FindStringSubmatch(text)
	if len(match) < 2 {
		return 0, false
	}
	return parseNumber(match[1])
}

func parseNumber(text string) (float64, bool) {
	text = strings.ReplaceAll(strings.TrimSpace(text), ",", "")
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}
