# Rust runtime architecture

The production executable is Rust 1.95, compiled with thin LTO and native bundled
SQLite. Optimized Go is a comparison/reference fixture generator only. RFD and
Crux build independently, with their own lockfile, data volume and Discord identity.

A current-thread Tokio executor runs pooled bounded HTTP, a small outbound Discord
Gateway client without guild/member/message caches, command callbacks, health
routes and completion-relative scheduled polls. A dedicated SQLite worker owns
one connection and a bounded queue. Database waits never block heartbeats.
Source and detail/quote concurrency, response sizes and callback concurrency are
bounded. Shutdown cancels source work cooperatively, drains callbacks and retains
acknowledged delivery writes before closing the database worker.

RFD keeps deterministic thread/product identities, conservative fuzzy grouping,
discount-backed engagement filters, per-channel receipt/author ownership and
stable retry nonces. It saves each successful send before trying another channel.
Optional Gemini title cleanup preserves Pacific-time quota reset/cooldown state;
Compose disables those calls in the free profile. Ordinary alerts work without AI.

Crux separates full/recent/sitemap checks from durable outbox delivery. Partial
or invalid pages do not replace the full baseline. Changes require confirmation;
identity collisions remain quarantined. A snapshot and its outbox enqueue commit
atomically. Delivery uses owned leases, stable nonces, retry backoff and durable
outage-alert suppression across restarts.

Stores retain the existing Go schemas and JSON field casing, timestamp identities,
application binding, receipts and pending deliveries. RFD's alias triggers are
upgraded transactionally to support repeated aliases in a grouped payload. A
foreign schema is rejected before journal/schema mutation. SQLite uses local disk,
WAL/FULL, bounded cache/checkpoint settings, startup/daily retention and online
backup snapshots that include committed WAL pages and refuse destination overwrite.

The static scratch image runs as UID 65532 with a writable data volume and no
shell or build tools. The same image, Compose/environment and SQLite snapshot move
between VMs. See [Oracle deployment](docs/oracle-deployment.md) and
[runtime measurements](docs/runtime-optimization.md). Retention limits bound rows;
record sizes and subscriptions still affect bytes. No provider promises unlimited
free capacity or uninterrupted hosting.

## Request-driven GCP profile

The optional `gcp` feature adds a Cloud Run HTTP entry point, fenced Firestore
checkpoints and separate command/worker roles. SQLite is in memory and restored
from verified compressed chunks. A checkpoint completes before a database call
returns; delivery receipts remain durable before the next send. The command
namespace owns subscriptions and polling only mirrors them. See
[Cloud Run design and migration](docs/gcp-deployment.md) for leases, resource
limits, retention and free-allowance safeguards. Native startup is unchanged.

## Category parsing

RFD duplicates its category metadata in desktop/mobile blocks. Detail parsing
takes the first nonempty category label rather than joining both blocks. Known
duplicated labels from older checkpoints are normalized during reconciliation
and embed rendering, preserving delivery receipts without extra detail fetches.
The responsive HTML regression fixture checks parsing through the Discord footer.
