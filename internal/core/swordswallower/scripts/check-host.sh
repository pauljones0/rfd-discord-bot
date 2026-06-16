#!/usr/bin/env bash
set -euo pipefail

missing=0
for command in adb emulator sdkmanager avdmanager; do
  if ! command -v "${command}" >/dev/null 2>&1; then
    echo "missing: ${command}"
    missing=1
  else
    echo "ok: ${command} -> $(command -v "${command}")"
  fi
done

if [[ -e /dev/kvm ]]; then
  if [[ -r /dev/kvm && -w /dev/kvm ]]; then
    echo "ok: /dev/kvm is accessible"
  else
    echo "warn: /dev/kvm exists but is not readable/writable by this user"
  fi
else
  echo "warn: /dev/kvm is missing; the Android emulator will be slow"
fi

if [[ "${missing}" -ne 0 ]]; then
  echo
  echo "Install Android command line tools, then rerun this script."
  exit 1
fi
