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
- `camoufox`: command adapter for the Camoufox script.
- `ai-crawler`: command adapter for the Crawl4AI Magic Mode script.
- `paid-trial`: command adapter for a managed browser/unlocker proof of concept.

The command adapters read the target URL from `SCRAPELAB_TARGET_URL` and should print HTML/JSON to stdout. Prefer the `*_COMMAND_ARGS` JSON argv variables; `{url}` is replaced only when it is a full argv value.

```powershell
$env:SCRAPELAB_EXTERNAL_STEALTH_COMMAND_ARGS = '["python","./scripts/nodriver_fetch.py","{url}","--headless"]'
$env:SCRAPELAB_CAMOUFOX_COMMAND_ARGS = '["python","./scripts/camoufox_fetch.py","{url}","--headless"]'
$env:SCRAPELAB_AI_CRAWLER_COMMAND_ARGS = '["python","./scripts/crawl4ai_fetch.py","{url}","--headless"]'
$env:SCRAPELAB_PAID_TRIAL_COMMAND_ARGS = '["python","./scripts/browserless_bql_fetch.py","{url}"]'
```

Legacy `*_COMMAND` shell strings still work for one deploy window with a warning, then should be removed after validation.

In production, browser binaries are not baked on every image build. Set
`XDG_CACHE_HOME=/data/browser-cache` and
`PLAYWRIGHT_BROWSERS_PATH=/data/browser-cache/playwright` with a persistent
`RFD_BOT_BROWSER_CACHE_DIR` volume. Camoufox uses its cache path automatically,
and the nodriver/Crawl4AI wrappers run `python -m playwright install <browser>`
only when the matching cached browser is missing or stale.

## Store Targets

Scrape lab can pull real targets from the local Postgres document store:

- eBay: recent `ebay_items` records with a non-empty `itemURL`, sorted by `lastSeenAt` descending.
- Memory Express: subscribed store codes, deduped into clearance page URLs.
- Best Buy: active `bestbuy_sellers`, using `searchURL` when present and otherwise building the public seller search page.

```powershell
go run ./cmd/scrape-lab `
  -from-store `
  -sites ebay,bestbuy `
  -ebay-limit 25 `
  -backends http,external-stealth,camoufox,ai-crawler,paid-trial `
  -env local `
  -out docs\scrape-lab\local-$(Get-Date -Format yyyyMMdd-HHmmss)
```

Manual JSON and env targets remain the override path for narrow repros. If `-targets` is supplied, scrape-lab uses only that file. If `SCRAPELAB_EBAY_URLS`, `SCRAPELAB_MEMEXPRESS_STORES`, `SCRAPELAB_BESTBUY_URLS`, or `SCRAPELAB_INCLUDE_DEFAULT_BESTBUY=1` are set, scrape-lab uses those env targets instead of store discovery.

If the store has no active Best Buy seller records yet, scrape-lab falls back to:

- `https://www.bestbuy.ca/en-ca/search?path=sellerName%3ATech+Outlet+Center`
- `https://www.bestbuy.ca/en-ca/search?path=sellerName%3AParts+Search`
- `https://www.bestbuy.ca/en-ca/search?path=sellerName%3AOpenBox`

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
    "site": "bestbuy",
    "name": "OpenBox",
    "url": "https://www.bestbuy.ca/en-ca/search?path=sellerName%3AOpenBox"
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

Production processors use ordered backend lists on Stormtrooper:

```text
EBAY_COUPON_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
EBAY_COUPON_EXTERNAL_STEALTH_COMMAND_ARGS=["xvfb-run","-a","/opt/scrape-venv/bin/python","scripts/camoufox_fetch.py","{url}","--wait-ms","7000"]
EBAY_COUPON_CAMOUFOX_COMMAND_ARGS=["xvfb-run","-a","/opt/scrape-venv/bin/python","scripts/camoufox_fetch.py","{url}","--wait-ms","7000"]
EBAY_COUPON_AI_CRAWLER_COMMAND_ARGS=["/opt/scrape-venv/bin/python","scripts/crawl4ai_fetch.py","{url}","--wait-ms","7000"]
EBAY_PAID_BROWSER_ENABLED=false
MEMEXPRESS_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
BESTBUY_BACKENDS=bestbuy-algolia,http
```

For eBay, the site-specific command envs are used before scrape-lab globals:

```text
EBAY_COUPON_EXTERNAL_STEALTH_COMMAND_ARGS
EBAY_COUPON_CAMOUFOX_COMMAND_ARGS
EBAY_COUPON_AI_CRAWLER_COMMAND_ARGS
EBAY_COUPON_PAID_TRIAL_COMMAND_ARGS
```

Keep `paid-trial` as the final backend when testing Browserless, but leave the
site-specific paid enable flags disabled in normal production runs. The
Browserless proof of concept uses the BrowserQL stealth route and requires
`BROWSERLESS_TOKEN`:

```bash
export BROWSERLESS_TOKEN=...
export SCRAPELAB_PAID_TRIAL_COMMAND_ARGS='["/opt/scrape-venv/bin/python","scripts/browserless_bql_fetch.py","{url}","--wait-ms","5000"]'
SCRAPELAB_PAID_BROWSER_ENABLED=true ./scrape-lab -from-store -sites ebay -ebay-limit 3 -backends paid-trial -env stormtrooper -out /data/scrape-lab-browserless-ebay-$(date +%Y%m%d-%H%M%S)
```

Stop the paid trial after three listing attempts or before the configured spend cap, whichever comes first.

## Legacy Persistent Browser Runner

For Memory Express experiments, a persistent browser runner can still be used on
a small remote VM or local desktop. The active production path is the
Stormtrooper bot container, so treat this as manual evidence gathering rather
than normal operations:

```bash
sudo apt update
sudo apt install -y google-chrome-stable xvfb
Xvfb :99 -screen 0 1920x1080x24 &
export DISPLAY=:99
export DISCORD_BOT_TOKEN=...
export GEMINI_API_KEY=...
export MEMEXPRESS_ALERT_MODE=log
export MEMEXPRESS_CHROME_PROFILE_DIR=/opt/rfd-discord-bot/memoryexpress-chrome
./memoryexpress-local
```

## Decision Rule

Use the cheapest backend that gets three consecutive successful low-rate runs for each target. If a backend produces two consecutive block/challenge results for a site, test the next backend in that site’s list. Only run `paid-trial` after the free/local paths have a clear failure record in `docs/scrape-lab/*/results.md`.
