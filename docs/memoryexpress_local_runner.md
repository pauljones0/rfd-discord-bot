# Memory Express Local Runner

This runner moves Memory Express scraping off Cloud Run and onto your local machine while keeping:

- GCP Firestore as the system of record
- Gemini analysis
- Discord notifications

It uses a persistent headed Chrome profile with one tab per subscribed Memory Express store. If Cloudflare appears, the runner pauses on that tab, shows a desktop notification, and resumes automatically after the challenge is cleared.

## Why this exists

Cloud Run was getting blocked by Cloudflare even with browser automation. The local runner keeps the Memory Express session on your residential connection and preserves cookies in a real Chrome profile.

## Required setup

1. Sign in for Google Application Default Credentials:

```powershell
gcloud auth application-default login
```

2. Set the environment variables the bot already uses:

```powershell
$env:GOOGLE_CLOUD_PROJECT="may2025-01"
$env:DISCORD_BOT_TOKEN="..."
$env:GEMINI_API_KEY="..."
```

3. Optionally set local runner overrides:

```powershell
$env:MEMEXPRESS_POLL_INTERVAL="30m"
$env:MEMEXPRESS_CHROME_PATH="C:\Program Files\Google\Chrome\Application\chrome.exe"
$env:MEMEXPRESS_CHROME_PROFILE_DIR="$env:LOCALAPPDATA\rfd-discord-bot\memoryexpress-chrome-profile"
```

`MEMEXPRESS_CHROME_PATH` is optional if Chrome is already discoverable.

`MEMEXPRESS_CHROME_PROFILE_DIR` is optional. If omitted, the runner creates a persistent profile under your local user cache directory.

## Run it

```powershell
go run ./cmd/memoryexpress-local
```

## Runtime behavior

- The runner loads active Memory Express subscriptions from Firestore before each cycle.
- It keeps exactly one tab open for each subscribed store.
- It refreshes the subscribed store tabs on the poll interval.
- If a tab hits Cloudflare, the runner brings that tab to the front, sends a desktop alert, and waits.
- Once the page clears, scraping resumes automatically.

## Recommended ops change

If you are fully moving Memory Express to local-only operation, stop the Cloud Scheduler job that calls `/process-memoryexpress` so Cloud Run does not keep producing expected scrape failures.
