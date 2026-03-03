package util

import "strings"

// IsTechCategory checks if a given RFD category name is considered tech-related.
func IsTechCategory(category string) bool {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "computers & electronics", "cameras", "cell phones", "cell phones & plans",
		"computers & tablets/ereaders", "home theatre & audio",
		"peripherals & accessories", "telecom", "televisions",
		"video games", "pc & video games":
		return true
	}
	return false
}

// GetCategoryEmoji returns a suitable emoji for a given category.
// If an emoji is not yet mapped for a category, it will return "❌".
func GetCategoryEmoji(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "computers & electronics", "cameras", "cell phones", "cell phones & plans",
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
	case "financial services", "personal finance":
		return "💰"
	case "groceries":
		return "🛒"
	case "home & garden":
		return "🏡"
	case "kids & babies":
		return "👶"
	case "restaurants", "fast food":
		return "🍔"
	case "sports & fitness":
		return "⚽"
	case "travel":
		return "✈️"
	case "small business":
		return "💼"
	case "shopping discussion":
		return "🛍️"
	case "request-a-deal":
		return "❓"
	case "careers":
		return "👔"
	case "expired offers":
		return "⏳"
	}
	return "❌" // Unmapped category tag emoji
}
