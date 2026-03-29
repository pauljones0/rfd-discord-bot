# Reddit Processors Design Spec

## Overview

Port the betterHardwareSwap bot into this repo and add r/bapcsalescanada support. Uses a local Reddit relay service (cloudflared tunnel) to bypass Cloud Run IP blocks on Reddit's unofficial JSON API.

Three new packages:
- `internal/reddit/` — Shared Reddit scraping code and models
- `internal/hardwareswap/` — Full alert system ported from betterHardwareSwap
- `internal/bapcsales/` — Simple warm/hot detector (RFD-style)

Plus a new local companion service:
- `cmd/reddit-service/` — HTTP relay for Reddit JSON API

## Architecture: Approach 3 (Shared Reddit Package + Thin Processor Wrappers)

```
Cloud Scheduler
  ├─► POST /process-hardwareswap (Cloud Run)
  │     └─► reddit.FetchPosts("CanadianHardwareSwap")
  │           └─► GET relay-service/reddit?subreddit=...
  │                 └─► reddit.com/r/.json (from local machine)
  │
  └─► POST /process-bapcsales (Cloud Run)
        └─► reddit.FetchPosts("bapcsalescanada")
              └─► GET relay-service/reddit?subreddit=...
                    └─► reddit.com/r/.json (from local machine)
```

Processors import `internal/reddit/` but never import each other. `internal/reddit/` is a shared package (like `ai/`, `storage/`), not a processor.

---

## 1. Reddit Relay Service (`cmd/reddit-service/main.go`)

Minimal HTTP server running locally on port 8082, exposed via cloudflared quick tunnel.

### Endpoints

- `GET /reddit?subreddit={name}` — Fetches `reddit.com/r/{name}/.json`, returns raw JSON
- `GET /health` — Returns `{"ready": true}`

### Behavior

- Stateless pass-through, no caching or storage
- Exponential backoff retry on Reddit 429/5xx (3 retries max, 2s/4s/8s backoff)
- Custom User-Agent: `script:canadianhardwareswapbot:v2.0 (by u/pauljones0)`
- Bearer token auth via `REDDIT_SERVICE_SECRET` env var
- Filters out AutoModerator posts before returning (cheap, universally unwanted)

### Tunnel Setup

- Own PowerShell script: `scripts/reddit-service-local.ps1` (modeled on `scripts/token-service-local.ps1`)
- Runs cloudflared with `--url http://localhost:8082`
- Registers URL to Firestore via `POST /register-reddit-service` on Cloud Run
- Cloud Run resolves URL dynamically from Firestore on each processor run (fallback to `REDDIT_SERVICE_URL` env var)

---

## 2. Shared Reddit Package (`internal/reddit/`)

### Models (`models.go`)

```go
type Post struct {
    ID                  string
    Title               string
    SelfText            string
    URL                 string
    Permalink           string
    Subreddit           string
    Author              string
    Score               int
    NumComments         int
    LinkFlairText       string
    RemovedByCategory   string
    CreatedUtc          float64
    Thumbnail           string
}

type ListingResponse struct {
    // Wraps Reddit JSON structure for deserialization
}
```

### Scraper (`scraper.go`)

- `RedditClient` struct with relay service URL (resolved from Firestore, fallback to env var)
- `FetchPosts(ctx, subreddit string) ([]Post, error)` — calls relay, deserializes response
- Retries only on tunnel/network failures (relay unreachable). Reddit-level retries handled by the relay service.
- Returns clean `[]Post` for processors to consume

No awareness of alerts, warm/hot detection, or Discord.

---

## 3. HardwareSwap Processor (`internal/hardwareswap/`)

Full port from betterHardwareSwap. Alert-based per-user notifications.

### Processor Pipeline (`processor.go`)

1. Fetch posts via `reddit.RedditClient.FetchPosts(ctx, "CanadianHardwareSwap")`
2. Filter out already-processed posts (Firestore `posts` collection by RedditID)
3. For each new post: Gemini `CleanRedditPost()` — extract title, price, location, condition
4. Match cleaned post against all user alerts using Boolean matcher
5. For matched alerts: send embed to server's feed channel, ping user in ping channel
6. For existing posts: check flair changes (Sold/Closed), edit Discord messages with strikethrough
7. Trim old posts (keep 500 most recent)

Semaphore: 1 concurrent run.

### Alert System

**Slash commands (integrated into `internal/api/interactions.go`):**
- `/alert add` — AI wizard (Gemini converts natural language to Boolean query) or manual Boolean entry
- `/alert list` — shows user's alerts with edit/delete buttons
- `/setup [feed_channel] [ping_channel]` — configure server (admin only)
- `/help` — usage guide

**Interaction handling:**
- Modal submissions for AI wizard and manual query input
- Button components for confirm/cancel/edit/delete alerts
- Per-user rate limiting (token bucket, 1 req/2s)
- Deferred responses for async Gemini processing

### Matching (`matcher.go`)

- Word-boundary-aware regex matching (ported from betterHardwareSwap)
- MustHave (AND), AnyOf (OR), MustNot (NOT) Boolean logic
- Matches against cleaned post corpus (title + description + location)

### AI Prompts (`prompts.go`)

- `CleanRedditPost` — parse messy Reddit posts into structured fields
- `KeywordWizard` — natural language to Boolean query conversion
- `ValidateManualQuery` — validate manual Boolean syntax
- Dynamic prompt storage in Firestore `system_prompts` collection

