# Memory Express Local Runner

This is legacy documentation for `cmd/memoryexpress-local`.

The active production path is now the main Stormtrooper bot container with:

- `STORAGE_BACKEND=postgres`
- `LOCAL_SCHEDULER_ENABLED=true`
- `MEMEXPRESS_POLL_INTERVAL=30m`
- Memory Express processing through `/process-memoryexpress` or the built-in
  scheduler

The standalone local runner is kept as an experimental/manual fallback for
interactive Cloudflare troubleshooting. It is not part of the normal production
schedule.

## Run Manually

Use the standard bot environment plus any browser overrides:

```powershell
$env:GOOGLE_CLOUD_PROJECT="may2025-01"
$env:DISCORD_BOT_TOKEN="..."
$env:GEMINI_API_KEY="..."
$env:MEMEXPRESS_CHROME_PATH="C:\Program Files\Google\Chrome\Application\chrome.exe"
$env:MEMEXPRESS_CHROME_PROFILE_DIR="$env:LOCALAPPDATA\rfd-discord-bot\memoryexpress-chrome-profile"

go run ./cmd/memoryexpress-local
```

If this runner is revived for production, document the storage backend and
notification behavior before enabling it. For now, prefer the main Stormtrooper
service and scrape-lab evidence for backend decisions.
