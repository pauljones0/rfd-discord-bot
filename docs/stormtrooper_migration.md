# Stormtrooper Migration Runbook

This runbook moves the bot from Cloud Run + Cloud Scheduler to Docker Compose on
Stormtrooper with local Postgres as the runtime store. Firestore is retained as
the migration source and rollback backup until parity is verified.

## Current Production Snapshot

- Cloud Run service: `rfd-discord-bot`
- GCP project/region: `may2025-01/us-central1`
- Deployed commit observed before migration work: `cbdcb99`
- Cloud Scheduler cadence:
  - RFD: every `3m`
  - eBay: every `30m`
  - Memory Express: every `30m`
  - Best Buy: paused
  - Facebook: paused
- Firestore remains available as the backup/export source; runtime should use
  `STORAGE_BACKEND=postgres` after migration.

## Files On Stormtrooper

Create these preferred paths outside git:

```bash
sudo mkdir -p /opt/rfd-discord-bot /srv/appdata/rfd-discord-bot /srv/appdata/rfd-discord-bot/postgres
sudo chown -R "$USER":"$USER" /opt/rfd-discord-bot /srv/appdata/rfd-discord-bot
```

`/opt/rfd-discord-bot/.env` should contain the production runtime variables.
Start with:

```bash
GOOGLE_CLOUD_PROJECT=may2025-01
LOCAL_SCHEDULER_ENABLED=false
STORAGE_BACKEND=postgres
POSTGRES_PASSWORD=generate-a-long-local-password
DATABASE_URL=postgres://rfd_bot:generate-a-long-local-password@postgres:5432/rfd_discord_bot?sslmode=disable
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=30m

EBAY_COUPON_BACKENDS=http,external-stealth,paid-trial
EBAY_COUPON_DISCOVERY_INTERVAL=6h
EBAY_COUPON_SAMPLE_SIZE=3
MEMEXPRESS_BACKENDS=chromedp-persistent,external-stealth,http
BESTBUY_BACKENDS=bestbuy-algolia,http
EBAY_COUPON_EXTERNAL_STEALTH_COMMAND=xvfb-run -a /opt/scrape-venv/bin/python scripts/camoufox_fetch.py "{url}" --wait-ms 7000
```

Keep Discord, Gemini, eBay, and optional Cloudflare Tunnel secrets in that same
env file. Do not commit it.

Keep a Firestore credential JSON for migration and rollback checks at:

```text
/opt/rfd-discord-bot/gcp-sa-key.json
```

This can be either a service-account key with Firestore permissions or an
Application Default Credentials JSON exported from `gcloud auth
application-default login`. The Compose file mounts it read-only and sets:

```text
GOOGLE_APPLICATION_CREDENTIALS=/run/secrets/gcp-sa-key.json
```

If the SSH user does not have root access, use user-owned paths and pass these
environment overrides to Docker Compose:

```bash
export RFD_BOT_ENV_FILE="$HOME/.config/rfd-discord-bot/.env"
export RFD_BOT_GCP_CREDENTIALS="$HOME/.config/rfd-discord-bot/adc.json"
export RFD_BOT_DATA_DIR="$HOME/appdata/rfd-discord-bot"
```

## Deploy

From the repo root on Stormtrooper:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build
```

Health check:

```bash
curl -fsS http://127.0.0.1:18080/health
```

Migrate Firestore into local Postgres from inside the running bot container:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot \
  ./migrate-store -project may2025-01
```

The migration writes every top-level Firestore collection to the local JSONB
document table while preserving document IDs, then prints per-collection counts.
Run it again with `-verify-only` for a cheap parity check after the first import.

Run scrape lab against Firestore targets from inside the container:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot \
  ./scrape-lab -from-firestore -sites ebay,memoryexpress,bestbuy \
  -backends http,external-stealth,bestbuy-algolia \
  -out /data/scrape-lab-$(date +%Y%m%d-%H%M%S)
```

To verify the eBay Camoufox fallback specifically, keep the scheduler disabled
and run only three Firestore-backed item pages:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec \
  -e SCRAPELAB_EXTERNAL_STEALTH_COMMAND='xvfb-run -a /opt/scrape-venv/bin/python scripts/camoufox_fetch.py "{url}" --wait-ms 7000' \
  bot ./scrape-lab -from-firestore -sites ebay -ebay-limit 3 \
  -backends external-stealth -env stormtrooper \
  -out /data/scrape-lab-stormtrooper-ebay-camoufox-$(date +%Y%m%d-%H%M%S)
```

Only if Camoufox is blocked or errors on all three samples, run the Browserless
paid trial with the same target cap:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec \
  -e BROWSERLESS_TOKEN="$BROWSERLESS_TOKEN" \
  -e SCRAPELAB_PAID_TRIAL_COMMAND='/opt/scrape-venv/bin/python scripts/browserless_bql_fetch.py "{url}" --wait-ms 5000' \
  bot ./scrape-lab -from-firestore -sites ebay -ebay-limit 3 \
  -backends paid-trial -env stormtrooper \
  -out /data/scrape-lab-stormtrooper-ebay-browserless-$(date +%Y%m%d-%H%M%S)
```

Prime Best Buy before enabling subscriptions or the scheduler:

```bash
curl -fsS -X POST http://127.0.0.1:18080/prime-bestbuy-baseline
```

This saves current configured-seller inventory without Discord notifications.

## Public Ingress

Preferred Cloudflare Tunnel:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml --profile tunnel up -d
```

The tunnel token belongs in `/opt/rfd-discord-bot/.env` as
`CLOUDFLARED_TUNNEL_TOKEN=...` or `TUNNEL_TOKEN=...` if using Cloudflare's
token-based `cloudflared` image flow.

Caddy fallback:

Add `deploy/stormtrooper/Caddyfile.fragment` to Stormtrooper's Caddyfile and
reload Caddy. The route proxies `https://bot.pauljones0.uk` to
`127.0.0.1:18080`.

Update the Discord Developer Portal interaction endpoint only after:

```text
https://bot.pauljones0.uk/discord/interactions
```

is reachable and `/health` returns `{"storage":"postgres","details":"connected"}`.

## Cutover Order

1. Deploy Compose with `LOCAL_SCHEDULER_ENABLED=false`.
2. Run `migrate-store` and confirm `/health` reports Postgres connected.
3. Confirm scrape-lab evidence from Stormtrooper.
4. Prime Best Buy baseline with notifications disabled.
5. Enable `LOCAL_SCHEDULER_ENABLED=true` with RFD `3m` and eBay, Memory
   Express, and Best Buy `30m`.
6. Verify one local run of each enabled processor.
7. Re-register Discord commands from Stormtrooper:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot ./register-commands
```

8. Pause/delete Cloud Scheduler jobs and delete Cloud Run after backup/export
   verification. Do not delete Firestore until Postgres has passed a full
   polling and notification cycle.
