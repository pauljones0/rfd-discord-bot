# RFD Discord Bot

A local-first deal monitor for Discord. The production runtime now lives on
Stormtrooper with Docker Compose, Postgres, and the built-in Go scheduler.

The bot watches:

- RedFlagDeals forum threads
- eBay tracked sellers and tracked item price drops
- Memory Express clearance by subscribed store
- Best Buy Marketplace seller pages

Facebook Marketplace, Carfax token service, Reddit relay, and HardwareSwap code
remain in the repo as optional features. Facebook and HardwareSwap are hidden
unless `FACEBOOK_ENABLED=true` or `HARDWARESWAP_ENABLED=true`.

## Production Runtime

Production runs from `deploy/stormtrooper/docker-compose.yml`:

- `bot`: Go HTTP server and scheduler
- `postgres`: local JSONB document store
- `cloudflared`: optional ingress profile for `bot.pauljones0.uk`

The public Discord interaction endpoint is:

```text
https://bot.pauljones0.uk/discord/interactions
```

Health check:

```powershell
curl.exe http://stormtrooper:18080/health
```

Public routing should expose only `/discord/interactions` and `/health`. Manual
processor endpoints stay on localhost/SSH and also require `RFD_ADMIN_TOKEN`.
Expected production health reports `storage=postgres`.

## Active Scheduler

The scheduler is in-process. Stormtrooper should set:

```env
LOCAL_SCHEDULER_ENABLED=true
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=30m
```

Manual HTTP triggers still exist and use the same concurrency guards as the
scheduler, but they require `Authorization: Bearer $RFD_ADMIN_TOKEN` and should
only be reached over localhost/SSH:

```text
GET /process-deals
GET /process-ebay
GET /process-memoryexpress
GET /process-bestbuy
GET /process-bestbuy-compute
POST /prime-bestbuy-baseline
```

Production should keep `ALLOW_UNSIGNED_DISCORD_INTERACTIONS=false`; unsigned
Discord interactions are only for explicit local development or tests.

## Current Flows

### RFD

RFD scrapes configured forum sections, groups duplicate threads, labels deals
with Gemini, stores state in Postgres, and sends Discord embeds to subscribed
channels according to `/deals setup-rfd` filters.

### eBay

eBay Browse API is the source of truth for seller inventory and base prices.
Tracked items are persisted in Postgres and alerts only fire on real API/base
price-drop signals.

Page scraping is a secondary coupon path. It is used only after a real Browse
API/base price-drop signal, never to create coupon-only alerts. Seller-level
coupon evidence is stored in `ebay_coupon_observations`, inferred coupons are
cached in `ebay_store_coupons`, and a seller gets at most one browser coupon
attempt every six hours unless a known coupon has expired.

Typical Stormtrooper backend order:

```env
EBAY_COUPON_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
EBAY_COUPON_DISCOVERY_INTERVAL=6h
EBAY_PAID_BROWSER_ENABLED=false
EBAY_PAID_BROWSER_MAX_CALLS_PER_RUN=1
EBAY_PAID_BROWSER_MAX_CALLS_PER_DAY=6
```

`paid-trial` is the Browserless adapter and only runs when explicitly enabled.
It is disabled in normal production runs for now; set the site-specific boolean
back to `true` only after validating Browserless in scrape-lab. It stays last in
the ladder behind HTTP, nodriver/external stealth, Camoufox, and the local AI
crawler adapter.

### Memory Express

Memory Express scrapes subscribed store clearance pages, saves every newly seen
product as baseline state, refreshes `lastSeen` for existing products, and uses
Gemini only to decide whether new items are warm/hot enough for subscribed
Discord channels.

### Best Buy

Best Buy polls configured seller pages, currently seeded for:

- Tech Outlet Center
- Parts Search
- OpenBox

Every new product is saved and deduped. Discord subscription filters are now
AI-labeled:

- `bb_new`: post every new listing with AI label fields when available
- `bb_warm_hot`: post only AI warm or lava-hot listings
- `bb_hot`: post only AI lava-hot listings

With no Best Buy subscription, the processor still refreshes baseline inventory
quietly.

Optional eBay sold-comps enrichment for Best Buy seller alerts is enabled by
default with `BESTBUY_SOLD_COMPS_ENABLED=true`. It only runs for AI tier-1
candidates before tier-2 analysis, caches sold-search snapshots for
`BESTBUY_SOLD_COMP_CACHE_TTL`, waits `BESTBUY_SOLD_COMP_QUERY_DELAY` between
eBay fetch attempts, and caps uncached lookups with `BESTBUY_SOLD_COMP_MAX_PER_RUN`
(default `10`). Uncached candidates are ranked by Best Buy comp gap, dollar
margin, comp count, high-value compute signal, and model/brand confidence. eBay
sold comps verify warm/hot labels when enough matches exist; blocked, errored,
or thin eBay evidence fails open to the existing AI/Best Buy behavior.

