package crux

import (
	"testing"
	"time"
)

func TestDiffCompaniesDetectsScoreChangesAndDeletions(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	existing := map[string]Company{
		"TSXV:AAA": {Key: "TSXV:AAA", Ticker: "TSXV:AAA", Exchange: "TSXV", Symbol: "AAA", Name: "Alpha", CruxScore: 4, HasCruxScore: true, Active: true, FirstSeenAt: now.Add(-time.Hour)},
		"TSX:BBB":  {Key: "TSX:BBB", Ticker: "TSX:BBB", Exchange: "TSX", Symbol: "BBB", Name: "Beta", CruxScore: 7, HasCruxScore: true, Active: true, FirstSeenAt: now.Add(-time.Hour)},
		"CSE:CCC":  {Key: "CSE:CCC", Ticker: "CSE:CCC", Exchange: "CSE", Symbol: "CCC", Name: "Gamma", CruxScore: 6, HasCruxScore: true, Active: true, FirstSeenAt: now.Add(-time.Hour)},
	}
	current := map[string]Company{
		"TSXV:AAA": {Key: "TSXV:AAA", Ticker: "TSXV:AAA", Exchange: "TSXV", Symbol: "AAA", Name: "Alpha", CruxScore: 5, HasCruxScore: true, Active: true, LastSeenAt: now},
		"TSX:BBB":  {Key: "TSX:BBB", Ticker: "TSX:BBB", Exchange: "TSX", Symbol: "BBB", Name: "Beta", CruxScore: 3, HasCruxScore: true, Active: true, LastSeenAt: now},
		"TSXV:DDD": {Key: "TSXV:DDD", Ticker: "TSXV:DDD", Exchange: "TSXV", Symbol: "DDD", Name: "Delta", CruxScore: 8, HasCruxScore: true, Active: true, LastSeenAt: now},
	}

	_, changes := diffCompanies(existing, current, now, false)
	got := map[string]string{}
	for _, change := range changes {
		got[change.Key] = change.Type
	}
	want := map[string]string{"TSXV:AAA": ChangeUpgraded, "TSX:BBB": ChangeDowngraded, "TSXV:DDD": ChangeAdded, "CSE:CCC": ChangeDeleted}
	if len(got) != len(want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
	for key, typ := range want {
		if got[key] != typ {
			t.Fatalf("change[%s] = %q, want %q (all=%#v)", key, got[key], typ, got)
		}
	}
}

func TestDiffCompaniesBaselineDoesNotEmitAdditions(t *testing.T) {
	now := time.Now()
	current := map[string]Company{
		"TSXV:AAA": {Key: "TSXV:AAA", Ticker: "TSXV:AAA", Exchange: "TSXV", Symbol: "AAA", Name: "Alpha", CruxScore: 5, HasCruxScore: true, Active: true},
	}
	_, changes := diffCompanies(nil, current, now, true)
	if len(changes) != 0 {
		t.Fatalf("baseline changes = %#v, want none", changes)
	}
}
