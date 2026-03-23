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
		let retries = 20;
		while ((selectEl.disabled || selectEl.options.length <= 1) && retries > 0) {
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

	// jsPopulateDropdown fetches options from the Carfax API using reCAPTCHA and
	// populates a dropdown that failed to cascade. This bypasses the page's own
	// cascade handler which may fail in headless browsers due to reCAPTCHA scoring.
	jsPopulateDropdown = `async ([property, params]) => {
		const form = document.getElementById('carfax-vin-decode-form');
		if (!form) return {error: 'form not found'};
		const baseURL = form.getAttribute('data-resource') + '.year-make-model.json';

		// Build query string from params
		const qs = Object.entries(params).map(([k,v]) => k + '=' + encodeURIComponent(v)).join('&');
		const url = baseURL + '?property=' + encodeURIComponent(property) + '&' + qs;

		// Get reCAPTCHA token
		let token = '';
		let recaptchaError = '';
		const siteKey = document.querySelector('script[src*="recaptcha/api.js"]')?.src?.match(/render=([^&]+)/)?.[1];
		if (!siteKey) {
			recaptchaError = 'site key not found in script tag';
		} else if (!window.grecaptcha) {
			recaptchaError = 'grecaptcha not loaded';
		} else {
			try {
				token = await grecaptcha.execute(siteKey, {action: 'submit'});
			} catch(e) {
				recaptchaError = 'grecaptcha.execute failed: ' + e.message;
			}
		}

		const headers = {};
		if (token) headers['x-recaptcha-token'] = token;

		try {
			const resp = await fetch(url, {method: 'GET', headers});
			if (!resp.ok) {
				const body = await resp.text().catch(() => '');
				return {
					error: 'API returned ' + resp.status,
					apiStatus: resp.status,
					apiBody: body.substring(0, 200),
					hasToken: !!token,
					tokenLength: token.length,
					recaptchaError: recaptchaError || null,
				};
			}
			const json = await resp.json();
			if (!json.data || !json.data.length) return {error: 'no data returned', hasToken: !!token};

			// Find the target select element
			const selectors = ['select[aria-label="' + property + '"]', 'select#' + property + 's', 'select#' + property];
			let sel = null;
			for (const s of selectors) { sel = document.querySelector(s); if (sel) break; }
			if (!sel) return {error: 'select element not found for ' + property};

			// Clear existing options (keep placeholder)
			while (sel.options.length > 1) sel.remove(1);

			// Add fetched options
			for (const item of json.data) {
				const name = typeof item === 'string' ? item : (item.name || item.value || String(item));
				const opt = new Option(name, name);
				sel.add(opt);
			}

			sel.disabled = false;
			return {populated: sel.options.length - 1, hasToken: !!token, tokenLength: token.length};
		} catch(e) {
			return {error: e.message, hasToken: !!token, recaptchaError: recaptchaError || null};
		}
	}`

	// jsFindBestOption locates the best matching option in a dropdown by label
	// using fuzzy matching, marks the element for Playwright selection, and returns
	// the option value. Unlike jsSelectFuzzy, it does NOT select the option itself —
	// the caller uses Playwright's native SelectOption for proper event dispatching.
	jsFindBestOption = `async ([label, targetText]) => {
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

		if (!selectEl) return {error: "Select element not found for " + label};

		let retries = 20;
		while ((selectEl.disabled || selectEl.options.length <= 1) && retries > 0) {
			await new Promise(r => setTimeout(r, 500));
			retries--;
		}

		if (selectEl.disabled) return {error: "Select element disabled for " + label};

		const opts = Array.from(selectEl.options);
		if (selectEl.selectedIndex > 0 && !targetText) return {alreadySelected: true};

		let bestIdx = -1;
		if (targetText) {
			const cleanTarget = targetText.toLowerCase().replace(/[^a-z0-9]/g, '');
			for(let i=1; i<opts.length; i++) {
				if (opts[i].text.toLowerCase().replace(/[^a-z0-9]/g, '') === cleanTarget) {
					bestIdx = i; break;
				}
			}
			if (bestIdx === -1) {
				const lowerTarget = targetText.toLowerCase();
				for(let i=1; i<opts.length; i++) {
					const optText = opts[i].text.toLowerCase();
					if (optText.includes(lowerTarget) || lowerTarget.includes(optText)) {
						bestIdx = i; break;
					}
				}
			}
			if (bestIdx === -1) {
				const cleanTarget2 = targetText.toLowerCase().replace(/[^a-z0-9]/g, '');
				for(let i=1; i<opts.length; i++) {
					const cleanOpt = opts[i].text.toLowerCase().replace(/[^a-z0-9]/g, '');
					if (cleanOpt.includes(cleanTarget2) || cleanTarget2.includes(cleanOpt)) {
						bestIdx = i; break;
					}
				}
			}
		}

		if (bestIdx === -1 && opts.length > 1) {
			bestIdx = 1;
		}

		if (bestIdx !== -1) {
			// Tag the element so Playwright can locate it
			selectEl.setAttribute('data-carfax-select', label);
			return {value: opts[bestIdx].value};
		}

		return {error: "No matching option found for " + label};
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
		WaitUntil: playwright.WaitUntilStateLoad,
	})
	if err != nil {
		slog.Warn("Carfax navigation failed",
			"processor", "facebook", "error", err,
			"year", year, "make", make, "model", model)
		return 0, fmt.Errorf("failed to navigate to carfax value page: %w", err)
	}

	// Check for redirect (Cloudflare challenge, login wall, etc.)
	currentURL := page.URL()
	if !strings.Contains(currentURL, "carfax.ca") {
		slog.Warn("Carfax page redirected away",
			"processor", "facebook",
			"expected_host", "carfax.ca",
			"actual_url", currentURL,
			"year", year, "make", make, "model", model)
		return 0, fmt.Errorf("carfax page redirected to: %s", currentURL)
	}

	// Dismiss cookie consent / overlay banners that may block interaction
	c.dismissCarfaxOverlays(page)

	if err := c.selectFuzzy(page, "Year", fmt.Sprintf("%d", year)); err != nil {
		slog.Warn("Carfax Year dropdown failed",
			"processor", "facebook", "error", err,
			"year", year, "make", make, "model", model,
			"page_url", page.URL())
		return 0, fmt.Errorf("failed to select year: %w", err)
	}
	if err := c.selectFuzzy(page, "Make", make); err != nil {
		// Cascade likely failed because reCAPTCHA blocked the API call in headless.
		// Fallback: populate the Make dropdown via direct API call with reCAPTCHA token.
		slog.Info("Carfax Make cascade failed, trying direct API fallback",
			"processor", "facebook", "error", err, "year", year, "make", make)
		if populateErr := c.populateDropdownViaAPI(page, "Make", map[string]string{"year": fmt.Sprintf("%d", year)}); populateErr != nil {
			c.logDropdownDiagnostics(page, year, make)
			return 0, fmt.Errorf("failed to select make (cascade and API fallback both failed): %w", err)
		}
		if err := c.selectFuzzy(page, "Make", make); err != nil {
			c.logDropdownDiagnostics(page, year, make)
			return 0, fmt.Errorf("failed to select make after API fallback: %w", err)
		}
	}
	if err := c.selectFuzzy(page, "Model", model); err != nil {
		// Same reCAPTCHA fallback for Model
		slog.Info("Carfax Model cascade failed, trying direct API fallback",
			"processor", "facebook", "error", err, "year", year, "make", make, "model", model)
		if populateErr := c.populateDropdownViaAPI(page, "Model", map[string]string{"year": fmt.Sprintf("%d", year), "make": make}); populateErr != nil {
			return 0, fmt.Errorf("failed to select model (cascade and API fallback both failed): %w", err)
		}
		if err := c.selectFuzzy(page, "Model", model); err != nil {
			slog.Warn("Carfax Model dropdown failed",
				"processor", "facebook", "error", err,
				"year", year, "make", make, "model", model)
			return 0, fmt.Errorf("failed to select model after API fallback: %w", err)
		}
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
		slog.Warn("Carfax step 2 (trim page) timeout",
			"processor", "facebook", "error", err,
			"year", year, "make", make, "model", model,
			"page_url", page.URL())
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
	result, err := page.Evaluate(jsFindBestOption, []interface{}{label, targetText})
	if err != nil {
		slog.Warn("Carfax selectFuzzy JS error",
			"processor", "facebook",
			"label", label, "target", targetText,
			"error", err, "page_url", page.URL())
		return fmt.Errorf("failed to evaluate JS for %s: %w", label, err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Errorf("unexpected result type for %s", label)
	}

	if errMsg, hasErr := resultMap["error"]; hasErr {
		errStr := fmt.Sprintf("%v", errMsg)
		slog.Warn("Carfax selectFuzzy failed",
			"processor", "facebook",
			"label", label, "target", targetText,
			"result", errStr)
		return fmt.Errorf("%s for %s", errStr, label)
	}

	// Already selected and no target — nothing to do
	if _, alreadySet := resultMap["alreadySelected"]; alreadySet {
		return nil
	}

	value, ok := resultMap["value"].(string)
	if !ok {
		return fmt.Errorf("no value returned for %s", label)
	}

	// Use Playwright's native SelectOption to fire proper browser events.
	loc := page.Locator("[data-carfax-select='" + label + "']")
	if _, err := loc.SelectOption(playwright.SelectOptionValues{
		Values: playwright.StringSlice(value),
	}); err != nil {
		slog.Warn("Carfax Playwright SelectOption failed",
			"processor", "facebook",
			"label", label, "target", targetText,
			"value", value, "error", err)
		return fmt.Errorf("failed to select option for %s: %w", label, err)
	}

	// Re-dispatch change event with native setter to ensure the page's event
	// handlers (which use event delegation on a parent container) see the change.
	// Playwright's SelectOption should fire events, but in headless Chromium the
	// cascade handler sometimes doesn't trigger without this extra dispatch.
	page.Evaluate(`([label, val]) => {
		const sel = document.querySelector('[data-carfax-select="' + label + '"]');
		if (!sel) return;
		const nativeSetter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set;
		if (nativeSetter) nativeSetter.call(sel, val);
		sel.dispatchEvent(new Event('input', { bubbles: true }));
		sel.dispatchEvent(new Event('change', { bubbles: true }));
	}`, []interface{}{label, value})

	// Clean up the temporary attribute
	page.Evaluate("(label) => document.querySelector('[data-carfax-select=\"' + label + '\"]')?.removeAttribute('data-carfax-select')", label)

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

// populateDropdownViaAPI fetches dropdown options directly from the Carfax API
// using reCAPTCHA and populates the select element. This is a fallback for when
// the page's built-in cascade handler fails (typically due to reCAPTCHA blocking
// the API call in headless browsers).
func (c *CarfaxClient) populateDropdownViaAPI(page playwright.Page, property string, params map[string]string) error {
	// Convert map[string]string to map[string]interface{} for Playwright serialization.
	// Playwright's serializeValue does a bare assertion to map[string]any which panics on map[string]string.
	paramsAny := make(map[string]interface{}, len(params))
	for k, v := range params {
		paramsAny[k] = v
	}
	result, err := page.Evaluate(jsPopulateDropdown, []interface{}{property, paramsAny})
	if err != nil {
		return fmt.Errorf("JS error populating %s: %w", property, err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return fmt.Errorf("unexpected result type populating %s", property)
	}

	if errMsg, hasErr := resultMap["error"]; hasErr {
		slog.Warn("Carfax API fallback failed",
			"processor", "facebook",
			"property", property,
			"error", errMsg,
			"api_status", resultMap["apiStatus"],
			"api_body", resultMap["apiBody"],
			"has_recaptcha_token", resultMap["hasToken"],
			"recaptcha_token_length", resultMap["tokenLength"],
			"recaptcha_error", resultMap["recaptchaError"],
		)
		return fmt.Errorf("API populate %s: %v", property, errMsg)
	}

	count, _ := resultMap["populated"].(float64)
	slog.Info("Carfax API fallback populated dropdown",
		"processor", "facebook",
		"property", property,
		"options_count", int(count),
		"has_recaptcha_token", resultMap["hasToken"],
		"recaptcha_token_length", resultMap["tokenLength"],
	)
	return nil
}

// dismissCarfaxOverlays attempts to close cookie consent banners and other
// overlays on the Carfax page that may prevent dropdown interaction.
func (c *CarfaxClient) dismissCarfaxOverlays(page playwright.Page) {
	// Common cookie consent selectors used by OneTrust, CookieBot, and generic banners
	selectors := []string{
		"#onetrust-accept-btn-handler",
		"button[id*='cookie-accept']",
		"button[class*='cookie-accept']",
		"button[data-testid='cookie-accept']",
		"[aria-label='Accept cookies']",
		"[aria-label='Accept all cookies']",
		".cookie-banner button:first-of-type",
	}
	for _, sel := range selectors {
		btn := page.Locator(sel)
		if count, _ := btn.Count(); count > 0 {
			if err := btn.First().Click(playwright.LocatorClickOptions{Timeout: playwright.Float(2000)}); err == nil {
				slog.Info("Dismissed Carfax overlay", "processor", "facebook", "selector", sel)
				time.Sleep(500 * time.Millisecond)
				return
			}
		}
	}
}

// logDropdownDiagnostics logs information about the page state when a dropdown
// cascade fails, helping identify whether the issue is blocking, page changes,
// or timing.
func (c *CarfaxClient) logDropdownDiagnostics(page playwright.Page, year int, targetMake string) {
	// Check what the Year dropdown currently shows
	yearInfo, _ := page.Evaluate(`() => {
		const sel = document.querySelector('select[aria-label="Year"]');
		if (!sel) return {found: false};
		return {
			found: true,
			disabled: sel.disabled,
			selectedIndex: sel.selectedIndex,
			selectedText: sel.selectedIndex >= 0 ? sel.options[sel.selectedIndex].text : "",
			optionCount: sel.options.length,
		};
	}`)

	// Check Make dropdown state — include actual option texts for debugging
	makeInfo, _ := page.Evaluate(`() => {
		const sel = document.querySelector('select[aria-label="Make"]');
		if (!sel) return {found: false};
		const optTexts = Array.from(sel.options).slice(0, 10).map(o => o.text);
		return {
			found: true,
			disabled: sel.disabled,
			optionCount: sel.options.length,
			options: optTexts,
		};
	}`)

	// Check page title for Cloudflare/CAPTCHA indicators
	title, _ := page.Title()

	slog.Warn("Carfax dropdown cascade diagnostic",
		"processor", "facebook",
		"page_url", page.URL(),
		"page_title", title,
		"target_year", year,
		"target_make", targetMake,
		"year_dropdown", yearInfo,
		"make_dropdown", makeInfo,
	)
}
