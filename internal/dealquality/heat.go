package dealquality

import "github.com/pauljones0/rfd-discord-bot/internal/models"

// HeatScore weights comments twice because they represent deeper engagement.
func HeatScore(likes, comments, views int) float64 {
	if views == 0 {
		return 0
	}
	return (float64(max(likes, 0)) + 2*float64(max(comments, 0))) / float64(views)
}

// IsWarm and IsHot evaluate community engagement. Callers also require discount
// evidence for warm/hot alerts; discussion volume alone does not establish value.
func IsWarm(deal models.DealInfo) bool { return engaged(deal, 0.05, 15) }
func IsHot(deal models.DealInfo) bool  { return engaged(deal, 0.20, 40) }

func engaged(deal models.DealInfo, ratio float64, withoutViews int) bool {
	likes, comments, views, hasViews := deal.EngagementStats()
	if likes < 2 {
		return false
	}
	if hasViews {
		return HeatScore(likes, comments, views) > ratio
	}
	return max(likes, 0)+2*max(comments, 0) >= withoutViews
}
