package dealtypes

import "testing"

func TestOnEveryCornerChoicesExposeAwarenessOnly(t *testing.T) {
	if len(OnEveryCornerChoices) != 1 {
		t.Fatalf("choices = %d, want one awareness choice", len(OnEveryCornerChoices))
	}
	choice := OnEveryCornerChoices[0]
	if choice.Value != OnEveryCornerPotentialGoals {
		t.Fatalf("choice value = %q, want %q", choice.Value, OnEveryCornerPotentialGoals)
	}
	if choice.Name != "Awareness: corner goals, X failures, Scoremer issues" {
		t.Fatalf("choice name = %q", choice.Name)
	}
}

func TestIsOnEveryCornerAcceptsLegacyAllAlertsValue(t *testing.T) {
	if !IsOnEveryCorner(OnEveryCornerPotentialGoals) {
		t.Fatal("awareness value should be valid")
	}
	if !IsOnEveryCorner(OnEveryCornerAlerts) {
		t.Fatal("legacy all-alerts value should remain valid")
	}
	if IsOnEveryCorner("oneverycorner_every_corner") {
		t.Fatal("unknown OnEveryCorner value should be invalid")
	}
}

func TestOnEveryCornerLabelsUseAwarenessWording(t *testing.T) {
	for _, value := range []string{OnEveryCornerPotentialGoals, OnEveryCornerAlerts} {
		if got := Label(value); got != "OnEveryCorner awareness alerts" {
			t.Fatalf("Label(%q) = %q, want awareness label", value, got)
		}
	}
}
