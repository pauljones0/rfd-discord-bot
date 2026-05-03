package couponinfer

import "testing"

func TestInferFlatCoupon(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 10000, DiscountCents: 3000, Text: "Save C$30.00 with coupon"},
		{BaseCents: 20000, DiscountCents: 3000, Text: "Save C$30.00 with coupon"},
	})
	if got.Rule.Type != TypeFlat {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypeFlat)
	}
	if got.Rule.ValueCents != 3000 {
		t.Fatalf("value cents = %d, want 3000", got.Rule.ValueCents)
	}
	if got.MaxErrorCents != 0 || got.Confidence < 0.75 {
		t.Fatalf("max error/confidence = %d/%v, want exact high confidence", got.MaxErrorCents, got.Confidence)
	}
}

func TestInferPercentCoupon(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 10000, DiscountCents: 1000, Text: "Extra 10% off"},
		{BaseCents: 20000, DiscountCents: 2000, Text: "Extra 10% off"},
	})
	if got.Rule.Type != TypePercent {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypePercent)
	}
	if got.Rule.BasisPoints != 1000 {
		t.Fatalf("basis points = %d, want 1000", got.Rule.BasisPoints)
	}
	if got.MaxErrorCents != 0 || got.Confidence < 0.75 {
		t.Fatalf("max error/confidence = %d/%v, want exact high confidence", got.MaxErrorCents, got.Confidence)
	}
}

func TestInferCappedPercentCoupon(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 10000, DiscountCents: 2000, Text: "Extra 20% off, maximum discount of C$100"},
		{BaseCents: 30000, DiscountCents: 6000, Text: "Extra 20% off, maximum discount of C$100"},
		{BaseCents: 80000, DiscountCents: 10000, Text: "Extra 20% off, maximum discount of C$100"},
	})
	if got.Rule.Type != TypePercentCap {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypePercentCap)
	}
	if got.Rule.BasisPoints != 2000 || got.Rule.CapCents != 10000 {
		t.Fatalf("basis/cap = %d/%d, want 2000/10000", got.Rule.BasisPoints, got.Rule.CapCents)
	}
	if got.MaxErrorCents != 0 || got.Confidence < 0.75 {
		t.Fatalf("max error/confidence = %d/%v, want exact high confidence", got.MaxErrorCents, got.Confidence)
	}
}

func TestInferThresholdFlatCoupon(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 4000, DiscountCents: 0, Text: "Save C$15 on orders over C$75"},
		{BaseCents: 8000, DiscountCents: 1500, Text: "Save C$15 on orders over C$75"},
		{BaseCents: 20000, DiscountCents: 1500, Text: "Save C$15 on orders over C$75"},
	})
	if got.Rule.Type != TypeThresholdFlat {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypeThresholdFlat)
	}
	if got.Rule.ValueCents != 1500 || got.Rule.ThresholdCents != 7500 {
		t.Fatalf("value/threshold = %d/%d, want 1500/7500", got.Rule.ValueCents, got.Rule.ThresholdCents)
	}
	if got.MaxErrorCents != 0 || got.Confidence < 0.75 {
		t.Fatalf("max error/confidence = %d/%v, want exact high confidence", got.MaxErrorCents, got.Confidence)
	}
}

func TestInferAmbiguousFlatVsCappedPercent(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 50000, DiscountCents: 5000},
		{BaseCents: 60000, DiscountCents: 5000},
	})
	if got.Rule.Type != TypeAmbiguous {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypeAmbiguous)
	}
	if got.CompetingRules < 2 || !got.NeedsMoreSamples {
		t.Fatalf("competing/needs more = %d/%v, want ambiguous sample request", got.CompetingRules, got.NeedsMoreSamples)
	}
}

func TestInferAllowsOneCentRoundingNoise(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 9999, DiscountCents: 1000, Text: "10% off"},
		{BaseCents: 14999, DiscountCents: 1500, Text: "10% off"},
	})
	if got.Rule.Type != TypePercent {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypePercent)
	}
	if got.Rule.BasisPoints != 1000 {
		t.Fatalf("basis points = %d, want 1000", got.Rule.BasisPoints)
	}
	if got.MaxErrorCents > 1 {
		t.Fatalf("max error = %d, want <= 1", got.MaxErrorCents)
	}
}

func TestInferConflictingSamplesReturnUnknown(t *testing.T) {
	got := Infer([]Sample{
		{BaseCents: 10000, DiscountCents: 1000},
		{BaseCents: 20000, DiscountCents: 5000},
	})
	if got.Rule.Type != TypeUnknown {
		t.Fatalf("rule type = %q, want %q", got.Rule.Type, TypeUnknown)
	}
	if !got.NeedsMoreSamples {
		t.Fatalf("NeedsMoreSamples = false, want true")
	}
}
