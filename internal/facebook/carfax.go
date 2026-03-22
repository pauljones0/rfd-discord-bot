package facebook

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/playwright-community/playwright-go"
)

// CarfaxClient automates the Carfax Canada valuation form using Playwright.
type CarfaxClient struct {
	pm *BrowserManager
}

// TrimPicker is called with the available trim options from Carfax and returns
// the best option to select. This allows the caller (e.g. Gemini) to make an
// informed choice based on what's actually available rather than guessing blindly.
type TrimPicker func(ctx context.Context, year int, make, model string, options []string) string

const (
	// jsReadOptions reads all non-placeholder options from a select element found by label.
	jsReadOptions = `async (label) => {
		let selectEl = document.querySelector('select[aria-label="' + label + '"]');
		if (!selectEl) {
			const fallbacks = {
				'Trim': '- Select Trim -',
				'Engine': '- Select Engine -',
				'Drivetrain': '- Select Drivetrain -',
				'Transmission': '- Select Transmission -',
				'Body Style': '- Select Body Style -'
			};
			if (fallbacks[label]) {
				selectEl = document.querySelector('select[aria-label="' + fallbacks[label] + '"]');
			}
		}
		if (!selectEl) {
			const labels = Array.from(document.querySelectorAll('label, div, span')).filter(el => el.innerText && el.innerText.trim() === label);
			for (const l of labels) {
				const s = l.parentElement ? l.parentElement.querySelector('select') : null;
				if (s) { selectEl = s; break; }
			}
		}
		if (!selectEl) return [];
		let retries = 5;
		while (selectEl.disabled && retries > 0) {
			await new Promise(r => setTimeout(r, 500));
			retries--;
		}
		return Array.from(selectEl.options).slice(1).map(o => o.text);
	}`

	jsExtractValue = `() => {
		const paragraphs = Array.from(document.querySelectorAll('p, div, span'));
		const privateRangeLabel = paragraphs.find(p => p.innerText.includes('Estimated Private Range') || p.innerText.includes('Estimated Trade-In Range'));
		if (privateRangeLabel) {
			const nextParagraph = privateRangeLabel.nextElementSibling;
			if (nextParagraph) {
				return nextParagraph.innerText;
			}
			const parent = privateRangeLabel.parentElement;
			if (parent) {
				return parent.innerText;
			}
		}

		const match = document.body.innerText.match(/\$\d{1,3}(?:,\d{3})*\s*-\s*\$\d{1,3}(?:,\d{3})*/);
		return match ? match[0] : null;
	}`

	jsSelectFuzzy = `async ([label, targetText]) => {
		const labels = Array.from(document.querySelectorAll('label, div, span')).filter(el => el.innerText && el.innerText.trim() === label);
		let selectEl = null;

		selectEl = document.querySelector('select[aria-label="' + label + '"]');

		if (!selectEl) {
			for (const l of labels) {
				const s = l.parentElement ? l.parentElement.querySelector('select') : null;
				if (s) { selectEl = s; break; }
			}
		}

		if (!selectEl) {
			const fallbacks = {
				'Trim': '- Select Trim -',
				'Engine': '- Select Engine -',
				'Drivetrain': '- Select Drivetrain -',
				'Transmission': '- Select Transmission -',
				'Body Style': '- Select Body Style -'
			};
			if (fallbacks[label]) {
				selectEl = document.querySelector('select[aria-label="' + fallbacks[label] + '"]');
			}
		}

		if (!selectEl) return "Select element not found for " + label;

		let retries = 5;
		while (selectEl.disabled && retries > 0) {
			await new Promise(r => setTimeout(r, 500));
			retries--;
		}

		if (selectEl.disabled) return "Select element disabled for " + label;

		const opts = Array.from(selectEl.options);
		if (selectEl.selectedIndex > 0 && !targetText) return null;

		let bestIdx = -1;
		if (targetText) {
			const cleanTarget = targetText.toLowerCase().replace(/[^a-z0-9]/g, '');
			for(let i=1; i<opts.length; i++) {
				if (opts[i].text.toLowerCase().replace(/[^a-z0-9]/g, '') === cleanTarget) {
					bestIdx = i; break;
				}
			}
			if (bestIdx === -1) {
				for(let i=1; i<opts.length; i++) {
					if (opts[i].text.toLowerCase().includes(targetText.toLowerCase())) {
						bestIdx = i; break;
					}
				}
			}
		}

		if (bestIdx === -1 && opts.length > 1) {
			bestIdx = 1;
		}

		if (bestIdx !== -1) {
			selectEl.selectedIndex = bestIdx;
			selectEl.dispatchEvent(new Event('change', { bubbles: true }));
			return null;
		}

		return "No matching option found for " + label;
	}`
)

