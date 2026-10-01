#!/usr/bin/env bash
# Cloud-init user data for a new dedicated Ubuntu 24.04 bot host. No credentials.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends docker.io docker-compose-v2 python3
systemctl enable --now docker
usermod -aG docker ubuntu
install -d -m 700 -o ubuntu -g ubuntu /home/ubuntu/bot-releases
printf 'Docker/Compose installed; restore bot state before activating producers.\n' > /var/lib/bot-host-ready
