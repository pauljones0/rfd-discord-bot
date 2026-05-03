package scrapelab

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteReports writes JSON and Markdown evidence tables for lab results.
func WriteReports(outDir string, results []Result) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	jsonBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), jsonBytes, 0o644); err != nil {
		return err
	}

	var md strings.Builder
	md.WriteString("# Scrape Lab Results\n\n")
	md.WriteString("| Site | Target | Backend | Env | Status | Block | Items | Coupon | Duration | Verdict | Sample | Error |\n")
	md.WriteString("| --- | --- | --- | --- | ---: | --- | ---: | --- | ---: | --- | --- | --- |\n")
	for _, r := range results {
		coupon := ""
		if r.CouponDiscount > 0 {
			coupon = fmt.Sprintf("$%.2f", r.CouponDiscount)
			if r.CouponCode != "" {
				coupon += " `" + escapeMD(r.CouponCode) + "`"
			}
		}
		sample := ""
		if r.SamplePath != "" {
			sample = filepath.ToSlash(r.SamplePath)
		}
		md.WriteString(fmt.Sprintf(
			"| %s | %s | %s | %s | %d | %s | %d | %s | %dms | %s | %s | %s |\n",
			escapeMD(r.Site),
			escapeMD(firstNonEmpty(r.Name, r.URL)),
			escapeMD(r.Backend),
			escapeMD(r.Environment),
			r.StatusCode,
			escapeMD(r.BlockSignal),
			r.ParsedItemCount,
			escapeMD(coupon),
			r.Duration.Milliseconds(),
			escapeMD(r.Verdict),
			escapeMD(sample),
			escapeMD(r.Error),
		))
	}
	return os.WriteFile(filepath.Join(outDir, "results.md"), []byte(md.String()), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func escapeMD(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	return value
}
