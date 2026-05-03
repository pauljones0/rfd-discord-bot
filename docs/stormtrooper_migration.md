# Stormtrooper Operations Runbook

Stormtrooper is the active production host. The bot runs with Docker Compose,
local Postgres, and the built-in scheduler. Firestore is retained only for
migration, rollback, and scrape-lab target discovery until we intentionally drop
that support.

## Runtime Paths

Preferred user-owned paths:

```bash
$HOME/rfd-discord-bot
$HOME/.config/rfd-discord-bot/.env
$HOME/.config/rfd-discord-bot/adc.json
$HOME/appdata/rfd-discord-bot
$HOME/appdata/rfd-discord-bot/postgres
```

The `.env` file is outside git and should include:

```env
STORAGE_BACKEND=postgres
POSTGRES_PASSWORD=...
DATABASE_URL=postgres://rfd_bot:...@postgres:5432/rfd_discord_bot?sslmode=disable

LOCAL_SCHEDULER_ENABLED=true
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=30m

EBAY_COUPON_BACKENDS=http,external-stealth,paid-trial
EBAY_COUPON_DISCOVERY_INTERVAL=6h
EBAY_COUPON_SAMPLE_SIZE=3
MEMEXPRESS_BACKENDS=chromedp-persistent,external-stealth,http
BESTBUY_BACKENDS=bestbuy-algolia,http
```

Keep Discord, Gemini, eBay, Cloudflare Tunnel, and optional Browserless secrets
in that same file. Do not commit it.

## Deploy Or Redeploy

```bash
cd ~/rfd-discord-bot
git pull --ff-only origin main
RFD_BOT_ENV_FILE=$HOME/.config/rfd-discord-bot/.env \
RFD_BOT_GCP_CREDENTIALS=$HOME/.config/rfd-discord-bot/adc.json \
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

- RFD
- eBay
- Memory Express
- Best Buy

Facebook remains paused/out of scope for the current local production cycle.

## Manual Processor Checks

Manual endpoints use the same no-overlap guard as scheduled runs:

```bash
curl -fsS http://127.0.0.1:18080/process
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
  ./scrape-lab -from-firestore -sites ebay,memoryexpress,bestbuy \
  -backends http,external-stealth,bestbuy-algolia \
  -out /data/scrape-lab-$(date +%Y%m%d-%H%M%S)
```

Camoufox-only eBay sample:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec \
  -e SCRAPELAB_EXTERNAL_STEALTH_COMMAND='xvfb-run -a /opt/scrape-venv/bin/python scripts/camoufox_fetch.py "{url}" --wait-ms 7000' \
  bot ./scrape-lab -from-firestore -sites ebay -ebay-limit 3 \
  -backends external-stealth -env stormtrooper \
  -out /data/scrape-lab-stormtrooper-ebay-camoufox-$(date +%Y%m%d-%H%M%S)
```

Browserless stays a controlled trial path. Do not add `paid-trial` to production
backends unless scrape-lab proves it returns useful eBay listing HTML and coupon
parsing works on the capped sample.

## Discord Commands

After changing command definitions:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot ./register-commands
```

Current public interaction endpoint:

```text
https://bot.pauljones0.uk/discord/interactions
```

## Firestore Rollback And Migration

Firestore support is still present for rollback and data movement:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot \
  ./migrate-store -project may2025-01 -verify
```

Keep the ADC/service-account JSON outside git. The Compose file mounts it
read-only at `/run/secrets/gcp-sa-key.json`.

Do not delete Firestore until Postgres has passed enough production scheduler
cycles that rollback is no longer useful.

## GCP Shutdown Check

The app runtime should not depend on Cloud Run or Cloud Scheduler anymore. A
read-only check should return zero services/jobs for the old project and region:

```bash
gcloud scheduler jobs list --project may2025-01 --location us-central1
gcloud run services list --project may2025-01 --region us-central1
```

The GitHub Actions Cloud Run deploy workflow is intentionally absent.
