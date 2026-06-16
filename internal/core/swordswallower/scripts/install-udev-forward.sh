#!/usr/bin/env bash
# Installs a udev rule that automatically runs phone-forward.sh whenever
# the Samsung SM-G930W8 (idVendor=04e8, idProduct=6860) is plugged in.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RULE_FILE="/etc/udev/rules.d/99-swordswallower-forward.rules"
ADB_PATH="$(command -v adb)"
FORWARD_SCRIPT="${SCRIPT_DIR}/phone-forward.sh"

chmod +x "${FORWARD_SCRIPT}"

# Samsung idVendor is 04e8. We match on the authorised ADB interface.
cat <<EOF | sudo tee "${RULE_FILE}" > /dev/null
# Swordswallower: auto ADB reverse port forward on Samsung phone connect
ACTION=="add", SUBSYSTEM=="usb", ATTR{idVendor}=="04e8", \\
  RUN+="${ADB_PATH} -s \$attr{serial} wait-for-device", \\
  RUN+="${ADB_PATH} -s \$attr{serial} reverse tcp:8787 tcp:8787"
EOF

sudo udevadm control --reload-rules
echo "udev rule installed: ${RULE_FILE}"
echo "The port forward will be set up automatically next time you plug in the phone."
