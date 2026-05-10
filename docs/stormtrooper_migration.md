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
```

The `.env` file is outside git and should include:

```env
POSTGRES_PASSWORD=...
DATABASE_URL=postgres://rfd_bot:...@postgres:5432/rfd_discord_bot?sslmode=disable

LOCAL_SCHEDULER_ENABLED=true
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=30m

EBAY_COUPON_BACKENDS=http,external-stealth,camoufox,ai-crawler,paid-trial
EBAY_COUPON_DISCOVERY_INTERVAL=6h
EBAY_PAID_BROWSER_ENABLED=true
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
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build
```

For branch verification, check out the branch first and use the same Compose
command.

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

The Compose project is explicitly named `rfd-discord-bot` to avoid collisions
with other Stormtrooper stacks. Keep the watchdog timer enabled so Docker
containers that are stopped or removed by a bad Compose run are brought back:

```bash
sudo cp deploy/stormtrooper/systemd/rfd-discord-bot-watchdog.* /etc/systemd/system/
chmod +x deploy/stormtrooper/rfd-bot-watchdog.sh
sudo systemctl daemon-reload
sudo systemctl enable --now rfd-discord-bot-watchdog.timer
systemctl list-timers rfd-discord-bot-watchdog.timer
```

- RFD
- eBay
- Memory Express
- Best Buy

Facebook and HardwareSwap remain disabled unless their feature flags are turned
on intentionally.

## Manual Processor Checks

Manual endpoints use the same no-overlap guard as scheduled runs:

```bash
curl -fsS http://127.0.0.1:18080/process-deals
curl -fsS http://127.0.0.1:18080/process-ebay
curl -fsS http://127.0.0.1:18080/process-memoryexpress
curl -fsS http://127.0.0.1:18080/process-bestbuy
```

Prime Best Buy baseline without Discord notifications:

```bash
curl -fsS -X POST http://127.0.0.1:18080/prime-bestbuy-baseline
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
  -e SCRAPELAB_CAMOUFOX_COMMAND='xvfb-run -a /opt/scrape-venv/bin/python scripts/camoufox_fetch.py "{url}" --wait-ms 7000' \
  bot ./scrape-lab -from-store -sites ebay -ebay-limit 3 \
  -backends camoufox -env stormtrooper \
  -out /data/scrape-lab-stormtrooper-ebay-camoufox-$(date +%Y%m%d-%H%M%S)
```

Browserless stays the final fallback. `paid-trial` is inert unless the matching
paid enable flag is true and the Browserless token/command are present.

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
