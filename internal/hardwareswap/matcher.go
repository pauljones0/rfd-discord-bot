package hardwareswap

import (
	"regexp"
	"strings"
)

// Matcher provides robust keyword matching with word boundary awareness.
type Matcher struct {
	patterns map[string]*regexp.Regexp
}

func NewMatcher() *Matcher {
	return &Matcher{
		patterns: make(map[string]*regexp.Regexp),
	}
}

// Matches returns true if the corpus matches the criteria defined by mustHave, anyOf, and mustNot.
func (m *Matcher) Matches(corpus string, mustHave, anyOf, mustNot []string) bool {
	corpus = strings.ToLower(corpus)

	for _, word := range mustNot {
		if m.containsWord(corpus, word) {
			return false
		}
	}

	for _, word := range mustHave {
		if !m.containsWord(corpus, word) {
			return false
		}
	}

	if len(anyOf) > 0 {
		matchedAny := false
		for _, word := range anyOf {
			if m.containsWord(corpus, word) {
				matchedAny = true
				break
			}
		}
		if !matchedAny {
			return false
		}
	}

	return true
}

// containsWord checks if a word exists in the corpus with word boundary awareness.
func (m *Matcher) containsWord(corpus, word string) bool {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return false
	}

	re, ok := m.patterns[word]
	if !ok {
		isWordStart := regexp.MustCompile(`^[a-zA-Z0-9]`).MatchString(word)
		isWordEnd := regexp.MustCompile(`[a-zA-Z0-9]$`).MatchString(word)

		pattern := regexp.QuoteMeta(word)
		if isWordStart {
			pattern = `\b` + pattern
		} else {
			pattern = `(?:^|[^\w])` + pattern
		}
		if isWordEnd {
			pattern = pattern + `\b`
		} else {
			pattern = pattern + `(?:$|[^\w])`
		}

		re = regexp.MustCompile(`(?i)` + pattern)
		m.patterns[word] = re
	}

	return re.MatchString(corpus)
}
