# Scrape Lab

The scrape lab is a low-rate evidence harness for eBay, Memory Express, and Best Buy bot-detection experiments. It tests the same real target pages through ordered backends and writes a Markdown/JSON table showing what worked, what was blocked, and what data was parsed.

The current cross-run evidence table lives in `docs/scrape-lab/evidence.md`.

## Backends

- `http`: plain Go HTTP with realistic browser headers.
- `chromedp-cloudrun`: Chromium via chromedp with a temporary profile.
- `chromedp-persistent`: Chromium via chromedp with a persistent profile, useful locally or on GCE with Xvfb.
- `playwright`: Playwright Chromium trial.
- `bestbuy-algolia`: Best Buy only; queries the public Algolia search index exposed by the Best Buy app shell and filters by seller.
- `external-stealth`: command adapter for a Camoufox/nodriver prototype.
- `paid-trial`: command adapter for a managed browser/unlocker proof of concept.

The command adapters read the target URL from `SCRAPELAB_TARGET_URL` and should print HTML/JSON to stdout. You can also include `{url}` in the command string.

```powershell
$env:SCRAPELAB_EXTERNAL_STEALTH_COMMAND = "python .\scripts\camoufox_fetch.py '{url}'"
$env:SCRAPELAB_PAID_TRIAL_COMMAND = "python .\scripts\browserless_bql_fetch.py '{url}'"
```

## Firestore Targets

Scrape lab can pull the real targets already tracked by the bot:

- eBay: recent `ebay_items` records with a non-empty `itemURL`, sorted by `lastSeenAt` descending.
- Memory Express: subscribed store codes, deduped into clearance page URLs.
- Best Buy: active `bestbuy_sellers`, using `searchURL` when present and otherwise building the public seller search page.

```powershell
go run ./cmd/scrape-lab `
  -from-firestore `
  -sites ebay,bestbuy `
  -ebay-limit 25 `
  -backends http,chromedp-cloudrun,chromedp-persistent `
  -env local `
  -out docs\scrape-lab\local-$(Get-Date -Format yyyyMMdd-HHmmss)
```

Manual JSON and env targets remain the override path for narrow repros. If `-targets` is supplied, scrape-lab uses only that file. If `SCRAPELAB_EBAY_URLS`, `SCRAPELAB_MEMEXPRESS_STORES`, `SCRAPELAB_BESTBUY_URLS`, or `SCRAPELAB_INCLUDE_DEFAULT_BESTBUY=1` are set, scrape-lab uses those env targets instead of Firestore.

If Firestore has no active Best Buy seller records yet, scrape-lab falls back to:

- `https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center`
- `https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search`

## Target File

Create a JSON file with only the real targets you care about:

```json
[
  {
    "site": "memoryexpress",
    "name": "Saskatoon North clearance",
    "url": "https://www.memoryexpress.com/Clearance/Store/SKST"
  },
  {
    "site": "bestbuy",
    "name": "Tech Outlet Center",
    "url": "https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center"
  },
  {
    "site": "bestbuy",
    "name": "Parts Search",
    "url": "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search"
  },
  {
    "site": "ebay",
    "name": "Tracked listing coupon check",
    "url": "https://www.ebay.ca/itm/YOUR_ITEM_ID"
  }
]
```

## Run

```powershell
go run ./cmd/scrape-lab `
  -targets .\scratch\scrape-targets.json `
  -backends http,chromedp-cloudrun,chromedp-persistent,playwright `
  -env local `
  -out docs\scrape-lab\local-$(Get-Date -Format yyyyMMdd-HHmmss)
```

For quick Best Buy smoke testing without a target file:

```powershell
$env:SCRAPELAB_INCLUDE_DEFAULT_BESTBUY = "1"
go run ./cmd/scrape-lab -backends http,bestbuy-algolia,chromedp-cloudrun -env local
```

## Production Fallback Config

Production processors use ordered backend lists:

```text
EBAY_COUPON_BACKENDS=http,external-stealth
EBAY_COUPON_EXTERNAL_STEALTH_COMMAND=xvfb-run -a /opt/scrape-venv/bin/python scripts/camoufox_fetch.py "{url}" --wait-ms 7000
MEMEXPRESS_BACKENDS=chromedp-persistent,external-stealth,http
BESTBUY_BACKENDS=bestbuy-algolia,http,chromedp-cloudrun,playwright
```

For eBay, the site-specific command envs are used before scrape-lab globals:

```text
EBAY_COUPON_EXTERNAL_STEALTH_COMMAND
EBAY_COUPON_PAID_TRIAL_COMMAND
```

Keep `paid-trial` out of production lists until the scrape lab shows repeatable failure on free/local options and a tiny paid sample succeeds. The Browserless proof of concept uses the BrowserQL stealth route and requires `BROWSERLESS_TOKEN`:

```bash
export BROWSERLESS_TOKEN=...
export SCRAPELAB_PAID_TRIAL_COMMAND='/opt/scrape-venv/bin/python scripts/browserless_bql_fetch.py "{url}" --wait-ms 5000'
./scrape-lab -from-firestore -sites ebay -ebay-limit 3 -backends paid-trial -env stormtrooper -out /data/scrape-lab-browserless-ebay-$(date +%Y%m%d-%H%M%S)
```

Stop the paid trial after three listing attempts or before the configured spend cap, whichever comes first.

## GCE Persistent Browser Runner

For Memory Express, the cheapest remote browser option is a small GCE VM with Chrome, Xvfb, Firestore credentials, and the existing runner:

```bash
sudo apt update
sudo apt install -y google-chrome-stable xvfb
Xvfb :99 -screen 0 1920x1080x24 &
export DISPLAY=:99
export GOOGLE_CLOUD_PROJECT=your-project
export DISCORD_BOT_TOKEN=...
export GEMINI_API_KEY=...
export MEMEXPRESS_ALERT_MODE=log
export MEMEXPRESS_CHROME_PROFILE_DIR=/opt/rfd-discord-bot/memoryexpress-chrome
./memoryexpress-local
```

When this runner is active, disable the Cloud Scheduler job that calls `/process-memoryexpress` so Cloud Run does not keep producing expected Cloudflare failures.

## Decision Rule

Use the cheapest backend that gets three consecutive successful low-rate runs for each target. If a backend produces two consecutive block/challenge results for a site, test the next backend in that site’s list. Only run `paid-trial` after the free/local paths have a clear failure record in `docs/scrape-lab/*/results.md`.
