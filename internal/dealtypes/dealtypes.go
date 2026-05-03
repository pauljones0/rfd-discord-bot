package dealtypes

type Choice struct {
	Name  string
	Value string
}

var RFDChoices = []Choice{
	{Name: "All deals", Value: "rfd_all"},
	{Name: "Tech only", Value: "rfd_tech"},
	{Name: "Warm + Hot (all)", Value: "rfd_warm_hot"},
	{Name: "Warm + Hot (tech)", Value: "rfd_warm_hot_tech"},
	{Name: "Hot only (all)", Value: "rfd_hot"},
	{Name: "Hot only (tech)", Value: "rfd_hot_tech"},
}

var EbayChoices = []Choice{
	{Name: "Canada price drops", Value: "ebay_ca_price_drop"},
	{Name: "US price drops", Value: "ebay_us_price_drop"},
}

var MemoryExpressChoices = []Choice{
	{Name: "Warm + Hot deals", Value: "me_warm_hot"},
	{Name: "Hot deals only", Value: "me_hot"},
}

var BestBuyChoices = []Choice{
	{Name: "All new listings + AI labels", Value: "bb_new"},
	{Name: "AI warm + hot deals only", Value: "bb_warm_hot"},
	{Name: "AI hot deals only", Value: "bb_hot"},
}

var RemoveChoices = []Choice{
	{Name: "RFD", Value: "rfd"},
	{Name: "eBay", Value: "ebay"},
	{Name: "Facebook", Value: "facebook"},
	{Name: "Memory Express", Value: "memoryexpress"},
	{Name: "Best Buy", Value: "bestbuy"},
}

func ActiveRemoveChoices(facebookEnabled, hardwareSwapEnabled bool) []Choice {
	out := make([]Choice, 0, len(RemoveChoices))
	for _, choice := range RemoveChoices {
		if choice.Value == "facebook" && !facebookEnabled {
			continue
		}
		out = append(out, choice)
	}
	return out
}

func ValidSubscriptionType(value string, facebookEnabled, hardwareSwapEnabled bool) bool {
	switch value {
	case "rfd", "ebay", "memoryexpress", "bestbuy":
		return true
	case "facebook":
		return facebookEnabled
	case "hardwareswap":
		return hardwareSwapEnabled
	default:
		return false
	}
}

func IsRFD(value string) bool {
	return containsValue(RFDChoices, value)
}

func IsEbay(value string) bool {
	return value == "ebay_price_drop" || containsValue(EbayChoices, value)
}

func IsMemoryExpress(value string) bool {
	return containsValue(MemoryExpressChoices, value)
}

func IsBestBuy(value string) bool {
	return containsValue(BestBuyChoices, value)
}

func IsLegacySetup(value string) bool {
	return IsRFD(value) || value == "ebay_price_drop" || value == "warm_hot_all" || value == "hot_all"
}

func Label(value string) string {
	switch value {
	case "":
		return "all"
	case "rfd_all":
		return "RFD all deals"
	case "rfd_tech":
		return "RFD tech deals"
	case "rfd_warm_hot":
		return "RFD warm + hot deals"
	case "rfd_warm_hot_tech":
		return "RFD warm + hot tech deals"
	case "rfd_hot":
		return "RFD hot deals"
	case "rfd_hot_tech":
		return "RFD hot tech deals"
	case "ebay_ca_price_drop":
		return "eBay Canada price drops"
	case "ebay_us_price_drop":
		return "eBay US price drops"
	case "ebay_price_drop":
		return "all eBay price drops"
	case "me_warm_hot":
		return "Memory Express warm + hot deals"
	case "me_hot":
		return "Memory Express hot deals only"
	case "bb_new":
		return "Best Buy all new listings + AI labels"
	case "bb_warm_hot":
		return "Best Buy AI warm + hot deals"
	case "bb_hot":
		return "Best Buy AI hot deals only"
	default:
		return value
	}
}

func containsValue(choices []Choice, value string) bool {
	for _, choice := range choices {
		if choice.Value == value {
			return true
		}
	}
	return false
}