### Core Discord Notifications

Core deal observations are ingested from the bundled Android notification
listener under `internal/core/swordswallower`. The listener posts directly to:

```text
POST /ingest/discord-notification
```

Authenticate it with `Authorization: Bearer $RFD_ADMIN_TOKEN`, or set
`SWORDSWALLOWER_SECRET` and use that listener-only token. The endpoint also
accepts the legacy `X-Swordswallower-Secret` header for the bundled receiver and
older listener configs.

The ingest path stores richer raw notification candidates (`bigText`, stacked
lines, message text, links, and images), processes them through the Core
pipeline, and sends operational failures to the configured Core subscription
channel. Listener test events, listener exceptions, raw-notification storage
failures, Core processing failures, and mark-as-read action failures are
reported there with throttling.

Build and configure the listener from this repo:

```powershell
cd internal/core/swordswallower
make build-listener
RFD_ADMIN_TOKEN=... make configure-listener
make enable-listener
```

## Discord Commands

Register slash commands after changing command definitions:

```powershell
go run ./cmd/register-commands
```

Primary command:

```text
/deals setup-rfd
/deals setup-ebay
/deals setup-memoryexpress
/deals setup-bestbuy
/deals remove
/deals list
```

HardwareSwap keeps its own optional commands when enabled:

```text
/hw-setup
/hw-help
/hw-alert
```

## Local Development

Copy `.env.example` to `.env` and fill the required secrets. Real `.env` files
are ignored by git.

Run tests:

```powershell
go test ./...
go vet ./...
```

Run the server locally:

```powershell
go run ./cmd/server
```

Run scrape-lab with Postgres-backed targets for experimentation:

```powershell
go run ./cmd/scrape-lab -from-store -sites ebay,memoryexpress,bestbuy -ebay-limit 3
```

Report any old subscription values before cleaning them from Postgres:

```powershell
go run ./cmd/cleanup-legacy-subscriptions
```

Postgres is the only runtime store. Manual JSON/env scrape-lab targets are still
available for narrow repros and paid-browser trials.

## Stormtrooper Deploy

From Stormtrooper:

```bash
cd ~/agent-work/repos/rfd-discord-bot
git pull --ff-only origin main
RFD_BOT_ENV_FILE=$HOME/.config/rfd-discord-bot/.env \
RFD_BOT_DATA_DIR=$HOME/appdata/rfd-discord-bot \
RFD_BOT_POSTGRES_DIR=$HOME/appdata/rfd-discord-bot/postgres \
RFD_BOT_BROWSER_CACHE_DIR=$HOME/appdata/rfd-discord-bot/browser-cache \
PY_BROWSER_PACKAGE_REFRESH=$(date -u +%Y%m%d) \
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build
```

For branch testing, pull the branch instead of `main` and use the same Compose
command.

Compose checks for newer base/runtime images on deploy while reusing local
layers when the digest is unchanged. Browser packages are intentionally
unpinned; bump `PY_BROWSER_PACKAGE_REFRESH` when you want Docker to refresh the
pip install layer. Camoufox, Playwright/Crawl4AI, and nodriver browser binaries
live under `RFD_BOT_BROWSER_CACHE_DIR` and are downloaded only when the matching
cache entry is missing or stale.

Useful checks:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml ps
docker compose -f deploy/stormtrooper/docker-compose.yml logs --tail=200 bot
curl -s http://127.0.0.1:18080/health
curl -fsS -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/process-deals
```

After deploy, watch at least one full scheduler cycle. Confirm health stays OK,
Compose shows the bot and Postgres healthy, and bot logs show RFD, eBay, Memory
Express, and Best Buy polling without repeated block or auth errors.

The Compose file pins the project name to `rfd-discord-bot` so other stacks in
directories named `stormtrooper` cannot replace its `postgres` service. Install
the watchdog timer after deploys so missing/stopped containers are reconciled
automatically:

```bash
chmod +x deploy/stormtrooper/rfd-bot-watchdog.sh
mkdir -p ~/.config/systemd/user
cp deploy/stormtrooper/systemd/rfd-discord-bot-watchdog.* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now rfd-discord-bot-watchdog.timer
```

## Repository Hygiene

Do not commit:

- `.env` or local secret files
- service-account JSON or ADC files
- Postgres data directories
- local binaries
- `.mcp.json`
- `.codex-remote/`

Legacy hosted deployment paths have been removed from normal operations.
