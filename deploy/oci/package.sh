#!/usr/bin/env bash
# Build locally, bundle only runtime image/config. Run on the destination architecture
# or a Docker builder configured for that platform. No credentials/state in bundles.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
output=${1:?Usage: package.sh NEW_OUTPUT_DIRECTORY [linux/amd64|linux/arm64]}
platform=${2:-linux/$(docker version --format '{{.Server.Arch}}')}
[[ ! -e "$output" ]] || { echo 'Output must not exist.' >&2; exit 1; }
mkdir -m 700 -- "$output"
output=$(cd -- "$output" && pwd)
image=rfd-bot:rust
trap 'rm -f -- "$output/image.tar.gz.tmp"' EXIT
docker build --platform "$platform" --target runtime --tag "$image" "$root"
docker save "$image" | gzip -1 > "$output/image.tar.gz.tmp"
mv -- "$output/image.tar.gz.tmp" "$output/image.tar.gz"
cp -- "$root/compose.yaml" "$output/compose.yaml"
cp -- "$root/.env.example" "$output/env.example"
cp -- "$root/deploy/oci/manage.sh" "$output/manage.sh"
docker image inspect --format '{{.Architecture}}' "$image" > "$output/architecture"
(cd -- "$output" && sha256sum image.tar.gz compose.yaml env.example manage.sh architecture > SHA256SUMS)
echo "Runtime bundle ready: $output ($platform). Credentials and databases must be transferred separately."
