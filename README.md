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
scheduler:

```text
GET /process
GET /process-ebay
GET /process-memoryexpress
GET /process-bestbuy
POST /prime-bestbuy-baseline
```

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
EBAY_PAID_BROWSER_ENABLED=true
```

`paid-trial` is the Browserless adapter and only runs when explicitly enabled.
It stays last in the ladder behind HTTP, nodriver/external stealth, Camoufox,
and the local AI crawler adapter.

### Memory Express

Memory Express scrapes subscribed store clearance pages, saves every newly seen
product as baseline state, refreshes `lastSeen` for existing products, and uses
Gemini only to decide whether new items are warm/hot enough for subscribed
Discord channels.

### Best Buy

Best Buy polls configured seller pages, currently seeded for:

- Tech Outlet Center
- Parts Search

Every new product is saved and deduped. Discord subscription filters are now
AI-labeled:

- `bb_new`: post every new listing with AI label fields when available
- `bb_warm_hot`: post only AI warm or lava-hot listings
- `bb_hot`: post only AI lava-hot listings

With no Best Buy subscription, the processor still refreshes baseline inventory
quietly.

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

Postgres is the only runtime store. Manual JSON/env scrape-lab targets are still
available for narrow repros and paid-browser trials.

## Stormtrooper Deploy

From Stormtrooper:

```bash
cd ~/rfd-discord-bot
git pull --ff-only origin main
RFD_BOT_ENV_FILE=$HOME/.config/rfd-discord-bot/.env \
RFD_BOT_DATA_DIR=$HOME/appdata/rfd-discord-bot \
RFD_BOT_POSTGRES_DIR=$HOME/appdata/rfd-discord-bot/postgres \
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build
```

For branch testing, pull the branch instead of `main` and use the same Compose
command.

Useful checks:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml ps
docker compose -f deploy/stormtrooper/docker-compose.yml logs --tail=200 bot
curl -s http://127.0.0.1:18080/health
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
