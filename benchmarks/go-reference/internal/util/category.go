package util

import "strings"

// IsTechCategory checks if a given RFD category name is considered tech-related.
func IsTechCategory(category string) bool {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	case "computers & electronics", "cameras", "cell phones", "cell phones & plans",
		"computers & tablets/ereaders", "home theatre & audio",
		"peripherals & accessories", "telecom", "televisions",
		"video games", "pc & video games", "equipment":
		return true
	}
	return false
}

// GetCategoryEmoji returns a suitable emoji for a given category.
// If an emoji is not yet mapped for a category, it will return "❌".
func GetCategoryEmoji(category string) string {
	cat := strings.ToLower(strings.TrimSpace(category))
	switch cat {
	// Tech & Electronics
	case "computers & electronics", "cameras", "cell phones", "cell phones & plans",
		"computers & tablets/ereaders", "home theatre & audio",
		"peripherals & accessories", "telecom", "televisions",
		"video games", "pc & video games":
		return "💻"

	// Apparel
	case "apparel", "baby apparel", "children's apparel", "men's apparel",
		"men's clothing", "men's shoes", "women's apparel", "women's clothing",
		"women's shoes", "clothing & accessories":
		return "👕"

	// Automotive
	case "automotive", "auto parts & accessories", "auto services", "motor vehicles":
		return "🚗"

	// Beauty & Wellness
	case "beauty & wellness", "beauty supplies & personal care", "salons & spas":
		return "💄"

	// Entertainment
	case "entertainment", "books, music, movies, magazines", "events & attractions":
		return "🍿"

	// Financial Services
	case "financial services", "personal finance", "banking & investing",
		"credit cards", "insurance", "mortgages & loans":
		return "💰"

	// Food & Restaurants
	case "groceries", "coffee & desserts":
		return "🛒"
	case "restaurants", "fast food", "restaurants & bars":
		return "🍔"

	// Home & Garden
	case "home & garden", "appliances", "furniture", "home decor",
		"home improvement & tools", "home services & repairs", "outdoors & patio":
		return "🏡"

	// Kids & Babies
	case "kids & babies", "baby needs", "toys & games":
		return "👶"

	// Sports & Fitness
	case "sports & fitness", "gyms & related services":
		return "⚽"

	// Travel
	case "travel", "flights", "hotels", "rail & bus", "vacations & cruises", "car rentals":
		return "✈️"

	// Office, Services, Pets & Others
	case "small business", "office supplies":
		return "💼"
	case "pets":
		return "🐾"
	case "school supplies":
		return "🎒"
	case "shopping discussion":
		return "🛍️"
	case "request-a-deal":
		return "❓"
	case "careers", "services":
		return "👔"
	case "expired offers":
		return "⏳"
	case "other", "equipment":
		return "🏷️"
	}
	return "❌" // Unmapped category tag emoji
}
