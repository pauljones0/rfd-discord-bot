package dealtypes

type Choice struct{ Name, Value string }

const (
	RFDAll         = "rfd_all"
	RFDTech        = "rfd_tech"
	RFDWarmHot     = "rfd_warm_hot"
	RFDWarmHotTech = "rfd_warm_hot_tech"
	RFDHot         = "rfd_hot"
	RFDHotTech     = "rfd_hot_tech"
)

var RFDChoices = []Choice{
	{"All deals", RFDAll}, {"Tech only", RFDTech}, {"Warm + Hot (all)", RFDWarmHot},
	{"Warm + Hot (tech)", RFDWarmHotTech}, {"Hot only (all)", RFDHot}, {"Hot only (tech)", RFDHotTech},
}

func IsRFD(value string) bool {
	for _, c := range RFDChoices {
		if c.Value == value {
			return true
		}
	}
	return false
}
func Label(value string) string {
	for _, c := range RFDChoices {
		if c.Value == value {
			return c.Name
		}
	}
	return value
}
func RFDEligible(kind string, tech, warm, hot bool) bool {
	switch kind {
	case RFDAll:
		return true
	case RFDTech:
		return tech
	case RFDWarmHot:
		return warm || hot
	case RFDWarmHotTech:
		return (warm || hot) && tech
	case RFDHot:
		return hot
	case RFDHotTech:
		return hot && tech
	}
	return false
}
