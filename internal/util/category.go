package util

import "strings"

// IsTechCategory checks if a given RFD category name is considered tech-related.
func IsTechCategory(category string) bool {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "computers & electronics", "cameras", "cell phones",
		"computers & tablets/ereaders", "home theatre & audio",
		"peripherals & accessories", "telecom", "televisions",
		"video games", "pc & video games":
		return true
	}
	return false
}
