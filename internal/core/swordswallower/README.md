# Swordswallower

Bundled Core tooling for an Android 7.0+ notification capture setup that can run
Discord on a logged-in device/emulator, forward Discord notification payloads to
`rfd-discord-bot`, and immediately invoke Discord's notification-level
`Mark as Read` action when Android exposes it.

This does not use a Discord user token, raw Discord API calls, or browser
automation. It only observes local Android notifications for an account already
logged in on the device.

## Current Shape

- `app/` is a small Android app with a `NotificationListenerService`.
- `tools/receiver.py` is an optional dependency-free local webhook receiver for
  debugging or bridging.
- `docker-compose.yml` can run the Android SDK/emulator stack in containers and
  defaults its emulator proxy to the `rfd-discord-bot` service on the existing
  `rfd-discord-bot_default` Docker network.
- `scripts/` contains Docker build, ADB, and Android 7.0 AVD helpers.
- `docker/android-build.Dockerfile` carries Java, Gradle, Android SDK tools,
  Android emulator, and the Android 7.0 x86_64 system image.

As of Discord's March 11, 2026 support page, Discord lists Android 7+ as the
minimum supported Android OS and Android 10+ as recommended:
https://support.discord.com/hc/en-us/articles/213491697-What-are-the-OS-system-requirements-for-Discord

## How The Mark-As-Read Trigger Works

Android notifications may include action buttons. A notification listener can
inspect `Notification.actions`; each action has a label and an `actionIntent`.
For Discord message notifications, the button is typically labeled like
`Mark as Read`. The listener matches that label, then calls
`action.actionIntent.send()`.

That clears Discord's notification state through Discord's own notification
action. It does not fetch more message text than Discord put into the Android
notification payload.

## Quick Start

Build the listener APK:

```bash
./scripts/build-listener.sh
```

Configure the listener to post directly to the bot:

```bash
RFD_ADMIN_TOKEN="$RFD_ADMIN_TOKEN" ./scripts/configure-listener.sh
```

The default Dockerized webhook URL is:

```text
http://10.0.2.2:8787/ingest/discord-notification
```

Inside the emulator, `10.0.2.2:8787` points to a proxy in the emulator
container, which forwards to `rfd-discord-bot:8080` on Docker's shared network.
The listener secret is sent as both `Authorization: Bearer ...` and
`X-Swordswallower-Secret`, so either `RFD_ADMIN_TOKEN` or
`SWORDSWALLOWER_SECRET` can authenticate the ingest route.

The bot endpoint is:

```text
POST /ingest/discord-notification
```

For receiver-side debugging, start a local receiver:

```bash
python3 tools/receiver.py
```

Or run the optional receiver sidecar that forwards into `rfd-discord-bot`:

```bash
FORWARD_BEARER_TOKEN="$RFD_ADMIN_TOKEN" make receiver-up
```

The receiver returns HTTP 502 when upstream forwarding fails, so failures are no
longer acknowledged as successful local deliveries.

Create and start an Android 7.0 AVD in Docker:

```bash
make create-avd-docker
make start-avd-docker
```

Install and configure the listener in the Dockerized emulator:

```bash
make install-apps-docker
make configure-listener-docker
make enable-listener-docker
```

After that, install/login to Discord once. The emulator can run headless after
login as long as the `android-avd` Docker volume is preserved.

Use the "Send test event" button in the listener app to verify end-to-end
delivery. The bot accepts test events without a Discord package name and reports
them into the configured Core Discord channel.

## Resource Notes

The configured AVD uses 2 virtual cores, 1536 MB RAM, and a 4 GB data partition.
The Android SDK/emulator image is expected to be several GB because it contains
Java, Gradle, SDK tools, the emulator, and the Android 7.0 system image. The AVD
state lives in the `android-avd` Docker volume and is where Discord's logged-in
session persists.

KVM is strongly preferred. This host currently has no `/dev/kvm`, and
`modprobe kvm_intel` returns `Operation not supported`, so Docker will not fix
hardware acceleration. With `ANDROID_ACCEL=off`, the emulator can be headless
but may be slow enough that a physical Android phone is the better runtime.

## Practical Limits

- This is notification capture only. It is not a full Discord client.
- Long messages can still be truncated before Android receives them.
- Notification grouping can still happen under high volume, though auto-read
  helps keep the notification queue from accumulating.
- One-time Discord login requires an interactive emulator window, scrcpy, or a
  real phone screen. The repo intentionally does not store credentials.
- Google Play Store images for Android 7.0 may be less convenient than newer
  images. The scripts support sideloading a user-provided APK instead.

## Useful Commands

```bash
make build-listener
make receiver
make receiver-up
make receiver-down
make check-host
make create-avd
make start-avd
make install-apps
make configure-listener
make enable-listener
make create-avd-docker
make start-avd-docker
make install-apps-docker
make configure-listener-docker
make enable-listener-docker
```
