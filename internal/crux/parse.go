package crux

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

var pageCountRe = regexp.MustCompile(`(?i)page\s+\d+\s+of\s+(\d+)|\b\d+\s*/\s*(\d+)`)

func ParseCompaniesPage(html, pageURL string) ([]Company, int, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil, 0, err
	}

	list := doc.Find(`[fs-cmsload-element="list"]`).First()
	if list.Length() == 0 {
		list = doc.Find(`.comp-list_col-list-wrap .w-dyn-items`).First()
	}
	if list.Length() == 0 {
		return nil, parsePageCount(doc), fmt.Errorf("crux company list not found")
	}

	var companies []Company
	list.Find("a.company-card").Each(func(_ int, card *goquery.Selection) {
		company, ok := parseCompanyCard(card, pageURL)
		if ok {
			companies = append(companies, company)
		}
	})
	if len(companies) == 0 {
		return companies, parsePageCount(doc), fmt.Errorf("no crux companies parsed")
	}
	return companies, parsePageCount(doc), nil
}

func parseCompanyCard(card *goquery.Selection, pageURL string) (Company, bool) {
	name := fieldValue(card, "Company Name")
	ticker := fieldValue(card, "Ticker")
	exchange, symbol, key := NormalizeTicker(ticker)
	if key == "" || name == "" {
		return Company{}, false
	}

	companyURL := strings.TrimSpace(attrOr(card, "href"))
	if companyURL != "" {
		companyURL = absoluteURL(pageURL, companyURL)
	}

	scoreText := strings.TrimSpace(card.Find(`[fs-cmssort-field="crux"]`).First().Text())
	if scoreText == "" {
		scoreText = fieldValue(card, "Crux Investor Index")
	}
	score, hasScore := parseScore(scoreText)

	return Company{
		Key:              key,
		Name:             name,
		Exchange:         exchange,
		Symbol:           symbol,
		Ticker:           key,
		URL:              companyURL,
		CruxScore:        score,
		HasCruxScore:     hasScore,
		DevelopmentStage: cleanFieldValue(fieldValue(card, "Development Stage")),
		Commodity:        cleanFieldValue(fieldValue(card, "Primary Commodity")),
		Active:           true,
	}, true
}

func fieldValue(card *goquery.Selection, label string) string {
	label = normalizeLabel(label)
	var value string
	card.Find(".cc_content").EachWithBreak(func(_ int, block *goquery.Selection) bool {
		blockLabel := normalizeLabel(block.Find(".cc_title-text").First().Text())
		if blockLabel != label {
			return true
		}
		if label == normalizeLabel("Crux Investor Index") {
			value = strings.TrimSpace(block.Find(`[fs-cmssort-field="crux"]`).First().Text())
			if value != "" {
				return false
			}
		}
		value = strings.TrimSpace(block.Find(".body").First().Text())
		if value == "" {
			value = strings.TrimSpace(block.Find(".fallback").First().Text())
		}
		return false
	})
	return cleanFieldValue(value)
}

func parsePageCount(doc *goquery.Document) int {
	text := strings.TrimSpace(doc.Find(".w-page-count").First().AttrOr("aria-label", ""))
	if text == "" {
		text = strings.TrimSpace(doc.Find(".w-page-count").First().Text())
	}
	matches := pageCountRe.FindStringSubmatch(text)
	if len(matches) == 0 {
		return 0
	}
	for _, match := range matches[1:] {
		if match == "" {
			continue
		}
		if n, err := strconv.Atoi(match); err == nil {
			return n
		}
	}
	return 0
}

func parseScore(value string) (int, bool) {
	value = cleanFieldValue(value)
	if value == "" || value == "-" || value == "–" {
		return 0, false
	}
	for _, part := range strings.Fields(value) {
		part = strings.Trim(part, "–- ")
		if n, err := strconv.Atoi(part); err == nil {
			return n, true
		}
	}
	return 0, false
}

func normalizeLabel(value string) string {
	return strings.ToLower(cleanFieldValue(value))
}

func cleanFieldValue(value string) string {
	value = strings.ReplaceAll(value, "\u00a0", " ")
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func attrOr(sel *goquery.Selection, name string) string {
	value, _ := sel.Attr(name)
	return value
}

func absoluteURL(baseURL, raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	return base.ResolveReference(parsed).String()
}
