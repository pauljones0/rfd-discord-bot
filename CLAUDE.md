# RFD Discord Bot

## Project Overview
Go 1.26 Cloud Run service that scrapes deal sources (RedFlagDeals, eBay, Facebook Marketplace), analyzes them with Gemini AI, and posts notifications to Discord channels.

## Architecture
- **Entry point**: `cmd/server/main.go` — HTTP server with endpoints for each processor
- **Command registration**: `cmd/register-commands/main.go` — Registers Discord slash commands
- **Processors**: Each deal source has its own package under `internal/`
  - `internal/processor/` — RFD deal scraping and processing
  - `internal/ebay/` — eBay deal processing via Browse API
  - `internal/facebook/` — Facebook Marketplace car deal scraping via Playwright (uses ProxyScrape residential proxy)
- **Shared packages**: `ai/`, `api/`, `config/`, `logger/`, `models/`, `notifier/`, `storage/`, `util/`, `validator/`
- **Separation of concerns**: Processor packages (processor, ebay, facebook) must NEVER import each other. They only import shared packages.

## Key Patterns
- **Concurrency**: Semaphores limit concurrent processing (2 for RFD, 1 for eBay, 1 for Facebook)
- **AI**: `internal/ai/gemini.go` — Multi-region Vertex AI with model tier failover and quota management
- **Storage**: Firestore collections: `deals` (RFD), `ebay_sellers`, `car_deals` (Facebook), `subscriptions`, `bot_config`, `price_history`
- **Subscriptions**: Multi-type per channel via `SubscriptionType` field. Doc ID: `{guildID}_{channelID}_{type}` (Facebook adds `_{city}`)
- **Discord**: HTTP API calls via `internal/notifier/`, slash commands via `internal/api/interactions.go`
- **Logging**: `log/slog` with Cloud Logging severity mapping. Always include `"processor"` key.

## Build & Test
```bash
go build ./...
go test ./... -count=1
go run cmd/register-commands/main.go  # Register Discord slash commands
```

## Deployment
- Push to `main` → GitHub Actions deploys to Cloud Run (`us-central1`)
- Cloud Scheduler triggers `/process-deals` (every minute), `/process-ebay` (every 15 min), `/process-facebook` (every 2 min)
- Secrets managed via GitHub Secrets, synced with `scripts/sync-secrets.ps1`

## Environment Variables
Required: `GOOGLE_CLOUD_PROJECT`, `DISCORD_APP_ID`, `DISCORD_BOT_TOKEN`, `DISCORD_PUBLIC_KEY`, `GEMINI_API_KEY`
Optional: `EBAY_CLIENT_ID`, `EBAY_CLIENT_SECRET`, `PROXY_URL` (for Facebook scraping)

## Important Notes
- Facebook scraping requires Playwright + Firefox + residential proxy (ProxyScrape)
- eBay features gracefully disable if credentials missing
- Facebook features gracefully disable if proxy not configured
- AI client is shared across all processors — do NOT create separate Gemini clients
- Proxy credentials must NEVER appear in logs (use `maskProxyURL()`)
