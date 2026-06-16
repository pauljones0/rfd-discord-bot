#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "${ROOT_DIR}"
docker compose --profile android run --build --rm android-toolchain bash -c "/usr/local/bin/setup-android-sdk.sh && $*"
