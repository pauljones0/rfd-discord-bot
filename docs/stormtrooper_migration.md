# Stormtrooper Operations Runbook

Stormtrooper is the active production host. The bot runs with Docker Compose,
local Postgres, and the built-in scheduler. Postgres is the only runtime store.

## Runtime Paths

Preferred user-owned paths:

```bash
$HOME/rfd-discord-bot
$HOME/.config/rfd-discord-bot/.env
$HOME/appdata/rfd-discord-bot
$HOME/appdata/rfd-discord-bot/postgres
$HOME/appdata/rfd-discord-bot/browser-cache
```

The `.env` file is outside git and should include:

```env
POSTGRES_PASSWORD=...
DATABASE_URL=postgres://rfd_bot:...@postgres:5432/rfd_discord_bot?sslmode=disable
RFD_ADMIN_TOKEN=...
ALLOW_UNSIGNED_DISCORD_INTERACTIONS=false

LOCAL_SCHEDULER_ENABLED=true
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=30m

EBAY_COUPON_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
EBAY_COUPON_DISCOVERY_INTERVAL=6h
EBAY_PAID_BROWSER_ENABLED=false
EBAY_PAID_BROWSER_MAX_CALLS_PER_RUN=1
EBAY_PAID_BROWSER_MAX_CALLS_PER_DAY=6
MEMEXPRESS_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
MEMEXPRESS_PAID_BROWSER_ENABLED=false
MEMEXPRESS_PAID_BROWSER_MAX_CALLS_PER_RUN=0
MEMEXPRESS_PAID_BROWSER_MAX_CALLS_PER_DAY=0
BESTBUY_BACKENDS=bestbuy-algolia,http
FACEBOOK_ENABLED=false
HARDWARESWAP_ENABLED=false
```

Keep Discord, Gemini, eBay, Cloudflare Tunnel, and optional Browserless secrets
in that same file. Do not commit it.

## Deploy Or Redeploy

```bash
cd ~/rfd-discord-bot
git pull --ff-only origin main
RFD_BOT_ENV_FILE=$HOME/.config/rfd-discord-bot/.env \
RFD_BOT_DATA_DIR=$HOME/appdata/rfd-discord-bot \
RFD_BOT_POSTGRES_DIR=$HOME/appdata/rfd-discord-bot/postgres \
RFD_BOT_BROWSER_CACHE_DIR=$HOME/appdata/rfd-discord-bot/browser-cache \
PY_BROWSER_PACKAGE_REFRESH=$(date -u +%Y%m%d) \
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build
```

For branch verification, check out the branch first and use the same Compose
command.

The Compose file asks Docker to check for newer images and build bases while
still reusing cached layers/blobs when they are current. Browser Python
packages are unpinned and refreshed by changing `PY_BROWSER_PACKAGE_REFRESH`.
Runtime browser binaries are cached in `RFD_BOT_BROWSER_CACHE_DIR`; Camoufox and
Playwright-backed adapters fetch into that cache only when the expected browser
is absent or stale.

## Health And Logs

```bash
curl -fsS http://127.0.0.1:18080/health
docker compose -f deploy/stormtrooper/docker-compose.yml ps
docker compose -f deploy/stormtrooper/docker-compose.yml logs --tail=200 bot
```

Healthy production should report:

```json
{"details":"connected","status":"ok","storage":"postgres"}
```

Scheduler logs should mention only these active loops unless a feature is
explicitly re-enabled:

- RFD
- eBay
- Memory Express
- Best Buy

After each deploy, watch one full scheduler cycle. Keep `docker compose ps`, the
health response, and `docker compose logs -f --tail=200 bot` open long enough to
see all four active loops run once.

The Compose project is explicitly named `rfd-discord-bot` to avoid collisions
with other Stormtrooper stacks. Keep the watchdog timer enabled so Docker
containers that are stopped or removed by a bad Compose run are brought back:

```bash
chmod +x deploy/stormtrooper/rfd-bot-watchdog.sh
mkdir -p ~/.config/systemd/user
cp deploy/stormtrooper/systemd/rfd-discord-bot-watchdog.* ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now rfd-discord-bot-watchdog.timer
systemctl --user list-timers rfd-discord-bot-watchdog.timer
```

Facebook and HardwareSwap remain disabled unless their feature flags are turned
on intentionally.

## Manual Processor Checks

Manual endpoints use the same no-overlap guard as scheduled runs. They are not
publicly routed and require the admin bearer token even on localhost:

```bash
curl -fsS -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/process-deals
curl -fsS -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/process-ebay
curl -fsS -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/process-memoryexpress
curl -fsS -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/process-bestbuy
```

Prime Best Buy baseline without Discord notifications:

```bash
curl -fsS -X POST -H "Authorization: Bearer $RFD_ADMIN_TOKEN" http://127.0.0.1:18080/prime-bestbuy-baseline
```

## Scrape Lab

Run from inside the container when validating scraping backends:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot \
  ./scrape-lab -from-store -sites ebay,memoryexpress,bestbuy \
  -backends http,external-stealth,camoufox,ai-crawler,paid-trial,bestbuy-algolia \
  -out /data/scrape-lab-$(date +%Y%m%d-%H%M%S)
```

Camoufox-only eBay sample:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec \
  -e SCRAPELAB_CAMOUFOX_COMMAND_ARGS='["xvfb-run","-a","/opt/scrape-venv/bin/python","scripts/camoufox_fetch.py","{url}","--wait-ms","7000"]' \
  bot ./scrape-lab -from-store -sites ebay -ebay-limit 3 \
  -backends camoufox -env stormtrooper \
  -out /data/scrape-lab-stormtrooper-ebay-camoufox-$(date +%Y%m%d-%H%M%S)
```

Browserless stays the final fallback for lab validation, but production should
leave the matching paid enable flag false unless a deploy intentionally opts in
with the Browserless token/argv command present.

## Discord Commands

After changing command definitions:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot ./register-commands
```

Current public interaction endpoint:

```text
https://bot.pauljones0.uk/discord/interactions
```

## Legacy Subscription Cleanup

Dry-run old deal/subscription values before deleting them:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot ./cleanup-legacy-subscriptions
```

Delete only after the dry run reports exactly the records you expect:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot ./cleanup-legacy-subscriptions -execute
```

## Hosted Runtime

The old hosted app runtime is intentionally absent. Stormtrooper is the active
runtime, and the repo no longer carries deploy workflow code for the previous
hosted scheduler/server setup.
