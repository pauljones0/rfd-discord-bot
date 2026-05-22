# Scrape Lab Evidence

Last updated: 2026-05-22 config update, latest scrape evidence remains 2026-05-03 UTC.

This is the living ROI table for the low-rate scraper experiments. A backend is not considered production-proven until it gets three consecutive low-rate successful runs for the same target class, but these runs are enough to pick the next cheapest candidate for each site.

| Site | Backend / Setup | Result | Evidence | Current Read |
| --- | --- | --- | --- | --- |
| eBay | `http` | Blocked by Akamai on tracked item pages | `.codex-remote/scrape-lab-store-browser-smoke` | Not enough for coupon discovery pages from this environment. |
| eBay | `chromedp-cloudrun` | Blocked by Akamai on tracked item pages | `.codex-remote/scrape-lab-store-browser-smoke` | Does not improve on plain HTTP here. |
| eBay | `chromedp-persistent` | Blocked by Akamai on tracked item pages | `.codex-remote/scrape-lab-persistent-abs-smoke` | Persistent Chrome alone was not enough locally. |
| eBay | `playwright` | Blocked by Akamai on tracked item pages | `.codex-remote/scrape-lab-playwright-smoke` | Not a good eBay coupon-page fallback by itself. |
| eBay | `external-stealth` with nodriver | Loaded a real item page; no coupon found on that sample | `.codex-remote/scrape-lab-nodriver-validated` | Viable free coupon-page fallback. Needs repeated runs. |
| eBay | `external-stealth` with Camoufox | Loaded a real item page and detected `5% off` coupon text | `.codex-remote/scrape-lab-camoufox-validated` | Strong free candidate when coupon text is missing from cheaper paths. |
| eBay | `http` on Stormtrooper | HTTP 403 Akamai access denied on sampled tracked item pages | `/data/scrape-lab-stormtrooper-ebay-http` | Not usable for page coupon discovery from Stormtrooper. |
| eBay | `chromedp-persistent` on Stormtrooper | Akamai access denied on sampled tracked item pages | `/data/scrape-lab-stormtrooper-ebay-chromedp` | Not enough for page coupon discovery from Stormtrooper. |
| eBay | `external-stealth` with nodriver on Stormtrooper | Akamai access denied on sampled tracked item pages | `/data/scrape-lab-stormtrooper-ebay-2` | Browser launches after container fixes, but eBay still blocks; needs Camoufox, a different host/IP, or paid browser trial. |
| eBay | `external-stealth` with Camoufox on Stormtrooper | Akamai access denied on 3 sampled tracked item pages | `/data/scrape-lab-stormtrooper-ebay-camoufox-20260503-023152` | Camoufox installs and runs, but Stormtrooper/IP is still blocked for eBay pages. |
| eBay | `paid-trial` with Browserless on Stormtrooper | Passed on 3 sampled item pages; parsed coupons on 2 samples (`$10`, `$30` with `SCANNER`) | `/data/scrape-lab-stormtrooper-ebay-browserless-20260503-023754` | Proven final eBay-only fallback for post-drop coupon math. |
| Memory Express | `http` | Cloudflare managed challenge | `.codex-remote/scrape-lab-memoryexpress-baseline` | Not reliable from this environment. |
| Memory Express | `chromedp-cloudrun` | Cloudflare managed challenge | `.codex-remote/scrape-lab-memoryexpress-baseline` | Legacy container browser path does not solve the current challenge. |
| Memory Express | `chromedp-persistent` | Cloudflare managed challenge in the local trial | `.codex-remote/scrape-lab-persistent-abs-smoke` | Persistent Chrome may still be worth retesting on GCE, but local run did not pass. |
| Memory Express | `external-stealth` with nodriver | Passed and parsed 24 SKST clearance items | `.codex-remote/scrape-lab-nodriver-validated` | Best free candidate so far. Needs repeated low-rate confirmation. |
| Memory Express | `external-stealth` with Camoufox | Passed and parsed 24 SKST clearance items | `.codex-remote/scrape-lab-camoufox-validated` | Also viable; slower than nodriver in this run. |
| Memory Express | `external-stealth` with nodriver on Stormtrooper | Blocked by Cloudflare Turnstile | `/data/scrape-lab-stormtrooper-memoryexpress-2` | Not first choice on Stormtrooper. Keep as fallback evidence only. |
| Memory Express | `chromedp-persistent` on Stormtrooper | Passed and parsed 24 SKST clearance items | `/data/scrape-lab-stormtrooper-memoryexpress-fallbacks-2` | Best free Stormtrooper candidate so far; now first in default `MEMEXPRESS_BACKENDS`. |
| Memory Express | `http` on Stormtrooper | HTTP 403 Cloudflare managed challenge | `/data/scrape-lab-stormtrooper-memoryexpress-fallbacks-2` | Keep only as final cheap probe/fallback evidence. |
| Best Buy | `http` | Akamai access denied on seller pages/API | `.codex-remote/scrape-lab-store-browser-smoke` | Avoid as the primary production path. |
| Best Buy | `chromedp-cloudrun` | Akamai access denied on seller pages | `.codex-remote/scrape-lab-store-browser-smoke` | Does not improve on HTTP here. |
| Best Buy | `chromedp-persistent` | Akamai access denied on seller pages | `.codex-remote/scrape-lab-persistent-abs-smoke` | Persistent Chrome alone did not pass. |
| Best Buy | `playwright` | Akamai access denied on seller pages | `.codex-remote/scrape-lab-playwright-smoke` | Not useful by itself for these seller pages. |
| Best Buy | `external-stealth` with nodriver | Loaded the app shell but extracted 0 seller products | `.codex-remote/scrape-lab-nodriver-bestbuy-validated` | Not enough for production listing notifications. |
| Best Buy | `external-stealth` with Camoufox | Akamai access denied on seller pages | `.codex-remote/scrape-lab-camoufox-validated` | Did not beat nodriver or Algolia here. |
| Best Buy | `bestbuy-algolia` | Passed; parsed 1,209 Tech Outlet Center items and 2,000 Parts Search items | `.codex-remote/scrape-lab-bestbuy-algolia` | Best ROI candidate so far; now first in default `BESTBUY_BACKENDS`. |
| Best Buy | `bestbuy-algolia` on Stormtrooper | Passed; parsed 1,207 Tech Outlet Center items and 2,000 Parts Search items | `/data/scrape-lab-stormtrooper-bestbuy` | Confirmed as the Stormtrooper production candidate. |

Current candidates:

- eBay coupon pages: keep Browse API as source of truth. Page coupon discovery remains post-drop-only. Stormtrooper failed HTTP, chromedp, nodriver, and Camoufox against sampled item pages; Browserless passed a capped three-listing paid sample and is the final eBay-only fallback when enabled.
- Memory Express clearance: prefer `chromedp-persistent` on Stormtrooper for SKST, keep `external-stealth` as a fallback/prototype, and evaluate paid services only if persistent Chrome starts seeing repeated Cloudflare challenges.
- Best Buy seller listings: use `bestbuy-algolia` first. It is free, avoids the Akamai-denied public page/API paths, and already maps into the existing new-listing notification processor.


2026-05-22 command config note: scraper command adapters now prefer JSON argv `*_COMMAND_ARGS` env vars and keep Browserless documented as the final paid fallback only. No new scrape result was added by this config-only update.
