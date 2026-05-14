package dealtypes

type Choice struct {
	Name  string
	Value string
}

const (
	SubscriptionRFD           = "rfd"
	SubscriptionEbay          = "ebay"
	SubscriptionFacebook      = "facebook"
	SubscriptionMemoryExpress = "memoryexpress"
	SubscriptionBestBuy       = "bestbuy"
	SubscriptionHardwareSwap  = "hardwareswap"

	RFDAll         = "rfd_all"
	RFDTech        = "rfd_tech"
	RFDWarmHot     = "rfd_warm_hot"
	RFDWarmHotTech = "rfd_warm_hot_tech"
	RFDHot         = "rfd_hot"
	RFDHotTech     = "rfd_hot_tech"

	EbayCAPriceDrop = "ebay_ca_price_drop"
	EbayUSPriceDrop = "ebay_us_price_drop"

	MemoryExpressWarmHot = "me_warm_hot"
	MemoryExpressHot     = "me_hot"

	BestBuyNew     = "bb_new"
	BestBuyWarmHot = "bb_warm_hot"
	BestBuyHot     = "bb_hot"
	BestBuyCompute = "bb_compute"
)

var RFDChoices = []Choice{
	{Name: "All deals", Value: RFDAll},
	{Name: "Tech only", Value: RFDTech},
	{Name: "Warm + Hot (all)", Value: RFDWarmHot},
	{Name: "Warm + Hot (tech)", Value: RFDWarmHotTech},
	{Name: "Hot only (all)", Value: RFDHot},
	{Name: "Hot only (tech)", Value: RFDHotTech},
}

var EbayChoices = []Choice{
	{Name: "Canada price drops", Value: EbayCAPriceDrop},
	{Name: "US price drops", Value: EbayUSPriceDrop},
}

var MemoryExpressChoices = []Choice{
	{Name: "Warm + Hot deals", Value: MemoryExpressWarmHot},
	{Name: "Hot deals only", Value: MemoryExpressHot},
}

var BestBuyChoices = []Choice{
	{Name: "All new listings only + AI labels", Value: BestBuyNew},
	{Name: "AI warm + hot listings/drops", Value: BestBuyWarmHot},
	{Name: "AI hot listings/drops only", Value: BestBuyHot},
	{Name: "High-compute outliers", Value: BestBuyCompute},
}

var RemoveChoices = []Choice{
	{Name: "RFD", Value: SubscriptionRFD},
	{Name: "eBay", Value: SubscriptionEbay},
	{Name: "Facebook", Value: SubscriptionFacebook},
	{Name: "Memory Express", Value: SubscriptionMemoryExpress},
	{Name: "Best Buy", Value: SubscriptionBestBuy},
}

func ActiveRemoveChoices(facebookEnabled, hardwareSwapEnabled bool) []Choice {
	out := make([]Choice, 0, len(RemoveChoices))
	for _, choice := range RemoveChoices {
		if choice.Value == SubscriptionFacebook && !facebookEnabled {
			continue
		}
		out = append(out, choice)
	}
	return out
}

func ValidSubscriptionType(value string, facebookEnabled, hardwareSwapEnabled bool) bool {
	switch value {
	case SubscriptionRFD, SubscriptionEbay, SubscriptionMemoryExpress, SubscriptionBestBuy:
		return true
	case SubscriptionFacebook:
		return facebookEnabled
	case SubscriptionHardwareSwap:
		return hardwareSwapEnabled
	default:
		return false
	}
}

func IsRFD(value string) bool {
	return containsValue(RFDChoices, value)
}

func IsEbay(value string) bool {
	return containsValue(EbayChoices, value)
}

func IsMemoryExpress(value string) bool {
	return containsValue(MemoryExpressChoices, value)
}

func IsBestBuy(value string) bool {
	return containsValue(BestBuyChoices, value)
}

func Label(value string) string {
	switch value {
	case "":
		return "all"
	case RFDAll:
		return "RFD all deals"
	case RFDTech:
		return "RFD tech deals"
	case RFDWarmHot:
		return "RFD warm + hot deals"
	case RFDWarmHotTech:
		return "RFD warm + hot tech deals"
	case RFDHot:
		return "RFD hot deals"
	case RFDHotTech:
		return "RFD hot tech deals"
	case EbayCAPriceDrop:
		return "eBay Canada price drops"
	case EbayUSPriceDrop:
		return "eBay US price drops"
	case MemoryExpressWarmHot:
		return "Memory Express warm + hot deals"
	case MemoryExpressHot:
		return "Memory Express hot deals only"
	case BestBuyNew:
		return "Best Buy all new listings only + AI labels"
	case BestBuyWarmHot:
		return "Best Buy AI warm + hot new listings and price drops"
	case BestBuyHot:
		return "Best Buy AI hot new listings and price drops only"
	case BestBuyCompute:
		return "Best Buy high-compute workstation/server outliers"
	default:
		return value
	}
}

func RFDEligible(dealType string, isTech, isWarm, isHot bool) bool {
	switch dealType {
	case RFDAll:
		return true
	case RFDTech:
		return isTech
	case RFDWarmHot:
		return isWarm || isHot
	case RFDWarmHotTech:
		return (isWarm || isHot) && isTech
	case RFDHot:
		return isHot
	case RFDHotTech:
		return isHot && isTech
	default:
		return false
	}
}

func EbayEligible(dealType, marketplace string) bool {
	switch dealType {
	case EbayCAPriceDrop:
		return marketplace == "EBAY_CA"
	case EbayUSPriceDrop:
		return marketplace == "EBAY_US"
	default:
		return false
	}
}

func MemoryExpressEligible(dealType string, isWarm, isLavaHot bool) bool {
	switch dealType {
	case MemoryExpressWarmHot:
		return isWarm || isLavaHot
	case MemoryExpressHot:
		return isLavaHot
	default:
		return false
	}
}

func BestBuyEligible(dealType string, isWarm, isLavaHot bool) bool {
	switch dealType {
	case BestBuyNew:
		return true
	case BestBuyWarmHot:
		return isWarm || isLavaHot
	case BestBuyHot:
		return isLavaHot
	case BestBuyCompute:
		return false
	default:
		return false
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
