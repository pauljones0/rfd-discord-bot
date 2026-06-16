#!/usr/bin/env bash
set -euo pipefail

ADB="${ADB:-adb}"

${ADB} wait-for-device
${ADB} shell monkey -p com.discord 1
