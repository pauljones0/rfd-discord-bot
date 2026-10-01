# GCP live migration — 2026-09-30 (local), 2026-10-01 UTC

Both independent Rust bots run in project `may2025-01`, Iowa (`us-central1`).
Latest registry cleanup reclaimed stale layers manually; provider-reported
storage was 11.089 MB after cleanup and is 16.806 MB after the category fix
deployment. See the reclamation and category fix records below.
The native VM/Gateway profile remains available. See [deployment and recovery](gcp-deployment.md).

## Live services

| Bot | Private worker | Public signed command endpoint | Schedule (UTC) |
| --- | --- | --- | --- |
| RFD | `rfd-bot` | `https://rfd-commands-e2sxnnqcvq-uc.a.run.app/discord/interactions` | `rfd-bot-tick`, every 3 minutes |
| Crux | `crux-bot` | `https://crux-commands-e2sxnnqcvq-uc.a.run.app/discord/interactions` | `crux-bot-tick`, every minute |

Crux persists due times for recent/15-minute, sitemap/hourly and full/6-hour
checks; one scheduler also handles delivery retries. Workers require IAM/OIDC
and a separate random scheduler header. Commands require Discord Ed25519
signatures and fresh timestamps. Discord validated both production endpoint
PINGs. Registration retained `/rfd` and `/deals` in their existing scopes and
removed retired Crux application subcommands.

All four services use request billing, 0.08 vCPU, 256 MiB, first-generation
execution, concurrency one, zero minimum instances, maximum one instance,
30-minute request timeout and disabled startup CPU boost. Runtime identities
use metadata credentials, with no service-account key files. Images contain
one static musl Rust executable, without compiler/browser/Go/cloud SDK.

Immutable registry references under
`us-central1-docker.pkg.dev/may2025-01/cloud-run-source-deploy`:

- RFD: `rfd-rust@sha256:f1a8b2e396e3b488c6da0d877176125462517d017f1ce2bc45807a63ab37abe4`
- Crux: `crux-rust@sha256:6ee8fa0697c06faab2ebc7c01a9058199061588d2eadfa9c7b8a1067e7585697`

## State and previous deployment

Both old containers (`rfd-standalone`, `rfd-discord-bot`) stopped cleanly with
exit zero. The watchdog timer and service were stopped, and its timer disabled.
Do not start them while either cloud producer remains active.

SQLite backup API snapshots included committed WAL data. The Crux-only export
preserved 1,358 companies, 160 changes, one configuration, one subscription and
all 13 outbox records. RFD preserved 1,410 deals, three settings (including its
application binding) and one subscription. Known Go JSON BLOB columns were
strictly decoded as UTF-8 without dropping payloads, receipts or history.
Original stopped snapshots were retained unchanged.

Worker namespaces are `rfd-production` and `crux-production`. Independent
command stores are `rfd-subscriptions-production` and
`crux-subscriptions-production`; pollers mirror those subscriptions without
writing back over them. Imports used atomic `exists:false` preconditions.
Initial logical checkpoints were 4,726,168 raw / 1,125,735 compressed bytes for
RFD (three chunks), and 1,084,146 raw bytes for Crux (one compressed chunk).
All namespace payload/chunk indexes are disabled only on the two bot fields.

