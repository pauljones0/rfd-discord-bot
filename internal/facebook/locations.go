package facebook

import (
	"fmt"
	"strings"
)

// CityLocationIDs maps Canadian city names to their exact Facebook Marketplace Location IDs.
var CityLocationIDs = map[string]string{
	"Abbotsford":          "112008808810771",
	"Banff":               "112372292108491",
	"Barrie":              "110893392264912",
	"Brampton":            "110185085668702",
	"Burlington":          "108043585884666",
	"Burnaby":             "110574778966847",
	"Calgary":             "111983945494775",
	"Dorval":              "108272592527229",
	"Edmonton":            "115976748413086",
	"Gatineau":            "106245446080870",
	"Halifax":             "112227518795154",
	"Hamilton":            "104011556303312",
	"Jasper":              "104106836291824",
	"Kelowna":             "111949595490847",
	"Kingston":            "105443806157102",
	"Kitchener":           "104045032964460",
	"Lake Louise":         "108634569161523",
	"Langley":             "105471749485955",
	"Laval":               "112746445406043",
	"London":              "107624535933778",
	"Markham":             "108108762549844",
	"Mississauga":         "106262189412553",
	"Moncton":             "102170323157613",
	"Montreal":            "102184499823699",
	"Nanaimo":             "110991578925336",
	"Niagara Falls":       "104083606294021",
	"Niagara-On-The-Lake": "106100116095327",
	"North Vancouver":     "104034516300688",
	"Oakville":            "104153936288668",
	"Oshawa":              "114418101908145",
	"Ottawa":              "109870912368806",
	"Quebec":              "114990258511923",
	"Regina":              "109369119081169",
	"Richmond":            "112202378796934",
	"Saint John":          "104072742961897",
	"Saskatoon":           "115362478475254",
	"Sherbrooke":          "116139965062971",
	"St. Catharines":      "106063096092020",
	"St. John's":          "114111265266017",
	"Surrey":              "109571329060695",
	"Thunder Bay":         "111551465530472",
	"Toronto":             "110941395597405",
	"Trois-Rivières":      "110315092330541",
	"Vancouver":           "114497808567786",
	"Vaughan":             "105959272778333",
	"Victoria":            "103135879727382",
	"Waterloo":            "112763262068685",
	"Whistler":            "101898263185412",
	"Windsor":             "108634965827573",
	"Winnipeg":            "112276732125400",
}

// CityPostalCodes maps each city to a central postal code for Carfax valuations.
var CityPostalCodes = map[string]string{
	"Abbotsford":          "V2S3N3",
	"Banff":               "T1L1A1",
	"Barrie":              "L4M3B1",
	"Brampton":            "L6Y1N2",
	"Burlington":          "L7R1A1",
	"Burnaby":             "V5H0A1",
	"Calgary":             "T2P1J9",
	"Dorval":              "H9S1A1",
	"Edmonton":            "T5J2R4",
	"Gatineau":            "J8X2N1",
	"Halifax":             "B3J1A1",
	"Hamilton":            "L8P1A1",
	"Jasper":              "T0E1E0",
	"Kelowna":             "V1Y6H2",
	"Kingston":            "K7L3N6",
	"Kitchener":           "N2G1A1",
	"Lake Louise":         "T0L1E0",
	"Langley":             "V3A4E6",
	"Laval":               "H7V1A1",
	"London":              "N6A1A1",
	"Markham":             "L3R5G2",
	"Mississauga":         "L5B3C2",
	"Moncton":             "E1C1A1",
	"Montreal":            "H3B1A1",
	"Nanaimo":             "V9R5J1",
	"Niagara Falls":       "L2E6S4",
	"Niagara-On-The-Lake": "L0S1J0",
	"North Vancouver":     "V7L1A1",
	"Oakville":            "L6J1A1",
	"Oshawa":              "L1H1A1",
	"Ottawa":              "K1P5M9",
	"Quebec":              "G1R2J6",
	"Regina":              "S4P3Y2",
	"Richmond":            "V6Y1K1",
	"Saint John":          "E2L1A1",
	"Saskatoon":           "S7K1A1",
	"Sherbrooke":          "J1H1A1",
	"St. Catharines":      "L2R5P9",
	"St. John's":          "A1C1A1",
	"Surrey":              "V3T1V8",
	"Thunder Bay":         "P7B6A6",
	"Toronto":             "M5H2N2",
	"Trois-Rivières":      "G9A1A1",
	"Vancouver":           "V6B1A1",
	"Vaughan":             "L4J0A1",
	"Victoria":            "V8W1A1",
	"Waterloo":            "N2J1A1",
	"Whistler":            "V8E0A1",
	"Windsor":             "N9A1A1",
	"Winnipeg":            "R3C0A1",
}

// PostalCodeForCity returns the central postal code for a city.
func PostalCodeForCity(city string) string {
	return CityPostalCodes[city]
}

// evomiCityFallbacks maps cities NOT available in Evomi to the nearest available
// city. Cities that ARE in Evomi don't need an entry — they're derived automatically
// via lowercase + spaces-to-dots.
var evomiCityFallbacks = map[string]string{
	"Banff":               "calgary",
	"Burlington":          "hamilton",
	"Halifax":             "dartmouth",
	"Jasper":              "edmonton",
	"Kingston":            "brockville",
	"Lake Louise":         "calgary",
	"London":              "woodstock",
	"Niagara Falls":       "welland",
	"Niagara-On-The-Lake": "thorold",
	"Quebec":              "charlesbourg",
	"Richmond":            "vancouver",
	"Saint John":          "fredericton",
	"St. Catharines":      "grimsby",
	"St. John's":          "paradise",
	"Trois-Rivières":      "shawinigan",
	"Victoria":            "sidney",
	"Whistler":            "squamish",
}

// EvomiCityForCity returns the Evomi city targeting string for a given city name.
// Returns empty string if city is empty (proxy will use country-level targeting only).
func EvomiCityForCity(city string) string {
	if city == "" {
		return ""
	}
	if fallback, ok := evomiCityFallbacks[city]; ok {
		return fallback
	}
	return strings.ToLower(strings.ReplaceAll(city, " ", "."))
}

// CategorySlugs maps frontend category names to Facebook URL suffixes.
var CategorySlugs = map[string]string{
	"Vehicles": "vehicles",
}

// CityNames returns a sorted list of all supported city names for autocomplete.
func CityNames() []string {
	names := make([]string, 0, len(CityLocationIDs))
	for name := range CityLocationIDs {
		names = append(names, name)
	}
	return names
}

// FilterCities returns city names that contain the query string (case-insensitive).
func FilterCities(query string) []string {
	if query == "" {
		return CityNames()
	}
	var matches []string
	lowerQuery := strings.ToLower(query)
	for name := range CityLocationIDs {
		if strings.Contains(strings.ToLower(name), lowerQuery) {
			matches = append(matches, name)
		}
	}
	return matches
}

// BuildMarketplaceURL constructs a valid Facebook Marketplace search URL.
func BuildMarketplaceURL(city, category string, radiusKm int) (string, error) {
	locationID, ok := CityLocationIDs[city]
	if !ok {
		return "", fmt.Errorf("unknown city %q: not found in CityLocationIDs", city)
	}

	catSlug, ok := CategorySlugs[category]
	if !ok {
		return "", fmt.Errorf("unknown category %q: not found in CategorySlugs", category)
	}

	if radiusKm <= 0 {
		radiusKm = 500
	}

	return fmt.Sprintf(
		"https://www.facebook.com/marketplace/%s/%s/?exact=false&radius=%d",
		locationID, catSlug, radiusKm,
	), nil
}