// NewCarfaxClient creates a new Carfax valuation client.
func NewCarfaxClient(pm *BrowserManager) *CarfaxClient {
	return &CarfaxClient{pm: pm}
}

// GetValue navigates the Carfax valuation page using Playwright.
// If pickTrim is non-nil, it reads the available trim options from Carfax after
// filling Year/Make/Model and lets the caller choose from the real options.
func (c *CarfaxClient) GetValue(ctx context.Context, year int, make, model, trim, engine, transmission, drivetrain, bodyStyle, postalCode string, odometer int, pickTrim TrimPicker) (float64, error) {
	start := time.Now()
	slog.Info("Carfax valuation starting",
		"processor", "facebook",
		"year", year,
		"make", make,
		"model", model,
		"trim", trim,
		"engine", engine,
		"transmission", transmission,
		"drivetrain", drivetrain,
		"body_style", bodyStyle,
		"odometer", odometer,
		"postal", postalCode,
	)

	bCtx, err := c.pm.NewContext("")
	if err != nil {
		return 0, fmt.Errorf("failed to create playwright context: %w", err)
	}
	defer bCtx.Close()

	page, err := bCtx.NewPage()
	if err != nil {
		return 0, fmt.Errorf("failed to create playwright page for carfax: %w", err)
	}
	defer page.Close()

	page.SetDefaultTimeout(30000)

	_, err = page.Goto("https://www.carfax.ca/whats-my-car-worth/car-value", playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to navigate to carfax value page: %w", err)
	}

	if err := c.selectFuzzy(page, "Year", fmt.Sprintf("%d", year)); err != nil {
		return 0, fmt.Errorf("failed to select year: %w", err)
	}
	if err := c.selectFuzzy(page, "Make", make); err != nil {
		return 0, fmt.Errorf("failed to select make: %w", err)
	}
	if err := c.selectFuzzy(page, "Model", model); err != nil {
		return 0, fmt.Errorf("failed to select model: %w", err)
	}

	cleanPostal := strings.ReplaceAll(strings.TrimSpace(postalCode), " ", "")
	postalLoc := page.GetByRole("textbox", playwright.PageGetByRoleOptions{Name: "Postal Code"})
	if err := postalLoc.Fill(cleanPostal); err != nil {
		return 0, fmt.Errorf("failed to fill postal code: %w", err)
	}

	odometerLoc := page.GetByRole("textbox", playwright.PageGetByRoleOptions{Name: "Odometer"})
	if err := odometerLoc.Fill(fmt.Sprintf("%d", odometer)); err != nil {
		return 0, fmt.Errorf("failed to fill odometer: %w", err)
	}

	continueBtn := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Continue"})
	if err := continueBtn.Click(); err != nil {
		return 0, fmt.Errorf("failed to click continue: %w", err)
	}

	trimSelector := "select[aria-label='- Select Trim -'], select[aria-label='Trim']"
	_, err = page.WaitForSelector(trimSelector, playwright.PageWaitForSelectorOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(15000),
	})
	if err != nil {
		return 0, fmt.Errorf("timeout waiting for step 2: %w", err)
	}

	// Read available trim options and let the caller pick the best one
	selectedTrim := trim
	if pickTrim != nil {
		trimOpts, _ := c.readOptions(page, "Trim")
		if len(trimOpts) > 0 {
			selectedTrim = pickTrim(ctx, year, make, model, trimOpts)
			slog.Info("Carfax trim selection",
				"processor", "facebook",
				"gemini_trim", trim,
				"selected_trim", selectedTrim,
				"available_trims", trimOpts,
			)
		}
	}

	if err := c.selectFuzzy(page, "Trim", selectedTrim); err != nil {
		return 0, fmt.Errorf("failed to select trim: %w", err)
	}
	// After selecting trim, other fields cascade — use fuzzy match with Gemini's
	// initial guess as a hint, falling back to the first available option.
	if err := c.selectFuzzy(page, "Engine", engine); err != nil {
		return 0, fmt.Errorf("failed to select engine: %w", err)
	}
	if err := c.selectFuzzy(page, "Drivetrain", drivetrain); err != nil {
		return 0, fmt.Errorf("failed to select drivetrain: %w", err)
	}
	if err := c.selectFuzzy(page, "Transmission", transmission); err != nil {
		return 0, fmt.Errorf("failed to select transmission: %w", err)
	}
	if err := c.selectFuzzy(page, "Body Style", bodyStyle); err != nil {
		return 0, fmt.Errorf("failed to select body style: %w", err)
	}

	getValueBtn := page.GetByRole("button", playwright.PageGetByRoleOptions{Name: "Get Value Range"})
	if _, err := page.WaitForFunction("btn => !btn.disabled", getValueBtn, playwright.PageWaitForFunctionOptions{
		Timeout: playwright.Float(10000),
	}); err != nil {
		return 0, fmt.Errorf("timeout waiting for Get Value Range button to enable: %w", err)
	}

	if err := getValueBtn.Click(playwright.LocatorClickOptions{Timeout: playwright.Float(10000)}); err != nil {
		return 0, fmt.Errorf("failed to click Get Value Range button: %w", err)
	}

	err = page.WaitForURL("**/car-value-results**", playwright.PageWaitForURLOptions{
		Timeout: playwright.Float(30000),
	})
	if err != nil {
		return 0, fmt.Errorf("timed out waiting for results page: %w", err)
	}

	valInterface, err := page.Evaluate(jsExtractValue)
	if err != nil {
		return 0, fmt.Errorf("failed to evaluate JS for value extraction: %w", err)
	}

	valStr, ok := valInterface.(string)
	if !ok || valStr == "" {
		slog.Warn("Carfax valuation failed: no value on results page",
			"processor", "facebook",
			"year", year, "make", make, "model", model,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return 0, fmt.Errorf("could not find value range on results page")
	}

	value, err := parseValueRange(valStr)
	if err != nil {
		slog.Warn("Carfax valuation failed: could not parse value",
			"processor", "facebook",
			"year", year, "make", make, "model", model,
			"raw_value", valStr,
			"error", err,
			"duration_ms", time.Since(start).Milliseconds(),
		)
		return 0, err
	}

	slog.Info("Carfax valuation succeeded",
		"processor", "facebook",
		"year", year, "make", make, "model", model, "trim", selectedTrim,
		"value", value,
		"raw_value", valStr,
		"duration_ms", time.Since(start).Milliseconds(),
	)
	return value, nil
}