Private consistent snapshots, application/command rollback metadata and
production configuration are kept outside both repositories. Original database
snapshots and a full archive of the six previous registry images are also in
`gs://may2025-01-bot-legacy-backups/20260930/`. The image archive SHA-256 is
`43a0538c09977946a54c6007412b5f0f8e36a9c44056b7680a66a51857a66437`.
Obsolete registry digests were deleted only after archive verification. The
retained Rust manifests total 32,865,821 image bytes including the final budget revision (including shared layers).
The repository initially held about 2.3 GB of old images. Its reported size can
remain above the free allowance until Google's daily unreferenced-layer cleanup;
existing charges and that transition are not undone by deleting tags/digests.
See [registry deletion behavior](https://docs.cloud.google.com/artifact-registry/docs/docker/manage-images). The
backup bucket is private Iowa Standard with public access prevention, uniform
IAM, no object versioning and zero soft-delete retention. Total preserved
storage is below the 5 GB ongoing free allowance. Keep backups private and
rotate additional snapshots.

## Verification

Crux's first scheduled production request completed at 02:19:14 UTC: all 83
pages and 2,488 cards were validated, a snapshot committed, no changes required
notifications, and the request succeeded in 1,093.799 seconds (about 18 minutes).
Unresolved/incomplete source ticker warnings were retained as warnings, rather
than guessed into identities. The authoritative lease was released normally.

A final cloud-only fix persists Crux source-outage counters, suppression and
recovery state across fresh workers. Its restart fixture verifies one alert
after two failures, no duplicate third failure and exactly one recovery. Both
full native/GCP suites passed after the fix; the native Docker volume check
also passed. The scheduler was briefly paused only after the first scan released
its lease, the new image deployed, then the schedule resumed. The previous good
Crux digest remains available for rollback. Its next normal scheduled request,
on a fresh worker revision, completed at 02:21:06 UTC in 5.449 seconds with
HTTP 200. The consistent full-scan backup retained all 1,358 company identities
and all 13 historical outbox records, with no pending deliveries and SQLite
integrity `ok`. Maintenance removed two expired change records (160 to 158).

The measured full crawl also informed the runtime allowance. Conservative
11-second overhead reservations on every one-minute tick plus 18-minute full
scans made Crux's initial 650,000-second/33-day limit too tight. Crux now has
700,000 seconds, while RFD remains 650,000 and each command service 20,000.
The combined nominal maximum is 111,200 vCPU-seconds and 347,500 GiB-seconds,
within the ongoing request-billed free quotas. A policy test projects all 33 days of one-minute overhead, four 1,100-second
full scans/day and 96 six-second recent checks/day, even counting skipped ticks;
it fits the adjusted cap and separately verifies the combined CPU/memory bound.
The guard can still pause work
under unusually heavy scans/outages; it is not an uptime or billing guarantee.

Native and GCP feature format, clippy (`-D warnings`) and full Rust test suites
passed in both repositories. Fixture tests cover ambiguous commits, fenced
leases, checkpoint corruption, atomic restore, operation/runtime budgets,
subscription changes during a scan, acknowledged receipt recovery and Go BLOB
migration. Python import/export tests cover atomic no-clobber publication,
consistent transaction reads, integrity/hash/identity checks and private modes.

Real Cloud Run fixture deployments used separate namespaces, dummy identities
and offline signing keys. Signed subscribe/list operations preceded fixture
polls; each bot's counter continued from 3 to 4 after a fresh revision, with
subscriptions and receipts/outbox completion preserved. Signed list responses
on fresh revisions took 1.784 seconds (RFD) and 0.709 seconds (Crux). These were
fresh-revision restores, not a guaranteed idle-scale-to-zero cold-start bound.
No real Discord test posts or fake production subscriptions were sent.

Both final native Docker images also passed static-ELF checks and non-root,
read-only, network-disabled storage initialization on fresh Docker data volumes.

RFD's first production schedule completed at 02:06:28 UTC: 39 deals observed,
one normal alert sent, no edits, HTTP 200, 27.550 seconds of request processing.
Its consistent post-poll export retained all 513 original Discord receipts and
contained 514 after that alert. The Scheduler acknowledgement succeeded. Later
polls at 02:09, 02:12 and 02:15 took 9.926, 11.000 and 9.139 seconds without
duplicate notifications. Worker IAM denial and command-role production health
were verified; retained global command collections contain only the current
`/rfd` and `/deals` subcommands.

Migration found and fixed an inherited scraper browser user-agent on Discord
REST requests. The same authorized read returned 403 with the browser header
and 200 with Discord's required bot header. Final production images contain the
fix and fixture assertions. Production scraper operations are observed through
normal schedules, rather than deliberately invoking a test alert.

## Cost and retention

Persistent application budgets reserve request time before processing and cap
Firestore operations conservatively across four namespaces. Limits and exact
calculations are in [the runbook](gcp-deployment.md). Ordinary maintenance handles
retention: no paid Firestore TTL, PITR or managed backup schedules are enabled.
Company identity, subscriptions and pending deliveries never expire silently.
AI and paid browser backends were disabled at the initial cutover. RFD optional
free Gemini was enabled later as recorded below; Crux has no AI. The old Cloud Build trigger remains
disabled; the deployment scripts build nothing remotely.

A project-scoped first-cent net-spend alert was created in the account's CAD
currency, including credits. It is a notification, not a billing hard cap.
Other billing-account usage, public invalid requests, unrecorded failed requests,
outbound traffic and provider terms remain outside the application's limits.
The setup targets ongoing free allowances; permanent zero cost and uninterrupted
hosting cannot be guaranteed.

## Final deployment verification

Final Crux budget revision `crux-bot-00005-j5z` completed its first normal
scheduled request at 02:28:02 UTC in 2.051 seconds, HTTP 200. Both scheduler
jobs are enabled and acknowledged successful requests. All four final services
were checked for immutable image identity, production role/mode, request billing,
0.08 CPU / 256 MiB, concurrency one, 30-minute timeout, maximum one instance,
no startup boost, and 100% traffic on the current revision. Both command services
report production health.

Final revisions: `rfd-bot-00003-s44`, `rfd-commands-00003-9fg`,
`crux-bot-00005-j5z`, `crux-commands-00005-lpr`. The private backup bucket
contains 2,848,377,391 bytes after the four operational SQLite exports, below
its free storage allowance. Current cloud-state exports are under
`gs://may2025-01-bot-legacy-backups/20261001/state/`; original migration
snapshots remain under `20260930/state/`.


## Account usage audit — 2026-10-01 UTC

At the time of this audit, the bots targeted ongoing free allowances, but the
existing project was **not free-tier-only**. This audit inspected the active billing account's only linked
project, `may2025-01`, the other visible unbilled project, Cloud Asset inventory,
all 34 Scheduler regions, service settings, retained objects, BigQuery job
metadata and Cloud Monitoring usage. No unrelated cloud workload was changed.

Confirmed exceptions:

- The unrelated stopped `smcsd-clean` GPU VM retains a 100 GiB `pd-ssd`
  disk in `us-central1-a`. Stopping compute does not remove disk charges.
  SSD storage has no applicable ongoing free allowance; its published USD
  list price is about $17 per typical month. Preserve needed data off GCP
  before removing this disk; a new GCP snapshot would also be billable.
- September BigQuery job metadata totals **4,523,907,088,384 bytes
  (4.11447 TiB)** of billed query scans, mainly the linked WeatherNext table.
  The 1,172 jobs contain 264 cached queries and 908 uncached SELECT jobs,
  with no script parent/child duplication or reservation use. The 1 TiB
  monthly free allowance is exceeded. At $6.25/TiB, the estimated query
  overage is $19.47 USD before credits/tax; the account bills in CAD.
- Gemini has successful GenerateContent requests and positive
  `GenerateContentPaidTierInputTokensPerModelPerMinute` usage for
  `gemini-2.5-flash-lite`, `gemini-2.5-flash` and
  `gemini-3.5-flash-lite`. Free-tier quota metrics have no samples.
  These are existing workloads outside the bots; AI is disabled in the bots.
  Quota per-project/per-user series duplicate token counts and must not be
  summed together. Vertex API activity checked here consists of management
  list requests, not inference.
- Artifact Registry still reports **2,364,983,412 bytes (2.20 GiB)**,
  above its 0.5 GiB free allowance, despite retained Rust images totalling
  only 32,865,821 image bytes. Deleted image layers are reclaimed daily.
  Confirm the provider-reported size after reclamation before marking
  registry storage within the allowance; past usage is not undone.

Bot and supporting usage observed around 03:05 UTC:

| Resource | Observed usage | Applicable free allowance |
| --- | --- | --- |
| Cloud Run | 1,398.28 billable instance-seconds, 123 requests; current profiles are 0.08 vCPU / 256 MiB, request billing, minimum zero and maximum one instance | 180,000 vCPU-seconds, 360,000 GiB-seconds and 2 million requests/month |
| Cloud Run internet outbound | 1,000,260 bytes; Google-bound traffic measured separately | 1 GB/month from North America |
| Firestore | 226 reads, 256 writes, no deletes recorded; 46,539,432 bytes data/index storage | 50,000 reads, 20,000 writes, 20,000 deletes/day; 1 GiB storage |
| Scheduler | Two jobs total across all regions | Three jobs/billing account |
| Cloud Storage | 2,930,460,381 live/noncurrent bytes across two Iowa Standard buckets; source bucket has no soft-deleted objects and backup bucket has soft delete disabled | 5 GB regional storage; observed operation counts also below free allowances |
| Logging | About 16.1 MB month-to-date ingestion; no paid extra-retention samples | 50 GiB ingestion/month |
| Cloud Build | One approximately 4.9-minute E2_STANDARD_2 build this month | 2,500 build minutes/month |

Firestore PITR, managed backups and paid TTL are absent. Registry scanning is
disabled. No reserved external IPs, snapshots, forwarding rules, BigQuery
reservations/commitments or Discovery Engine data stores/engines were found.
The new backup bucket is too recent for daily storage metrics, so its object
inventory was used rather than treating missing metrics as zero usage.

This is a resource and usage audit, not a verified final invoice: there is no
billing-export dataset, and the billing console is unavailable through this
session's browser tools. Credits, taxes, currency conversion and billing
latency can change the payable amount. Application guards and first-cent
alerts do not enforce an account-wide zero-dollar billing cap. A full day of
production measurements is still needed to assess the normal daily rate.

Pricing references:
[ongoing free allowances](https://docs.cloud.google.com/free/docs/free-cloud-features),
[persistent disks](https://cloud.google.com/compute/disks-image-pricing),
[BigQuery](https://cloud.google.com/bigquery/pricing),
[Gemini](https://ai.google.dev/gemini-api/docs/billing),
[registry storage](https://cloud.google.com/artifact-registry/pricing) and
[daily layer cleanup](https://docs.cloud.google.com/artifact-registry/docs/docker/manage-images).

## Cost cleanup and RFD free Gemini — 2026-10-01 UTC

The account audit above records the state before the authorized cleanup. The
100 GiB `smcsd-clean` SSD was detached and deleted; Compute Engine now lists no
disks. Its VM remains stopped with no attached disks. Existing charges are not
reversed.

BigQuery had no pending/running jobs. The BigQuery API was disabled at the user's
request, including its BigQuery Storage and `cloudapis.googleapis.com`
dependents. Cloud APIs is a convenience umbrella; disabling it did not disable
Cloud Run, Firestore, Scheduler, Artifact Registry or Storage. Do not re-enable
it casually: enabling it can restore default services, including BigQuery.
Both bots continued to acknowledge normal scheduled requests after shutdown.
BigQuery and its Storage/umbrella dependents were also disabled in the new
unbilled Gemini project, where Google had enabled them by default.

The Gemini Developer API is disabled in both old projects (`may2025-01` and
`gen-lang-client-0762488406`), and Vertex AI is disabled in `may2025-01`.
Gemini Cloud Assist and Gemini for Google Cloud APIs were also disabled in
`may2025-01`, covering the console/Code Assist path as well as inference. See
[Google's shutdown instructions](https://docs.cloud.google.com/cloud-assist/turn-off).
The dedicated project `rfd-gemini-free-20261001` (number `345872540963`) has
**no linked billing account**. Its only API key is restricted to Gemini
GenerateContent and supplied only to the RFD worker; Crux and both command
services have no Gemini key. The RFD runtime identity has a custom role with
only key lookup, project lookup and service-usage permissions there.

Deployment and runtime verify key ownership and that the Gemini project has no
billing account. Runtime checks use metadata credentials immediately before AI
work; failed verification retains original titles. The cloud profile allows
only `gemini-3.5-flash-lite`, reserves at most 20 attempts per Pacific day before
HTTP calls, waits at least 60 seconds between attempts, bounds prompts at 16 KiB
and output at 2,048 tokens, and persists quota/cooldown state in its fenced
checkpoint. Authentication/quota failures trigger cooldown, without paid model
or key fallback. Failed persistence prevents provider calls. The bot continues
ordinary delivery when AI is unavailable.

A small synthetic request in the unbilled project succeeded with HTTP 200.
Provider free quotas were 15 requests/minute and 500/day. Fixture tests passed
for invalid keys, quota failure, failed reservation, restart/cooldown and
ordinary delivery with preserved receipts. The new worker completed scheduled
polls at 03:30 and 03:33 with HTTP 200, no duplicate sends and no AI verification
warnings. No bot-side Gemini request was observed during these polls; this
verification does not establish a live title-cleanup result. A consistent export
preserved all 514 RFD delivery receipts. Crux also completed post-shutdown
scheduled requests with HTTP 200.

The RFD image is now
`rfd-rust@sha256:b4a26321816e0a0f11ea26e0499c88dfa0f31e5995cfb8de4888f44721f66e42`.
Current revisions are `rfd-bot-00004-lj7`, `rfd-commands-00004-p98`,
`crux-bot-00005-j5z` and `crux-commands-00005-lpr`, each receiving 100% traffic.
All four retain their 0.08 vCPU / 256 MiB, request-billed, minimum-zero profiles.

Registry cleanup removed all superseded builds. Six manifests remain: the two
current OCI indexes and their runtime/attestation children, with 11,084,870
reported child-image bytes (including shared layers). Production tags protect
both deployed roles. An active daily policy deletes other images older than
one day and retains production-tagged roots and their referenced children.
Google still reported about 2.371 GB of repository storage immediately after
deletion; its daily unreferenced-layer reclamation must complete before the
reported size is within the 0.5 GiB allowance. This remains a follow-up check,
not a claim that registry billing is already zero. See
[Google's image deletion behavior](https://docs.cloud.google.com/artifact-registry/docs/docker/manage-images).

Native/GCP Rust format, clippy and full test suites passed for the changed RFD
processing. Crux GCP checks, both repositories' Python deployment tests and
static/non-root Docker fixture verification passed. No real Discord test posts
were sent. Runtime and Firestore budgets remain application safeguards; public
invalid traffic and billing-account costs remain outside those guards. The
first-cent budget alert remains a notification, not a spending hard cap.

### Manual registry recheck — 2026-10-01 UTC

A full repository/image/file/attachment inventory confirmed one Docker
repository, exactly the six deployed OCI manifests and two current attestations.
The production manifest graph references seven config/layer files plus six
manifest files, totalling 11,089,210 bytes. The remaining 51 files
(2,359,599,176 bytes) are unreferenced leftovers
from already-deleted images; there are no additional old image versions to
remove. Redundant dated build tags and the generic Crux `production` tag were
manually deleted; only `production-rfd-bot`, `production-rfd-commands`,
`production-crux-bot` and `production-crux-commands` remain. The cleanup keep
policy continues to match these tags. All service digests remain unchanged.

Google's [file-delete API](https://docs.cloud.google.com/artifact-registry/docs/reference/rest/v1/projects.locations.repositories.files/delete)
permits deletion only in generic repositories, not this Docker repository.
The initial investigation checked the generic file API and automatic daily
cleanup. Further investigation found a separate Docker data-plane deletion
method; the manual reclamation below supersedes the waiting recommendation.

### Direct Docker layer reclamation — 2026-10-01 UTC

Google also documents `Docker-DeleteBlob` in its
[Artifact Registry data-plane API](https://docs.cloud.google.com/artifact-registry/docs/audit-logging#docker-deleteblob).
A controlled DELETE of one verified unused 159-byte blob returned HTTP 202, and
its subsequent HEAD returned 404. The same endpoint removed the remaining
unreferenced blobs. Six encoded manifest-file paths were distinguished from
blob digests; their blob-path requests returned 404 and did not remove any
manifest. Final inventory has exactly 13 files: the six required OCI manifests
plus seven referenced config/layer files. Both image graphs and their referenced
blobs remain accessible, with no leftover unused files. Total storage is now
**11,089,210 bytes**; Google's repository describe reports **11.089 MB**,
below the 0.5 GiB free allowance. The 2,359,599,176 bytes of stale layers have
been reclaimed manually. Deployment digests and service configuration were
unchanged. Repository deletion was unnecessary.

Cloud Run additionally imports and retains deployed images for serving revisions,
so existing instances do not pull from the registry at startup. See
[Cloud Run deployment behavior](https://docs.cloud.google.com/run/docs/deploying).
The small current registry images remain available for future deployments.

### Ongoing-cost recheck — 2026-10-01 UTC

After layer reclamation, the repository reports 11,089,210 bytes. Fresh checks
still show no Compute Engine disks, the diskless `smcsd-clean` VM terminated,
BigQuery/paid Gemini APIs disabled, and the RFD Gemini project unbilled.
Cloud Asset resource details confirm both retained BigQuery datasets are
`LINKED`, and both visible tables belong to those linked datasets. They do not
represent subscriber-owned stored copies; sharing does not impose storage costs
on the subscriber. See
[BigQuery linked datasets](https://docs.cloud.google.com/bigquery/docs/analytics-hub-view-subscribe-listings).
BigQuery was not re-enabled to perform this read-only verification.

Expected ongoing cost under normal bot usage is zero with the current resources
and controls. This is not confirmation that the next invoice will be zero:
previous SSD, query, AI and registry usage can still be billed. Free allowances
and application budgets are not an account-wide hard spending cap. Billing
reports after usage has settled are the final source for actual charges.

### Category fix and repository publication — 2026-10-01 UTC

RFD detail pages render the same `.thread_category` label in both desktop and
mobile markup. Joining all matching nodes produced values such as
`Home & GardenHome & Garden`, which missed the emoji mapping and used the
unknown-category fallback. Parsing now uses the first nonempty category node.
Category normalization also repairs known repeated labels when rendering and
reconciling existing stored deals, without guessing unknown categories.
Regression fixtures cover responsive markup, empty nodes, metadata fallback,
the resulting Discord footer, and preservation of delivery receipts.

The fix is published in the standalone
[RFD repository](https://github.com/pauljones0/rfd-discord-bot).
Crux has a separate private
[Crux repository](https://github.com/pauljones0/crux-discord-bot), with clean
history, its own build/deployment files, and no local credentials or databases.
Both repositories passed native/GCP Rust checks, Python tests, Docker static
and non-root checks, Go-reference race/vet checks, and staged secret scans.

Both RFD services now use the immutable RFD digest listed above:
`rfd-bot-00005-nn4` and `rfd-commands-00005-mdl`. The first normal scheduled
request on the worker completed at 04:09:23 UTC with HTTP 200. It observed and
reconciled 39 deals, repaired 39 stored category values, and sent no new
notifications. A consistent checkpoint comparison retained all 514 preexisting
delivery receipts. Existing Discord messages outside the normal update window
were not rewritten. The command health endpoint also returned HTTP 200.
Registry storage after deployment is 16,805,780 bytes, still below the free
allowance; runtime and resource limits are unchanged.

### Local Docker retirement — 2026-10-01 UTC

At the user's request, all 37 containers on the local Unix-socket Docker engine
were stopped and removed. Verification at 04:12 UTC showed zero remaining
containers. Images and volumes were preserved. This includes the old RFD and
combined bot containers; the GCP bots remain the active producers. The earlier
disabled local bot watchdog remains disabled.
