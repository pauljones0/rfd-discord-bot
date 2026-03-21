# EPIC: Facebook Car Deal Bot Integration into RFD Discord Bot

## Context & Motivation

The `rfd-discord-bot` is a production Go 1.26 Cloud Run service that scrapes RedFlagDeals and eBay for deals, analyzes them with Gemini AI, and posts notifications to Discord channels. The `fb-car-deal-bot` is a standalone Go app that scrapes Facebook Marketplace for car deals, runs Carfax valuations via Playwright browser automation, and uses Gemini AI with Google Search grounding to assess deals. Both use Firestore, both use Gemini, both post to Discord.

**Goal**: Merge the Facebook car deal functionality into the RFD bot monorepo as a new `internal/facebook/` module, while reworking the subscription system to support multiple subscription types per channel, integrating ProxyScrape residential proxies, adding API usage metrics logging, and deploying via a separate GCloud Scheduler-triggered endpoint.

---

## Phase 1: Foundation — Code Integration & Module Setup

### Subphase 1.1: Port Facebook Module Code

**Agents**: Senior Dev (lead), SecOps (review)

**Senior Dev responsibilities**:
- Create `internal/facebook/` package directory structure:
  - `internal/facebook/processor.go` — Main orchestration
  - `internal/facebook/scraper.go` — Facebook Marketplace scraper
  - `internal/facebook/locations.go` — Canadian city IDs and postal codes
  - `internal/facebook/carfax.go` — Carfax valuation client
  - `internal/facebook/browser.go` — Playwright browser manager with stealth config
  - `internal/facebook/analysis.go` — Gemini prompts for car deal normalization and FOMO analysis
- Port Facebook-specific models into `internal/models/`:
  - `CarData`, `DealAnalysis`, `ScrapedAd`, `AdRecord`, `PriceHistory` structs
  - `IsCarfaxEligible()` method
  - Facebook subscription fields (`City`, `RadiusKm`, `FilterBrands[]`)
- Port Facebook-specific Firestore operations into `internal/storage/`:
  - `facebook_ads.go` — Ad deduplication, pruning
  - `facebook_subscriptions.go` — Facebook subscription CRUD
- Port test files
- Add `playwright-go` dependency to `go.mod`

**SecOps responsibilities**:
- Review ported code for hardcoded credentials, proxy password leaks
- Verify no test files contain real API keys or proxy credentials
- Ensure Carfax/Facebook scraping doesn't store PII

**Gate**: `go build ./...` compiles AND `go test ./internal/facebook/... ./internal/models/... ./internal/storage/...` passes (excluding Playwright tests)

---

### Subphase 1.2: Wire Facebook Processor to Server Entry Point

**Agents**: Senior Dev (lead)

**Senior Dev responsibilities**:
- Add `/process-facebook` HTTP endpoint to `cmd/server/main.go`
- Create `internal/facebook/processor.go` orchestration:
  1. Load active Facebook subscriptions from Firestore
  2. Group by city, merge brand filters and radius
  3. For each city: scrape → normalize → dedup → Carfax → analyze → notify
  4. Concurrency: semaphore of 1 (same pattern as eBay)
  5. Context timeout: 9 minutes
- Use existing `internal/notifier/discord.go` for posting deal embeds
- Add Facebook deal embed format: green color (0x00C853), fields for Asking Price, Carfax Value, city/location

**Gate**: `/process-facebook` endpoint responds HTTP 202. Existing `/process-deals` and `/process-ebay` still function.

---

## Phase 2: Subscription System Rework — Multi-Subscription Support

### Subphase 2.1: Extend Subscription Model & Storage

**Agents**: Senior Dev (lead), Designer (schema design)

**Senior Dev responsibilities**:
- Update `internal/models/subscription.go`:
  - Add `SubscriptionType` field (enum: `rfd`, `ebay`, `facebook`)
  - Add optional Facebook-specific fields: `City`, `RadiusKm`, `FilterBrands`
  - Add `IsFacebook()`, `IsRFD()`, `IsEbay()` helper methods
- Update `internal/storage/subscriptions.go`:
  - Doc ID format: `{guildID}_{channelID}_{subscriptionType}` (Facebook: `{guildID}_{channelID}_facebook_{city}`)
  - Migration: existing docs treated as `rfd` type (backwards compatible)
- Update processors to filter subscriptions by type

**Gate**: All existing tests pass. New storage tests pass. Single channel supports RFD + eBay + Facebook subscriptions simultaneously.

---

### Subphase 2.2: Rework Discord Slash Commands

**Agents**: Senior Dev (lead), Designer (UX)

**Senior Dev responsibilities**:
- Replace `/rfd-bot-setup` with unified `/deals` command:
  ```
  /deals setup <type> [options...]
    type: rfd | ebay | facebook
    For rfd: deal_filter
    For ebay: deal_filter
    For facebook: city (autocomplete), radius (default 500km), brands (optional)
  /deals remove <type> [city]
  /deals list
  ```
