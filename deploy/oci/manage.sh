#!/usr/bin/env bash
# Same bundle/volumes on Oracle, another VM, or local Docker. Never auto-register commands.
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd -- "$root"
project=${BOT_PROJECT:-rfd-cloud}
export BOT_IMAGE=rfd-bot:rust
compose=(docker compose -p "$project" -f compose.yaml)
case ${1:-help} in
 load)
   sha256sum --check --quiet SHA256SUMS
   case $(uname -m) in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported host architecture.' >&2;exit 1;; esac
   [[ $(cat architecture) == "$arch" ]] || { echo 'Bundle architecture does not match this host.' >&2;exit 1; }
   gzip -dc image.tar.gz | docker load
   docker run --rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges "$BOT_IMAGE" version
   ;;
 check|start|stop|health|backup)
   [[ -f .env ]] || { echo 'Supply this bot own .env (mode 0600) first.' >&2;exit 1; }
   chmod 600 .env
   case $1 in
     check) "${compose[@]}" run --rm --no-deps bot check-config; "${compose[@]}" run --rm --no-deps bot check-storage;;
     start) "${compose[@]}" up -d --no-build;;
     stop) "${compose[@]}" stop -t 60;;
     health) "${compose[@]}" exec -T bot /rfd-bot healthcheck;;
     backup) name="snapshot-$(date -u +%Y%m%dT%H%M%SZ).sqlite"; "${compose[@]}" exec -T bot /rfd-bot backup --destination "/data/$name"; echo "Copy $name from ${project}_rfd-data to an existing off-VM machine, then remove the volume copy after verification.";;
   esac
   ;;
 *) echo 'Usage: manage.sh {load|check|start|stop|health|backup}';echo 'Supply .env and restore state BEFORE start. Stop previous producers/watchdogs before moving the same identities.';;
esac
