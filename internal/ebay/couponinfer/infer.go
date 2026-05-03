package couponinfer

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	TypeUnknown          = "unknown"
	TypeAmbiguous        = "ambiguous"
	TypeFlat             = "flat"
	TypePercent          = "percent"
	TypePercentCap       = "percent_cap"
	TypeThresholdFlat    = "threshold_flat"
	TypeThresholdPercent = "threshold_percent"

	defaultMaxErrorCents = int64(2)
)

var (
	percentRe   = regexp.MustCompile(`(?i)(\d{1,2}(?:\.\d{1,2})?)\s*%\s*off`)
	capRe       = regexp.MustCompile(`(?i)(?:max(?:imum)?(?: discount)?(?: of)?|up to)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	thresholdRe = regexp.MustCompile(`(?i)(?:over|orders? over|min(?:imum)?(?: spend| purchase)?(?: of)?|when you spend|spend)\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)`)
	fixedRe     = regexp.MustCompile(`(?i)(?:save|get|extra|coupon|discount)?\s*(?:c\$|ca\$|\$)\s*([0-9][0-9,]*(?:\.[0-9]{1,2})?)\s*(?:off|coupon|discount|savings)?`)
)

// Sample is one observed listing/coupon outcome, expressed in integer cents.
type Sample struct {
	BaseCents     int64
	DiscountCents int64
	Text          string
}

// Rule describes the inferred seller coupon formula.
type Rule struct {
	Type           string
	ValueCents     int64
	BasisPoints    int64
	CapCents       int64
	ThresholdCents int64
}

// Result is the selected coupon rule plus confidence and ambiguity metadata.
type Result struct {
	Rule                Rule
	Confidence          float64
	MaxErrorCents       int64
	TotalErrorCents     int64
	CompetingRules      int
	NeedsMoreSamples    bool
	NextSamplePriceHint string
	PositiveSampleCount int
	ObservedSampleCount int
}

type candidate struct {
	rule            Rule
	maxErrorCents   int64
	totalErrorCents int64
	textSupport     int
	complexity      int
}

type textHints struct {
	fixedCents     []int64
	basisPoints    []int64
	capCents       []int64
	thresholdCents []int64
}

// Infer selects the most likely coupon rule from observed listing samples.
func Infer(samples []Sample) Result {
	normalized := normalizeSamples(samples)
	result := Result{ObservedSampleCount: len(normalized)}
	if len(normalized) == 0 {
		result.Rule.Type = TypeUnknown
		result.NeedsMoreSamples = true
		result.NextSamplePriceHint = "sample at least two listings from the seller"
		return result
	}

	hints := collectTextHints(normalized)
	candidates := enumerateCandidates(normalized, hints)
	if len(candidates) == 0 {
		result.Rule.Type = TypeUnknown
		result.NeedsMoreSamples = true
		result.NextSamplePriceHint = "sample listings with visible coupon text"
		return result
	}

	scored := make([]candidate, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		key := c.rule.Signature("")
		if seen[key] {
			continue
		}
		seen[key] = true
		c.maxErrorCents, c.totalErrorCents = scoreRule(c.rule, normalized)
		scored = append(scored, c)
	}
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].maxErrorCents != scored[j].maxErrorCents {
			return scored[i].maxErrorCents < scored[j].maxErrorCents
		}
		if scored[i].totalErrorCents != scored[j].totalErrorCents {
			return scored[i].totalErrorCents < scored[j].totalErrorCents
		}
		if scored[i].textSupport != scored[j].textSupport {
			return scored[i].textSupport > scored[j].textSupport
		}
		return scored[i].complexity < scored[j].complexity
	})

	best := scored[0]
	result.Rule = best.rule
	result.MaxErrorCents = best.maxErrorCents
	result.TotalErrorCents = best.totalErrorCents
	result.PositiveSampleCount = positiveSampleCount(normalized)

	if best.maxErrorCents > defaultMaxErrorCents {
		result.Rule.Type = TypeUnknown
		result.Confidence = 0
		result.NeedsMoreSamples = true
		result.CompetingRules = len(scored)
		result.NextSamplePriceHint = "coupon observations conflict; refresh more seller samples"
		return result
	}

	result.CompetingRules = countCompetingRules(best, scored)
	if result.CompetingRules > 1 && best.textSupport == 0 {
		result.Rule.Type = TypeAmbiguous
		result.Confidence = 0.5
		result.NeedsMoreSamples = true
		result.NextSamplePriceHint = nextSampleHint(normalized)
		return result
	}

	result.Confidence = confidence(best, result.PositiveSampleCount, len(normalized), result.CompetingRules)
	result.NeedsMoreSamples = result.Confidence < 0.75 || result.PositiveSampleCount < 2
	if result.NeedsMoreSamples {
		result.NextSamplePriceHint = nextSampleHint(normalized)
	}
	return result
}

func (r Rule) DiscountCents(baseCents int64) int64 {
	if baseCents <= 0 {
		return 0
	}
	if r.ThresholdCents > 0 && baseCents < r.ThresholdCents {
		return 0
	}

	var discount int64
	switch r.Type {
	case TypeFlat, TypeThresholdFlat:
		discount = r.ValueCents
	case TypePercent, TypePercentCap, TypeThresholdPercent:
		discount = roundDiv(baseCents*r.BasisPoints, 10000)
		if r.CapCents > 0 && discount > r.CapCents {
			discount = r.CapCents
		}
	default:
		return 0
	}
	if discount >= baseCents {
		return 0
	}
	return discount
}

func (r Rule) Signature(code string) string {
	parts := []string{r.Type}
	switch r.Type {
	case TypeFlat, TypeThresholdFlat:
		parts = append(parts, centsString(r.ValueCents))
	case TypePercent, TypePercentCap, TypeThresholdPercent:
		parts = append(parts, basisPointsString(r.BasisPoints))
		if r.CapCents > 0 {
			parts = append(parts, "cap", centsString(r.CapCents))
		}
	default:
		return r.Type
	}
	if r.ThresholdCents > 0 {
		parts = append(parts, "min", centsString(r.ThresholdCents))
	}
	if strings.TrimSpace(code) != "" {
		parts = append(parts, strings.ToLower(strings.TrimSpace(code)))
	}
	return strings.Join(parts, "|")
}

func normalizeSamples(samples []Sample) []Sample {
	out := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if sample.BaseCents <= 0 || sample.DiscountCents < 0 {
			continue
		}
		if sample.DiscountCents >= sample.BaseCents {
			continue
		}
		out = append(out, sample)
	}
	return out
}

func enumerateCandidates(samples []Sample, hints textHints) []candidate {
	fixedValues := uniquePositive(append(hints.fixedCents, observedDiscounts(samples)...))
	percentValues := uniquePositive(append(hints.basisPoints, observedBasisPoints(samples)...))
	capValues := uniquePositive(append(hints.capCents, repeatedPositiveDiscounts(samples)...))
	thresholdValues := uniquePositive(append(hints.thresholdCents, inferredThresholds(samples)...))

	var candidates []candidate
	for _, fixed := range fixedValues {
		candidates = append(candidates, candidate{rule: Rule{Type: TypeFlat, ValueCents: fixed}, textSupport: containsInt(hints.fixedCents, fixed), complexity: 1})
		for _, threshold := range thresholdValues {
			candidates = append(candidates, candidate{rule: Rule{Type: TypeThresholdFlat, ValueCents: fixed, ThresholdCents: threshold}, textSupport: containsInt(hints.fixedCents, fixed) + containsInt(hints.thresholdCents, threshold), complexity: 2})
		}
	}
	for _, bps := range percentValues {
		candidates = append(candidates, candidate{rule: Rule{Type: TypePercent, BasisPoints: bps}, textSupport: containsInt(hints.basisPoints, bps), complexity: 1})
		for _, capCents := range capValues {
			candidates = append(candidates, candidate{rule: Rule{Type: TypePercentCap, BasisPoints: bps, CapCents: capCents}, textSupport: containsInt(hints.basisPoints, bps) + containsInt(hints.capCents, capCents), complexity: 2})
		}
		for _, threshold := range thresholdValues {
			candidates = append(candidates, candidate{rule: Rule{Type: TypeThresholdPercent, BasisPoints: bps, ThresholdCents: threshold}, textSupport: containsInt(hints.basisPoints, bps) + containsInt(hints.thresholdCents, threshold), complexity: 2})
		}
	}
	return candidates
}

func scoreRule(rule Rule, samples []Sample) (int64, int64) {
	var maxErr, totalErr int64
	for _, sample := range samples {
		err := abs64(rule.DiscountCents(sample.BaseCents) - sample.DiscountCents)
		if err > maxErr {
			maxErr = err
		}
		totalErr += err
	}
	return maxErr, totalErr
}

func countCompetingRules(best candidate, candidates []candidate) int {
	count := 0
	for _, c := range candidates {
		if c.maxErrorCents <= defaultMaxErrorCents && c.totalErrorCents <= best.totalErrorCents+defaultMaxErrorCents {
			count++
		}
	}
	return count
}

func confidence(best candidate, positives, observed, competitors int) float64 {
	score := 0.62
	if positives >= 2 {
		score += 0.15
	}
	if observed >= 3 {
		score += 0.05
	}
	if best.textSupport > 0 {
		score += 0.12
	}
	if best.maxErrorCents == 0 {
		score += 0.05
	}
	if competitors <= 1 {
		score += 0.05
	} else {
		score -= math.Min(0.2, float64(competitors-1)*0.04)
	}
	if score < 0 {
		return 0
	}
	if score > 0.98 {
		return 0.98
	}
	return score
}

func collectTextHints(samples []Sample) textHints {
	var hints textHints
	for _, sample := range samples {
		text := sample.Text
		if text == "" {
			continue
		}
		for _, match := range percentRe.FindAllStringSubmatch(text, -1) {
			if len(match) >= 2 {
				if bps := parsePercentBasisPoints(match[1]); bps > 0 {
					hints.basisPoints = append(hints.basisPoints, bps)
				}
			}
		}
		for _, match := range capRe.FindAllStringSubmatch(text, -1) {
			if len(match) >= 2 {
				if cents := parseAmountCents(match[1]); cents > 0 {
					hints.capCents = append(hints.capCents, cents)
				}
			}
		}
		for _, match := range thresholdRe.FindAllStringSubmatch(text, -1) {
			if len(match) >= 2 {
				if cents := parseAmountCents(match[1]); cents > 0 {
					hints.thresholdCents = append(hints.thresholdCents, cents)
				}
			}
		}
		for _, match := range fixedRe.FindAllStringSubmatch(text, -1) {
			if len(match) < 2 {
				continue
			}
			cents := parseAmountCents(match[1])
			if cents <= 0 || containsInt(hints.capCents, cents) == 1 || containsInt(hints.thresholdCents, cents) == 1 {
				continue
			}
			hints.fixedCents = append(hints.fixedCents, cents)
		}
	}
	hints.fixedCents = uniquePositive(hints.fixedCents)
	hints.basisPoints = uniquePositive(hints.basisPoints)
	hints.capCents = uniquePositive(hints.capCents)
	hints.thresholdCents = uniquePositive(hints.thresholdCents)
	return hints
}

func observedDiscounts(samples []Sample) []int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		if sample.DiscountCents > 0 {
			values = append(values, sample.DiscountCents)
		}
	}
	return values
}

func observedBasisPoints(samples []Sample) []int64 {
	values := make([]int64, 0, len(samples))
	for _, sample := range samples {
		if sample.BaseCents > 0 && sample.DiscountCents > 0 {
			values = append(values, roundDiv(sample.DiscountCents*10000, sample.BaseCents))
		}
	}
	return values
}

func repeatedPositiveDiscounts(samples []Sample) []int64 {
	counts := make(map[int64]int)
	for _, sample := range samples {
		if sample.DiscountCents > 0 {
			counts[sample.DiscountCents]++
		}
	}
	var values []int64
	for cents, count := range counts {
		if count >= 2 {
			values = append(values, cents)
		}
	}
	return values
}

func inferredThresholds(samples []Sample) []int64 {
	var maxZero, minPositive int64
	for _, sample := range samples {
		if sample.DiscountCents == 0 && sample.BaseCents > maxZero {
			maxZero = sample.BaseCents
		}
		if sample.DiscountCents > 0 && (minPositive == 0 || sample.BaseCents < minPositive) {
			minPositive = sample.BaseCents
		}
	}
	var values []int64
	if maxZero > 0 && minPositive > 0 && maxZero < minPositive {
		values = append(values, maxZero+1, minPositive)
	}
	return values
}

func positiveSampleCount(samples []Sample) int {
	count := 0
	for _, sample := range samples {
		if sample.DiscountCents > 0 {
			count++
		}
	}
	return count
}

func nextSampleHint(samples []Sample) string {
	var minPositive, maxPositive int64
	for _, sample := range samples {
		if sample.DiscountCents <= 0 {
			continue
		}
		if minPositive == 0 || sample.BaseCents < minPositive {
			minPositive = sample.BaseCents
		}
		if sample.BaseCents > maxPositive {
			maxPositive = sample.BaseCents
		}
	}
	if minPositive == 0 {
		return "sample a listing that shows the coupon"
	}
	if maxPositive > minPositive {
		return fmt.Sprintf("sample below %s or above %s", centsString(minPositive), centsString(maxPositive))
	}
	return fmt.Sprintf("sample a listing far from %s", centsString(minPositive))
}

func uniquePositive(values []int64) []int64 {
	seen := make(map[int64]bool, len(values))
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func containsInt(values []int64, target int64) int {
	for _, value := range values {
		if value == target {
			return 1
		}
	}
	return 0
}

func parseAmountCents(raw string) int64 {
	clean := strings.ReplaceAll(strings.TrimSpace(raw), ",", "")
	value, err := strconv.ParseFloat(clean, 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(math.Round(value * 100))
}

func parsePercentBasisPoints(raw string) int64 {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || value <= 0 {
		return 0
	}
	return int64(math.Round(value * 100))
}

func roundDiv(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	return (n + d/2) / d
}

func abs64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func centsString(cents int64) string {
	return strconv.FormatFloat(float64(cents)/100, 'f', 2, 64)
}

func basisPointsString(bps int64) string {
	return strconv.FormatFloat(float64(bps)/100, 'f', 2, 64)
}