- Rewrite `cmd/register-commands/main.go`
- Update `internal/api/interactions.go` with new routing

**Gate**: `go run cmd/register-commands/main.go` succeeds. All subscription flows work via Discord.

---

## Phase 3: Proxy & Secrets Integration

### Subphase 3.1: ProxyScrape Environment & Secrets Setup

**Agents**: SecOps (lead), Senior Dev (support)

**Responsibilities**:
- Add proxy env vars to `internal/config/config.go`: `PROXY_URL`, `PROXY_HOST`, `PROXY_PORT`, `PROXY_USER`, `PROXY_PASS`
- Update `.env.example`
- Update `scripts/sync-secrets.ps1` with `PROXY_URL`
- Run secrets sync script
- Mask proxy credentials in all log output

**Gate**: Proxy config validated in tests. Credentials masked in logs.

---

### Subphase 3.2: GitHub Actions & Deployment Updates

**Agents**: Senior Dev (lead), SecOps (review)

**Responsibilities**:
- Update `.github/workflows/deploy.yml`: add PROXY_URL, Playwright, 2Gi memory
- Create `Dockerfile` with multi-stage build (Go + Playwright + Firefox)

**Gate**: GitHub Actions build succeeds. Cloud Run deploys with proxy env var.

---

## Phase 4: Infrastructure — GCloud Scheduler & Separation of Concerns

### Subphase 4.1: GCloud Scheduler for Facebook Processing

**Agents**: Senior Dev (lead), SecOps (review)

**Responsibilities**:
- Create Cloud Scheduler job: `facebook-car-deal-scraper`, every 2 minutes, HTTP POST to `/process-facebook`
- Ensure existing RFD and eBay schedulers unaffected

**Gate**: `gcloud scheduler jobs describe facebook-car-deal-scraper` returns valid config. Manual trigger works.

---

### Subphase 4.2: Enforce Separation of Concerns

**Agents**: Senior Dev (lead), Designer (architecture review)

**Responsibilities**:
- Verify no cross-imports between `internal/ebay/`, `internal/facebook/`, `internal/processor/`
- Each processor imports only shared packages: `ai`, `config`, `logger`, `models`, `notifier`, `storage`, `util`, `validator`

**Gate**: `go vet ./...` passes. `grep` confirms no cross-imports between processor modules.

---

## Phase 5: Observability — Logging, Metrics & API Usage Tracking

### Subphase 5.1: INFO-Level Logging for Observability

**Agents**: Senior Dev (lead)

**Responsibilities**:
- Audit all Facebook module code for appropriate log levels
- Add `"processor"` key to all log entries (`"facebook"`, `"rfd"`, `"ebay"`)

**Gate**: Structured JSON logs queryable in GCP Cloud Logging by `jsonPayload.processor`.

---

### Subphase 5.2: API Usage Metrics Logging

**Agents**: Senior Dev (lead), Designer (metrics schema)

**Responsibilities**:
- Create `internal/metrics/tracker.go` with atomic counters
- Track: eBay API calls (5k/day limit), Gemini calls (model, tokens, region), ProxyScrape bandwidth (5GB lifetime), Carfax valuations, Discord messages
- Emit `api_usage_summary` INFO log at end of each processor run

**Gate**: Each processor emits usage summary. ProxyScrape bandwidth tracked in MB.

---

## Phase 6: Testing & Validation

### Subphase 6.1: Port & Adapt All Tests

**Agents**: Senior Dev (lead)

**Responsibilities**:
- Port all fb-car-deal-bot tests to new locations
- Add Playwright skip logic for CI
- Verify existing RFD/eBay tests unchanged

**Gate**: `go test ./... -count=1` all green. Coverage ≥ 70% for new code.

---

### Subphase 6.2: End-to-End Validation

**Agents**: Senior Dev (lead), SecOps (security audit)

**Responsibilities**:
- Local E2E: register commands → setup subscription → trigger processing → verify Discord embeds
- Deploy to Cloud Run, verify scheduler, monitor logs

**Gate**: Full pipeline works. Existing RFD/eBay unaffected. No secrets in logs.

---

## Verification Checklist

1. **Functional**: `/deals setup facebook city:Toronto` → scheduler → deal embeds in Discord
2. **Multi-sub**: Same channel has RFD + eBay + Facebook subscriptions active
3. **Isolation**: Disable Facebook scheduler → RFD and eBay continue
4. **Proxy**: Facebook scrape uses residential proxy IP
5. **Metrics**: `jsonPayload.message = "api_usage_summary"` returns entries for all 3 processors
6. **Security**: No proxy credentials in committed code
7. **Tests**: `go test ./... -count=1` all green
8. **Separation**: No cross-imports between processor modules
