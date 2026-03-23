# Carfax Canada API Knowledge

## Status
**Not yet implemented as direct API integration** — the current approach automates the Carfax UI via Playwright. reCAPTCHA v3 scoring blocks direct API calls from headless browsers. This document captures API knowledge for future implementation when a viable auth strategy is found.

## API Endpoint

The Carfax valuation page uses a cascading dropdown API:

```
{base_url}.year-make-model.json?property={property}&{params}
```

- `base_url` is read from the form: `document.getElementById('carfax-vin-decode-form').getAttribute('data-resource')`
- Typically resolves to something like: `https://www.carfax.ca/api/services/vehicles`

### Example Calls

**Get Makes for a Year:**
```
GET {base_url}.year-make-model.json?property=Make&year=2018
```

**Get Models for Year+Make:**
```
GET {base_url}.year-make-model.json?property=Model&year=2018&make=Honda
```

### Required Headers

| Header | Description |
|--------|-------------|
| `x-recaptcha-token` | reCAPTCHA v3 token (required for successful response) |

### Response Format

**Success (200):**
```json
{
  "data": ["Accord", "Civic", "CR-V", "Fit", "HR-V", "Odyssey", "Pilot", "Ridgeline"]
}
```

**Failure — reCAPTCHA rejected (400):**
```json
{"success": false}
```

**Failure — low reCAPTCHA score (200 with empty data):**
```json
{"data": []}
```

## reCAPTCHA Details

- **Type:** reCAPTCHA v3 (invisible, score-based)
- **Site key:** Extracted from `<script src="...recaptcha/api.js?render={SITE_KEY}">` on the page
- **Action:** `submit`
- **Token acquisition:** `await grecaptcha.execute(siteKey, {action: 'submit'})`
- **Token length:** ~2300-2500 characters when valid

### Why It Fails in Headless

reCAPTCHA v3 assigns a trust score (0.0-1.0) based on:
1. Browser fingerprint (WebGL, canvas, plugins) — we spoof these
2. Mouse/keyboard interaction patterns — we now simulate these
3. Session history and behavior consistency
4. IP reputation (residential proxy helps)
5. **Headless detection heuristics** — the main issue; even with stealth patches, reCAPTCHA's server-side model detects automation patterns

The token is obtained successfully, but the server-side score is too low, causing either a 400 response or an empty data array.

## Current Implementation (UI Automation)

See `internal/facebook/carfax.go`:
- `GetValue()` — main flow, navigates the multi-page form
- `selectFuzzy()` — fuzzy-matches dropdown options
- `populateDropdownViaAPI()` — fallback that calls the API with a reCAPTCHA token from the page
- `jsPopulateDropdown` — JavaScript constant containing the API call logic

## Future Path

The ideal future architecture:
1. **Primary:** Direct API calls (this doc) — fastest, no browser overhead
2. **Fallback:** UI automation via Playwright (current approach)

To make direct API work, we'd need one of:
- A way to get high-scoring reCAPTCHA tokens (e.g., from a headed browser session)
- Session cookie reuse from a successfully scored session
- A server-side token proxy that generates valid tokens
- Carfax offering an official API (they have dealer APIs, but no public valuation API)
