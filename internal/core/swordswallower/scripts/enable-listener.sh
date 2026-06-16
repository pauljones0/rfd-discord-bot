#!/usr/bin/env bash
set -euo pipefail

COMPONENT="${COMPONENT:-dev.swordswallower/dev.swordswallower.DiscordNotificationListenerService}"
ADB="${ADB:-adb}"

${ADB} wait-for-device
existing="$(${ADB} shell settings get secure enabled_notification_listeners | tr -d '\r')"

if [[ "${existing}" == "null" || -z "${existing}" ]]; then
  updated="${COMPONENT}"
elif [[ ":${existing}:" == *":${COMPONENT}:"* ]]; then
  updated="${existing}"
else
  updated="${existing}:${COMPONENT}"
fi

${ADB} shell settings put secure enabled_notification_listeners "${updated}"
${ADB} shell am force-stop dev.swordswallower >/dev/null 2>&1 || true
${ADB} shell monkey -p dev.swordswallower 1 >/dev/null 2>&1 || true

echo "Enabled notification listener: ${COMPONENT}"
