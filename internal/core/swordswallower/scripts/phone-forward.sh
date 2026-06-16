#!/usr/bin/env bash
# Wait for the Samsung phone to be authorised over ADB, then set up reverse
# port forwarding so the on-device listener can reach the local receiver at
# 127.0.0.1:8787.
set -euo pipefail

ADB="${ADB:-adb}"
PHONE_PORT="${PHONE_PORT:-8787}"
HOST_PORT="${HOST_PORT:-8787}"

echo "Waiting for ADB device..."
"${ADB}" wait-for-device

SERIAL="$("${ADB}" get-serialno)"
echo "Device connected: ${SERIAL}"

"${ADB}" reverse "tcp:${PHONE_PORT}" "tcp:${HOST_PORT}"
echo "Reverse port forward active: phone:${PHONE_PORT} -> host:${HOST_PORT}"
