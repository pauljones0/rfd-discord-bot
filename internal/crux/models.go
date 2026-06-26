package crux

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	ChangeAdded        = "added"
	ChangeDeleted      = "deleted"
	ChangeUpgraded     = "upgraded"
	ChangeDowngraded   = "downgraded"
	ChangeScoreAdded   = "score_added"
	ChangeScoreRemoved = "score_removed"
)

var docIDReplacer = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Company is the current known Crux Investor state for one traded company.
type Company struct {
	Key              string    `docstore:"key"`
	Name             string    `docstore:"name"`
	Exchange         string    `docstore:"exchange"`
	Symbol           string    `docstore:"symbol"`
	Ticker           string    `docstore:"ticker"`
	URL              string    `docstore:"url,omitempty"`
	CruxScore        int       `docstore:"cruxScore,omitempty"`
	HasCruxScore     bool      `docstore:"hasCruxScore"`
	DevelopmentStage string    `docstore:"developmentStage,omitempty"`
	Commodity        string    `docstore:"commodity,omitempty"`
	Active           bool      `docstore:"active"`
	FirstSeenAt      time.Time `docstore:"firstSeenAt"`
	LastSeenAt       time.Time `docstore:"lastSeenAt"`
	LastChangedAt    time.Time `docstore:"lastChangedAt,omitempty"`
	RemovedAt        time.Time `docstore:"removedAt,omitempty"`
}

// Change is one timestamped Crux state transition.
type Change struct {
	ID               string    `docstore:"id"`
	Type             string    `docstore:"type"`
	Key              string    `docstore:"key"`
	Name             string    `docstore:"name"`
	Exchange         string    `docstore:"exchange"`
	Symbol           string    `docstore:"symbol"`
	Ticker           string    `docstore:"ticker"`
	URL              string    `docstore:"url,omitempty"`
	OldScore         int       `docstore:"oldScore,omitempty"`
	HasOldScore      bool      `docstore:"hasOldScore"`
	NewScore         int       `docstore:"newScore,omitempty"`
	HasNewScore      bool      `docstore:"hasNewScore"`
	DevelopmentStage string    `docstore:"developmentStage,omitempty"`
	Commodity        string    `docstore:"commodity,omitempty"`
	DetectedAt       time.Time `docstore:"detectedAt"`
}

// SystemAlert is an operational Crux monitor alert, such as a scrape/parser
// failure or recovery. These go to Crux subscription channels.
type SystemAlert struct {
	Title      string
	Severity   string
	Component  string
	Details    string
	Fields     []SystemAlertField
	OccurredAt time.Time
}

type SystemAlertField struct {
	Name  string
	Value string
}

func CompanyKey(exchange, symbol string) string {
	exchange = strings.ToUpper(strings.TrimSpace(exchange))
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if exchange == "" || symbol == "" {
		return ""
	}
	return exchange + ":" + symbol
}

func NormalizeTicker(ticker string) (exchange, symbol, key string) {
	parts := strings.SplitN(strings.TrimSpace(ticker), ":", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	exchange = strings.ToUpper(strings.TrimSpace(parts[0]))
	symbol = strings.ToUpper(strings.TrimSpace(parts[1]))
	key = CompanyKey(exchange, symbol)
	return exchange, symbol, key
}

func IsCanadianExchange(exchange string) bool {
	switch strings.ToUpper(strings.TrimSpace(exchange)) {
	case "TSX", "TSXV", "CSE":
		return true
	default:
		return false
	}
}

func ChangeDocID(change Change) string {
	id := strings.TrimSpace(change.ID)
	if id == "" {
		when := change.DetectedAt.UTC()
		if when.IsZero() {
			when = time.Now().UTC()
		}
		id = fmt.Sprintf("%s_%s_%s", when.Format("20060102T150405.000000000Z"), change.Type, change.Key)
	}
	id = strings.ToLower(docIDReplacer.ReplaceAllString(id, "_"))
	id = strings.Trim(id, "_")
	if id == "" {
		return fmt.Sprintf("crux_%d", time.Now().UTC().UnixNano())
	}
	return id
}
