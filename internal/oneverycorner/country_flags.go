package oneverycorner

import "strings"

var countryTeamFlagOverrides = map[string]string{
	"england":  "\U0001F3F4\U000E0067\U000E0062\U000E0065\U000E006E\U000E0067\U000E007F",
	"scotland": "\U0001F3F4\U000E0067\U000E0062\U000E0073\U000E0063\U000E0074\U000E007F",
	"wales":    "\U0001F3F4\U000E0067\U000E0062\U000E0077\U000E006C\U000E0073\U000E007F",
}

var countryTeamFlagCodes = map[string]string{
	"afghanistan":                      "AF",
	"albania":                          "AL",
	"algeria":                          "DZ",
	"american samoa":                   "AS",
	"andorra":                          "AD",
	"angola":                           "AO",
	"antigua and barbuda":              "AG",
	"argentina":                        "AR",
	"armenia":                          "AM",
	"aruba":                            "AW",
	"australia":                        "AU",
	"austria":                          "AT",
	"azerbaijan":                       "AZ",
	"bahamas":                          "BS",
	"bahrain":                          "BH",
	"bangladesh":                       "BD",
	"barbados":                         "BB",
	"belarus":                          "BY",
	"belgium":                          "BE",
	"belize":                           "BZ",
	"benin":                            "BJ",
	"bermuda":                          "BM",
	"bhutan":                           "BT",
	"bolivia":                          "BO",
	"bosnia":                           "BA",
	"bosnia and herzegovina":           "BA",
	"botswana":                         "BW",
	"brazil":                           "BR",
	"brunei":                           "BN",
	"bulgaria":                         "BG",
	"burkina faso":                     "BF",
	"burundi":                          "BI",
	"cabo verde":                       "CV",
	"cambodia":                         "KH",
	"cameroon":                         "CM",
	"canada":                           "CA",
	"cape verde":                       "CV",
	"cayman islands":                   "KY",
	"central african republic":         "CF",
	"chad":                             "TD",
	"chile":                            "CL",
	"china":                            "CN",
	"china pr":                         "CN",
	"chinese taipei":                   "TW",
	"colombia":                         "CO",
	"comoros":                          "KM",
	"congo":                            "CG",
	"congo dr":                         "CD",
	"costa rica":                       "CR",
	"cote d ivoire":                    "CI",
	"cote divoire":                     "CI",
	"croatia":                          "HR",
	"cuba":                             "CU",
	"curacao":                          "CW",
	"cyprus":                           "CY",
	"czech republic":                   "CZ",
	"czechia":                          "CZ",
	"d r congo":                        "CD",
	"democratic republic of congo":     "CD",
	"denmark":                          "DK",
	"djibouti":                         "DJ",
	"dominica":                         "DM",
	"dominican republic":               "DO",
	"dpr korea":                        "KP",
	"dr congo":                         "CD",
	"east timor":                       "TL",
	"ecuador":                          "EC",
	"egypt":                            "EG",
	"el salvador":                      "SV",
	"equatorial guinea":                "GQ",
	"eritrea":                          "ER",
	"estonia":                          "EE",
	"eswatini":                         "SZ",
	"ethiopia":                         "ET",
	"faroe islands":                    "FO",
	"fiji":                             "FJ",
	"finland":                          "FI",
	"france":                           "FR",
	"french guiana":                    "GF",
	"gabon":                            "GA",
	"gambia":                           "GM",
	"georgia":                          "GE",
	"germany":                          "DE",
	"ghana":                            "GH",
	"gibraltar":                        "GI",
	"greece":                           "GR",
	"grenada":                          "GD",
	"guadeloupe":                       "GP",
	"guam":                             "GU",
	"guatemala":                        "GT",
	"guinea":                           "GN",
	"guinea bissau":                    "GW",
	"guyana":                           "GY",
	"haiti":                            "HT",
	"holland":                          "NL",
	"honduras":                         "HN",
	"hong kong":                        "HK",
	"hong kong china":                  "HK",
	"hungary":                          "HU",
	"iceland":                          "IS",
	"india":                            "IN",
	"indonesia":                        "ID",
	"iran":                             "IR",
	"iran ir":                          "IR",
	"iran islamic republic":            "IR",
	"iraq":                             "IQ",
	"ireland":                          "IE",
	"israel":                           "IL",
	"italy":                            "IT",
	"ivory coast":                      "CI",
	"jamaica":                          "JM",
	"japan":                            "JP",
	"jordan":                           "JO",
	"kazakhstan":                       "KZ",
	"kenya":                            "KE",
	"korea dpr":                        "KP",
	"korea republic":                   "KR",
	"kosovo":                           "XK",
	"kuwait":                           "KW",
	"kyrgyzstan":                       "KG",
	"lao pdr":                          "LA",
	"laos":                             "LA",
	"latvia":                           "LV",
	"lebanon":                          "LB",
	"lesotho":                          "LS",
	"liberia":                          "LR",
	"libya":                            "LY",
	"liechtenstein":                    "LI",
	"lithuania":                        "LT",
	"luxembourg":                       "LU",
	"macao":                            "MO",
	"macau":                            "MO",
	"macedonia":                        "MK",
	"madagascar":                       "MG",
	"malawi":                           "MW",
	"malaysia":                         "MY",
	"maldives":                         "MV",
	"mali":                             "ML",
	"malta":                            "MT",
	"martinique":                       "MQ",
	"mauritania":                       "MR",
	"mauritius":                        "MU",
	"mexico":                           "MX",
	"moldova":                          "MD",
	"mongolia":                         "MN",
	"montenegro":                       "ME",
	"montserrat":                       "MS",
	"morocco":                          "MA",
	"mozambique":                       "MZ",
	"myanmar":                          "MM",
	"namibia":                          "NA",
	"nepal":                            "NP",
	"netherlands":                      "NL",
	"new caledonia":                    "NC",
	"new zealand":                      "NZ",
	"nicaragua":                        "NI",
	"niger":                            "NE",
	"nigeria":                          "NG",
	"north korea":                      "KP",
	"north macedonia":                  "MK",
	"northern ireland":                 "GB",
	"norway":                           "NO",
	"oman":                             "OM",
	"pakistan":                         "PK",
	"palestine":                        "PS",
	"panama":                           "PA",
	"papua new guinea":                 "PG",
	"paraguay":                         "PY",
	"peru":                             "PE",
	"philippines":                      "PH",
	"poland":                           "PL",
	"portugal":                         "PT",
	"pr china":                         "CN",
	"puerto rico":                      "PR",
	"qatar":                            "QA",
	"republic of congo":                "CG",
	"republic of ireland":              "IE",
	"republic of korea":                "KR",
	"romania":                          "RO",
	"russia":                           "RU",
	"rwanda":                           "RW",
	"saint kitts and nevis":            "KN",
	"saint lucia":                      "LC",
	"saint vincent and the grenadines": "VC",
	"samoa":                            "WS",
	"san marino":                       "SM",
	"sao tome and principe":            "ST",
	"saudi arabia":                     "SA",
	"senegal":                          "SN",
	"serbia":                           "RS",
	"seychelles":                       "SC",
	"sierra leone":                     "SL",
	"singapore":                        "SG",
	"slovakia":                         "SK",
	"slovenia":                         "SI",
	"solomon islands":                  "SB",
	"somalia":                          "SO",
	"south africa":                     "ZA",
	"south korea":                      "KR",
	"south sudan":                      "SS",
	"spain":                            "ES",
	"sri lanka":                        "LK",
	"st kitts and nevis":               "KN",
	"st lucia":                         "LC",
	"st vincent and the grenadines":    "VC",
	"sudan":                            "SD",
	"suriname":                         "SR",
	"swaziland":                        "SZ",
	"sweden":                           "SE",
	"switzerland":                      "CH",
	"syria":                            "SY",
	"tahiti":                           "PF",
	"taiwan":                           "TW",
	"tajikistan":                       "TJ",
	"tanzania":                         "TZ",
	"thailand":                         "TH",
	"timor leste":                      "TL",
	"togo":                             "TG",
	"tonga":                            "TO",
	"trinidad and tobago":              "TT",
	"tunisia":                          "TN",
	"turkey":                           "TR",
	"turkiye":                          "TR",
	"turkmenistan":                     "TM",
	"turks and caicos islands":         "TC",
	"u s a":                            "US",
	"uae":                              "AE",
	"uganda":                           "UG",
	"ukraine":                          "UA",
	"united arab emirates":             "AE",
	"united states":                    "US",
	"united states of america":         "US",
	"uruguay":                          "UY",
	"us":                               "US",
	"usa":                              "US",
	"uzbekistan":                       "UZ",
	"vanuatu":                          "VU",
	"venezuela":                        "VE",
	"viet nam":                         "VN",
	"vietnam":                          "VN",
	"yemen":                            "YE",
	"zambia":                           "ZM",
	"zimbabwe":                         "ZW",
}

