package memoryexpress

import (
	"sort"
	"strings"
)

// Stores maps Memory Express store codes to their display names.
// These are the 13 physical retail locations (excludes OnlineStore since clearance is in-store only).
var Stores = map[string]string{
	"CalNE":  "Calgary North East",
	"CalNW":  "Calgary North West",
	"CalSE":  "Calgary South East",
	"Edm1":   "Edmonton South",
	"EdmW":   "Edmonton West",
	"ABLET1": "Lethbridge Central",
	"BBBC":   "Burnaby",
	"LYBC":   "Langley",
	"VBBC":   "Vancouver",
	"BCVIC1": "Victoria Downtown",
	"WpgW":   "Winnipeg West",
	"ONETO":  "Etobicoke (Toronto)",
	"SKST":   "Saskatoon North",
}

// ValidStoreCode returns true if the given code corresponds to a known physical store.
func ValidStoreCode(code string) bool {
	_, ok := Stores[code]
	return ok
}

// StoreName returns the display name for a store code, or the code itself if unknown.
func StoreName(code string) string {
	if name, ok := Stores[code]; ok {
		return name
	}
	return code
}

// MatchingStores returns store codes whose display name contains the query (case-insensitive).
// Results are sorted alphabetically by display name.
func MatchingStores(query string) []struct {
	Code string
	Name string
} {
	query = strings.ToLower(query)
	var matches []struct {
		Code string
		Name string
	}
	for code, name := range Stores {
		if query == "" || strings.Contains(strings.ToLower(name), query) {
			matches = append(matches, struct {
				Code string
				Name string
			}{code, name})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Name < matches[j].Name
	})
	return matches
}
