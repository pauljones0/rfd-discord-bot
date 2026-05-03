# Stormtrooper Migration Runbook

This runbook moves the bot from Cloud Run + Cloud Scheduler to Docker Compose on
Stormtrooper while keeping Firestore as the phase-1 source of truth.

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
- Firestore remains active in phase 1.

## Files On Stormtrooper

Create these preferred paths outside git:

```bash
sudo mkdir -p /opt/rfd-discord-bot /srv/appdata/rfd-discord-bot
sudo chown -R "$USER":"$USER" /opt/rfd-discord-bot /srv/appdata/rfd-discord-bot
```

`/opt/rfd-discord-bot/.env` should contain the production runtime variables.
Start with:

```bash
GOOGLE_CLOUD_PROJECT=may2025-01
LOCAL_SCHEDULER_ENABLED=false
RFD_POLL_INTERVAL=3m
EBAY_POLL_INTERVAL=30m
MEMEXPRESS_POLL_INTERVAL=30m
BESTBUY_POLL_INTERVAL=15m

EBAY_COUPON_BACKENDS=http,external-stealth
MEMEXPRESS_BACKENDS=chromedp-persistent,external-stealth,http
BESTBUY_BACKENDS=bestbuy-algolia,http
```

Keep Discord, Gemini, eBay, and optional Cloudflare Tunnel secrets in that same
env file. Do not commit it.

Mount the phase-1 Firestore credential JSON at:

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
docker compose -f deploy/stormtrooper/docker-compose.yml up -d --build bot
```

Health check:

```bash
curl -fsS http://127.0.0.1:18080/health
```

Run scrape lab against Firestore targets from inside the container:

```bash
docker compose -f deploy/stormtrooper/docker-compose.yml exec bot \
  ./scrape-lab -from-firestore -sites ebay,memoryexpress,bestbuy \
  -backends http,external-stealth,bestbuy-algolia \
  -out /data/scrape-lab-$(date +%Y%m%d-%H%M%S)
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

is reachable and `/health` returns Firestore connected.

## Cutover Order

1. Deploy Compose with `LOCAL_SCHEDULER_ENABLED=false`.
2. Confirm `/health` and scrape-lab evidence from Stormtrooper.
3. Prime Best Buy baseline with notifications disabled.
4. Enable local scheduler for one processor at a time by updating the env file
   and restarting Compose.
5. After each verified Stormtrooper run, pause the matching Cloud Scheduler job:
   Memory Express, eBay, RFD, then Best Buy when desired.
6. Keep Cloud Run deployed but idle for 24-48 hours as rollback.
7. Disable/delete old Cloud Scheduler and Cloud Run only after the local runs are
   stable.

## Phase 2

After parity is proven, migrate Firestore to a local Postgres container:

1. Add a storage abstraction or Postgres-backed store implementation.
2. Export Firestore collections.
3. Import into Postgres.
4. Switch storage config from Firestore to Postgres.
5. Keep Firestore read-only until a full polling and notification cycle passes.
