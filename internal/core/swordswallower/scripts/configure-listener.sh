#!/usr/bin/env bash
set -euo pipefail

ADB="${ADB:-adb}"
WEBHOOK_URL="${WEBHOOK_URL:-${SWORDSWALLOWER_WEBHOOK:-http://10.0.2.2:8787/ingest/discord-notification}}"
WEBHOOK_SECRET="${SWORDSWALLOWER_SECRET:-${RFD_ADMIN_TOKEN:-}}"
TARGET_PACKAGE="${TARGET_PACKAGE:-com.discord}"
ACTION_REGEX="${ACTION_REGEX:-(?i).*(mark\\s*(as\\s*)?read|read\\s*already|already\\s*read).*}"
AUTO_ACTION="${AUTO_ACTION:-true}"
CANCEL_FALLBACK="${CANCEL_FALLBACK:-false}"

${ADB} wait-for-device
${ADB} shell am start \
  -n dev.swordswallower/.MainActivity \
  --es webhookUrl "${WEBHOOK_URL}" \
  --es webhookSecret "${WEBHOOK_SECRET}" \
  --es targetPackage "${TARGET_PACKAGE}" \
  --es actionRegex "'${ACTION_REGEX}'" \
  --ez autoAction "${AUTO_ACTION}" \
  --ez cancelFallback "${CANCEL_FALLBACK}"

echo "Configured listener webhook: ${WEBHOOK_URL}"
