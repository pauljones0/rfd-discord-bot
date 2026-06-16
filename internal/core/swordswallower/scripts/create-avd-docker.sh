#!/usr/bin/env bash
set -euo pipefail

AVD_NAME="${AVD_NAME:-swordswallower-api24}"
SYSTEM_IMAGE="${SYSTEM_IMAGE:-system-images;android-24;google_apis;x86_64}"
DEVICE_PROFILE="${DEVICE_PROFILE:-pixel}"

"$(dirname "${BASH_SOURCE[0]}")/android-docker.sh" bash -lc "
set -euo pipefail
yes | sdkmanager --licenses >/dev/null
if avdmanager list avd | grep -q 'Name: ${AVD_NAME}$'; then
  echo 'AVD already exists: ${AVD_NAME}'
  exit 0
fi
echo no | avdmanager create avd --force --name '${AVD_NAME}' --package '${SYSTEM_IMAGE}' --device '${DEVICE_PROFILE}'
cat >> \"\${ANDROID_AVD_HOME}/${AVD_NAME}.avd/config.ini\" <<'EOF'
hw.ramSize=1536
hw.cpu.ncore=2
disk.dataPartition.size=4096M
EOF
echo 'Created ${AVD_NAME}'
"
