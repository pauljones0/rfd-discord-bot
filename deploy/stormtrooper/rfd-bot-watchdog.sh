#!/usr/bin/env sh
set -eu

APP_DIR="${RFD_BOT_APP_DIR:-/home/paul/agent-work/repos/rfd-discord-bot/deploy/stormtrooper}"
HEALTH_URL="${RFD_BOT_HEALTH_URL:-http://127.0.0.1:18080/health}"

cd "$APP_DIR"

postgres_status="$(docker inspect rfd-discord-bot-postgres --format '{{.State.Status}}' 2>/dev/null || true)"
postgres_health="$(docker inspect rfd-discord-bot-postgres --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' 2>/dev/null || true)"

if [ "$postgres_status" != "running" ] || [ "$postgres_health" != "healthy" ]; then
  echo "rfd-bot-watchdog: postgres status=${postgres_status:-missing} health=${postgres_health:-none}; starting compose services"
  docker compose up -d postgres
fi

if ! curl -fsS --max-time 5 "$HEALTH_URL" >/dev/null; then
  echo "rfd-bot-watchdog: health check failed; reconciling bot and postgres"
  docker compose up -d postgres bot
fi
