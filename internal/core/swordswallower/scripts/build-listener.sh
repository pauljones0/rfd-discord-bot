#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

"${ROOT_DIR}/scripts/android-docker.sh" gradle --no-daemon :app:assembleDebug

echo "Built ${ROOT_DIR}/app/build/outputs/apk/debug/app-debug.apk"