### Storage (Firestore)

- `posts` collection — processed posts, keyed by RedditID, tracks Discord message IDs per server
- `alerts` collection — per-user Boolean queries (UserID, ServerID, MustHave, AnyOf, MustNot, RawQuery, CreatedAt)
- `servers` collection — per-guild config (FeedChannelID, PingChannelID, UpdatedAt)

### What Changes from Original betterHardwareSwap

- Reddit scraping goes through relay service instead of direct HTTP
- Uses this repo's shared `ai.Client` (not its own Gemini client)
- Uses this repo's logging patterns (`log/slog` with `"processor": "hardwareswap"`)
- Discord REST calls use this repo's existing HTTP client patterns

---

## 4. BAPCSalesCanada Processor (`internal/bapcsales/`)

Simple warm/hot detector modeled after the RFD processor. Channel-level subscriptions, no per-user alerts.

### Processor Pipeline (`processor.go`)

1. Fetch posts via `reddit.RedditClient.FetchPosts(ctx, "bapcsalescanada")`
2. Generate stable IDs (SHA256 of RedditID — consistent with RFD pattern)
3. Batch fetch existing from Firestore, skip already-processed
4. Dedup within batch (fuzzy token matching, same approach as RFD)
5. Single-pass Gemini analysis: clean title, is_warm, is_lava_hot
6. Match against channel subscriptions using existing eligibility system
7. Send Discord embeds to eligible channels, track message IDs
8. Update existing deals if engagement metrics changed significantly (1-hour window)
9. Batch save to Firestore, trim old entries

Semaphore: 1 concurrent run.

### Subscription Types (added to existing system)

- `bapcs_all` — all posts
- `bapcs_warm_hot` — warm + hot only
- `bapcs_hot` — hot only
- Subscription type: `bapcsales`

### Storage

- New Firestore collection: `bapcs_deals` (same schema as `deals`, separate collection)
- Uses existing `subscriptions` collection with `SubscriptionType: "bapcsales"`

### Discord Embeds

- Similar format to RFD: clean title, Reddit link, score/comments metrics, warm/hot color coding
- Footer shows subreddit name + flair if present

### AI Prompt

- Adapted from RFD warm/hot analysis, tuned for Reddit context
- Uses score, num_comments, flair, post content
- Adjusted thresholds for smaller community size (r/bapcsalescanada vs RFD)

### Explicitly Stubbed for Later

```go
// TODO: Per-user alert system (like hardwareswap) can be implemented later
// TODO: Retailer extraction and price tracking can be implemented later
// TODO: Deal URL extraction (links to actual retailer) can be implemented later
// TODO: Thread aggregation (multiple posts about same deal) can be implemented later
```

---

## 5. Integration Points

### `cmd/server/main.go`

- New endpoints: `/process-hardwareswap`, `/process-bapcsales`, `/register-reddit-service`
- New semaphores: 1 each for hardwareswap and bapcsales
- Both processors initialized with shared `ai.Client`, Firestore client, notifier
- Reddit service URL resolved from Firestore (same pattern as Carfax token service)
- HardwareSwap processor nil-safe (skip if config missing, like eBay pattern)

### `internal/api/interactions.go`

- New slash command cases: `/alert` (add, list), `/setup`, `/help` (names may need namespacing like `/hardwareswap-setup` if they conflict with existing commands — verify during implementation)
- New component handler cases: alert confirm/cancel/edit/delete buttons
- New modal handler cases: AI wizard submission, manual query submission
- Routes to `internal/hardwareswap/` functions

### `cmd/register-commands/main.go`

- Register new slash commands: `alert` (subcommands: add, list), `setup`, `help`
- Run `go run cmd/register-commands/main.go` after changes

### `internal/models/subscription.go`

- New subscription type: `bapcsales`
- New deal types: `bapcs_all`, `bapcs_warm_hot`, `bapcs_hot`
- New eligibility check functions

### `internal/notifier/discord.go`

- New `SendBAPCSDeal()` method and `formatBAPCSEmbed()` formatter
- HardwareSwap uses its own notification logic in `internal/hardwareswap/builder.go`

### `internal/storage/`

- New Firestore operations for `posts`, `alerts`, `servers`, `bapcs_deals` collections

### Environment & Secrets

- Add to `.env`: `REDDIT_SERVICE_URL`, `REDDIT_SERVICE_SECRET`
- Update GCP Cloud Run env vars via GCP MCP
- Create Cloud Scheduler jobs: `/process-hardwareswap` (every 60s), `/process-bapcsales` (every 2-3 min)
- Set up Firestore indexes if needed via GCP MCP

### Graceful Degradation

- If Reddit relay service is unreachable (tunnel down), both processors log a warning and skip the run
- Same pattern as eBay gracefully disabling when credentials are missing
- No crashes, no retry floods

---

## 6. Non-Goals

- No Reddit OAuth — using unofficial `.json` API only
- No headed browser for Reddit — the `.json` endpoint provides all needed data (score, num_comments, flair, etc.)
- No fancy bapcsalescanada features yet (per-user alerts, retailer extraction, price tracking) — strongly commented as future work
- No changes to existing processors (RFD, eBay, Facebook, MemoryExpress, BestBuy)
