#!/usr/bin/env bash
# Package a tested static Rust binary built on a matching or cross build host.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
output=${1:?Usage: package-binary.sh NEW_DIRECTORY STATIC_BINARY linux/amd64_OR_linux/arm64}
binary=${2:?Supply tested static binary}
platform=${3:?Supply linux/amd64 or linux/arm64}
[[ ! -e "$output" ]] || { echo 'Output must not exist.' >&2;exit 1; }
[[ -f "$binary" && -x "$binary" ]] || { echo 'Missing executable binary.' >&2;exit 1; }
readelf -h "$binary" >/dev/null
[[ $(readelf -d "$binary" | grep -c '(NEEDED)' || true) == 0 && $(readelf -l "$binary" | grep -c INTERP || true) == 0 ]] || { echo 'Binary must be static.' >&2;exit 1; }
case $platform in linux/arm64) readelf -h "$binary" | grep -q AArch64;;linux/amd64) readelf -h "$binary" | grep -q 'X86-64';;*) echo 'Unsupported platform.' >&2;exit 1;;esac
staged=$(mktemp -d)
trap 'rm -rf -- "$staged"' EXIT
mkdir "$staged/runtime" "$staged/data"
cp -- "$binary" "$staged/runtime/rfd-bot"
:
mkdir -m 700 -- "$output"
output=$(cd -- "$output" && pwd)
docker build --platform "$platform" -f "$root/deploy/oci/Dockerfile.binary" -t rfd-bot:rust "$staged"
docker save rfd-bot:rust | gzip -1 > "$output/image.tar.gz"
cp -- "$root/compose.yaml" "$output/compose.yaml"
cp -- "$root/.env.example" "$output/env.example"
cp -- "$root/deploy/oci/manage.sh" "$output/manage.sh"
docker image inspect --format '{{.Architecture}}' rfd-bot:rust > "$output/architecture"
(cd -- "$output" && sha256sum image.tar.gz compose.yaml env.example manage.sh architecture > SHA256SUMS)
echo "Tested binary runtime bundle ready: $output ($platform)."