var countryTeamNameReplacer = strings.NewReplacer(
	"’", " ",
	"'", " ",
	".", "",
	",", "",
	"(", " ",
	")", " ",
	"[", " ",
	"]", " ",
	"-", " ",
	"_", " ",
	"/", " ",
	"&", " and ",
	"á", "a",
	"à", "a",
	"â", "a",
	"ä", "a",
	"ã", "a",
	"å", "a",
	"ç", "c",
	"é", "e",
	"è", "e",
	"ê", "e",
	"ë", "e",
	"í", "i",
	"ì", "i",
	"î", "i",
	"ï", "i",
	"ñ", "n",
	"ó", "o",
	"ò", "o",
	"ô", "o",
	"ö", "o",
	"õ", "o",
	"ú", "u",
	"ù", "u",
	"û", "u",
	"ü", "u",
	"ý", "y",
	"ÿ", "y",
)

var countryTeamDecorators = map[string]struct{}{
	"football": {},
	"men":      {},
	"mens":     {},
	"national": {},
	"soccer":   {},
	"team":     {},
	"u":        {},
	"w":        {},
	"women":    {},
	"womens":   {},
}

func countryTeamFlagOrName(team string) string {
	team = strings.TrimSpace(team)
	if team == "" {
		return ""
	}
	if flag := countryTeamFlagEmoji(team); flag != "" {
		return flag
	}
	return team
}