// readOptions reads the available options from a Carfax dropdown by label.
func (c *CarfaxClient) readOptions(page playwright.Page, label string) ([]string, error) {
	result, err := page.Evaluate(jsReadOptions, label)
	if err != nil {
		return nil, fmt.Errorf("failed to read options for %s: %w", label, err)
	}
	items, ok := result.([]interface{})
	if !ok {
		return nil, nil
	}
	var opts []string
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			opts = append(opts, s)
		}
	}
	return opts, nil
}

func (c *CarfaxClient) selectFuzzy(page playwright.Page, label, targetText string) error {
	result, err := page.Evaluate(jsSelectFuzzy, []interface{}{label, targetText})
	if err != nil {
		slog.Warn("Carfax selectFuzzy JS error", "processor", "facebook", "label", label, "target", targetText, "error", err)
		return fmt.Errorf("failed to evaluate JS for %s: %w", label, err)
	}
	if result != nil {
		slog.Warn("Carfax selectFuzzy failed", "processor", "facebook", "label", label, "target", targetText, "result", result)
		return fmt.Errorf("%v", result)
	}
	return nil
}

// valueRangeRe matches patterns like "$12,345 - $67,890" or "12345-67890"
var valueRangeRe = regexp.MustCompile(`\$?\s*([\d,]+)\s*[-–—]\s*\$?\s*([\d,]+)`)

// singleValueRe matches a standalone dollar amount like "$12,345"
var singleValueRe = regexp.MustCompile(`\$?\s*([\d,]+)`)

func parseValueRange(valStr string) (float64, error) {
	valStr = strings.TrimSpace(valStr)
	if valStr == "" {
		return 0, fmt.Errorf("empty value string")
	}

	// Try to match a range like "$12,345 - $67,890"
	if m := valueRangeRe.FindStringSubmatch(valStr); len(m) >= 3 {
		minStr := strings.ReplaceAll(m[1], ",", "")
		maxStr := strings.ReplaceAll(m[2], ",", "")
		minVal, minErr := strconv.ParseFloat(minStr, 64)
		maxVal, maxErr := strconv.ParseFloat(maxStr, 64)
		if minErr != nil {
			return 0, fmt.Errorf("failed to parse range minimum %q: %w", m[1], minErr)
		}
		if maxErr != nil {
			return 0, fmt.Errorf("failed to parse range maximum %q: %w", m[2], maxErr)
		}
		if minVal <= 0 || maxVal <= 0 {
			return 0, fmt.Errorf("parsed non-positive values from range: min=%.0f max=%.0f", minVal, maxVal)
		}
		return (minVal + maxVal) / 2, nil
	}

	// Fallback: find any single dollar amount
	if m := singleValueRe.FindStringSubmatch(valStr); len(m) >= 2 {
		numStr := strings.ReplaceAll(m[1], ",", "")
		val, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, fmt.Errorf("failed to parse single value %q: %w", m[1], err)
		}
		if val <= 0 {
			return 0, fmt.Errorf("parsed non-positive value: %.0f", val)
		}
		return val, nil
	}

	return 0, fmt.Errorf("no numeric value found in: %s", valStr)
}
