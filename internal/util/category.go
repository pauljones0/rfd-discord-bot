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

// GetCategoryEmoji returns a suitable emoji for a given category.
func GetCategoryEmoji(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "computers & electronics", "cameras", "cell phones",
		"computers & tablets/ereaders", "home theatre & audio",
		"peripherals & accessories", "telecom", "televisions",
		"video games", "pc & video games":
		return "💻"
	case "apparel":
		return "👕"
	case "automotive":
		return "🚗"
	case "beauty & wellness":
		return "💄"
	case "entertainment":
		return "🍿"
	case "financial services":
		return "💰"
	case "groceries":
		return "🛒"
	case "home & garden":
		return "🏡"
	case "kids & babies":
		return "👶"
	case "restaurants":
		return "🍔"
	case "sports & fitness":
		return "⚽"
	case "travel":
		return "✈️"
	}
	return "🏷️" // Default tag emoji
}
