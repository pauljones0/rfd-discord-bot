#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LISTENER_APK="${LISTENER_APK:-${ROOT_DIR}/app/build/outputs/apk/debug/app-debug.apk}"
DISCORD_APK="${DISCORD_APK:-}"
ADB="${ADB:-adb}"

if [[ ! -f "${LISTENER_APK}" ]]; then
  echo "Listener APK not found: ${LISTENER_APK}"
  echo "Run ./scripts/build-listener.sh first."
  exit 1
fi

${ADB} wait-for-device
${ADB} install -r "${LISTENER_APK}"

if [[ -n "${DISCORD_APK}" ]]; then
  if [[ -d "${DISCORD_APK}" ]]; then
    mapfile -t apk_files < <(find "${DISCORD_APK}" -maxdepth 1 -name '*.apk' -type f | sort)
    if [[ "${#apk_files[@]}" -eq 0 ]]; then
      echo "No APK files found in ${DISCORD_APK}"
      exit 1
    fi
    ${ADB} install-multiple -r "${apk_files[@]}"
  elif [[ -f "${DISCORD_APK}" ]]; then
    ${ADB} install -r "${DISCORD_APK}"
  else
    echo "DISCORD_APK does not exist: ${DISCORD_APK}"
    exit 1
  fi
else
  echo "Skipped Discord install. Set DISCORD_APK=/path/to/discord.apk or use Play Store."
fi
