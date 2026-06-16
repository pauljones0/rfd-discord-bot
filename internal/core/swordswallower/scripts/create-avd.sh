#!/usr/bin/env bash
set -euo pipefail

AVD_NAME="${AVD_NAME:-swordswallower-api24}"
SYSTEM_IMAGE="${SYSTEM_IMAGE:-system-images;android-24;google_apis;x86_64}"
DEVICE_PROFILE="${DEVICE_PROFILE:-pixel}"

yes | sdkmanager --licenses >/dev/null
sdkmanager \
  "platform-tools" \
  "emulator" \
  "platforms;android-24" \
  "${SYSTEM_IMAGE}"

if avdmanager list avd | grep -q "Name: ${AVD_NAME}$"; then
  echo "AVD already exists: ${AVD_NAME}"
  exit 0
fi

echo "no" | avdmanager create avd \
  --force \
  --name "${AVD_NAME}" \
  --package "${SYSTEM_IMAGE}" \
  --device "${DEVICE_PROFILE}"

AVD_CONFIG="${HOME}/.android/avd/${AVD_NAME}.avd/config.ini"
{
  echo "hw.ramSize=1536"
  echo "hw.cpu.ncore=2"
  echo "disk.dataPartition.size=4096M"
} >> "${AVD_CONFIG}"

echo "Created ${AVD_NAME}"
