#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "${ROOT_DIR}"
docker compose --profile android up -d --build android-emulator
docker compose exec -T android-emulator adb wait-for-device
until [[ "$(docker compose exec -T android-emulator adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == "1" ]]; do
  sleep 2
done
docker compose exec -T android-emulator adb shell input keyevent 82 >/dev/null 2>&1 || true
echo "Android boot completed"