func countryTeamFlagEmoji(team string) string {
	key := normalizeCountryTeamKey(team)
	if key == "" {
		return ""
	}
	if flag := countryTeamFlagOverrides[key]; flag != "" {
		return flag
	}
	if code := countryTeamFlagCodes[key]; code != "" {
		return countryCodeFlagEmoji(code)
	}
	return ""
}

func normalizeCountryTeamKey(team string) string {
	key := strings.ToLower(strings.TrimSpace(team))
	key = countryTeamNameReplacer.Replace(key)
	words := strings.Fields(key)
	for len(words) > 0 {
		last := words[len(words)-1]
		if _, ok := countryTeamDecorators[last]; ok || isCountryTeamAgeSuffix(last) || isCountryTeamNumericSuffix(last) {
			words = words[:len(words)-1]
			continue
		}
		break
	}
	return strings.Join(words, " ")
}

func isCountryTeamAgeSuffix(value string) bool {
	if len(value) < 2 || value[0] != 'u' {
		return false
	}
	for _, r := range value[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isCountryTeamNumericSuffix(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func countryCodeFlagEmoji(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) != 2 {
		return ""
	}
	var b strings.Builder
	for _, r := range code {
		if r < 'A' || r > 'Z' {
			return ""
		}
		b.WriteRune(0x1F1E6 + r - 'A')
	}
	return b.String()
}
