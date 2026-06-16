package models

import "time"

const (
	OnEveryCornerAlertCorner             = "corner"
	OnEveryCornerAlertPossibleCornerGoal = "possible_corner_goal"
)

type OnEveryCornerAlert struct {
	Kind               string
	MatchName          string
	EventID            string
	StableID           string
	SourcePackage      string
	SourceApp          string
	RawTitle           string
	RawText            string
	Lines              []string
	ReceivedAt         time.Time
	CornerAt           time.Time
	GoalAt             time.Time
	SecondsAfterCorner int
	TweetText          string
	TweetURL           string
}
