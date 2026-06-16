#!/usr/bin/env bash
set -euo pipefail

AVD_NAME="${AVD_NAME:-swordswallower-api24}"
HEADLESS="${HEADLESS:-1}"

flags=(
  -avd "${AVD_NAME}"
  -no-audio
  -no-boot-anim
  -gpu swiftshader_indirect
  -memory 1536
  -cores 2
  -netfast
)

if [[ "${HEADLESS}" == "1" ]]; then
  flags+=(-no-window)
fi

emulator "${flags[@]}" "$@" &
pid="$!"
echo "Started emulator PID ${pid}"

adb wait-for-device
until [[ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" == "1" ]]; do
  sleep 2
done

adb shell input keyevent 82 >/dev/null 2>&1 || true
echo "Android boot completed for ${AVD_NAME}"
