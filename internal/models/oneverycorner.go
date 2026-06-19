package models

import "time"

const (
	OnEveryCornerAlertCorner             = "corner"
	OnEveryCornerAlertPossibleCornerGoal = "possible_corner_goal"
	OnEveryCornerAlertSystem             = "system"
)

type OnEveryCornerAlert struct {
	Kind               string
	MatchName          string
	Score              string
	CornerScore        string
	ScoringSide        string
	ScoringTeam        string
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
	VariantTweetText   string
	VariantTweetURL    string
	SystemSeverity     string
	SystemDetails      string
	SystemFields       []CoreSystemAlertField
}
