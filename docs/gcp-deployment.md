# GCP Cloud Run deployment

This profile replaces persistent Gateway connections with signed HTTPS Discord
interactions and scheduled HTTP polls. The native VM profile still uses local
SQLite and the Gateway. Provider terms checked on 2026-09-30.

## Services and persistence

Each bot deploys the same Rust image twice: a `worker` service for `/tick`, and a
`commands` service for `/discord/interactions`. Both scale to zero, use request
billing, first-generation execution, 0.08 vCPU, 256 MiB, concurrency 1, and a
maximum of one instance at both service and revision level. Startup CPU boost
is disabled. No always-on Gateway, VM, public IPv4 allocation, NAT connector,
load balancer, browser or background scheduler runs in the containers. RFD can
optionally call Gemini through a separate unbilled project, verified at runtime.
Separate command services prevent a long scan from holding up Discord's
three-second response deadline. Cold starts and simultaneous commands can still
fail that deadline; command writes are idempotent and users can retry.

The existing free eligible Native `(default)` Firestore database and image
registry must both be in `us-central1`. The runtime service accounts receive
`roles/datastore.user` and fetch short-lived credentials from Cloud Run metadata;
no service-account keys are deployed. Only command services allow public HTTP access, as required by Discord.
Workers require Cloud Run IAM authentication: Scheduler uses an OIDC token for
the worker service account, granted `roles/run.invoker` only on its own service.
Command signatures bind the body and a fresh timestamp. Discord REST uses the
required `DiscordBot (URL, VERSION)` user-agent; scraper browser headers are
not forwarded to Discord. Worker requests additionally require
a random scheduler token, at least 32 characters long, in the
`X-Bot-Scheduler-Token` header. Neither token is written to source or logs.

SQLite `:memory:` is a disposable working copy. Logical rows are compressed and
stored under `bot_checkpoints/{namespace}/bot_checkpoint_chunks/{number}`.
A manifest records bot/application identity, SHA-256 chunk hashes, operation
budgets and a lease. Each checkpoint commits changed chunks, deletes obsolete
chunks, and updates the manifest atomically with an update-time precondition.
A failed/ambiguous checkpoint poisons the local working copy until authoritative
state is reloaded. Receipts are persisted before another notification is sent.
An exclusive worker lease lasts 35 minutes; work is bounded at 28 minutes and
Cloud Run/Scheduler requests at 30 minutes. After a crash, later invocations can
resume after lease expiry. This reduces duplicate notifications but does not
provide exactly-once delivery if Discord acknowledges and the process dies
before the receipt commits.

Each command service uses its own small subscription namespace. Pollers load
that checkpoint at the start of a tick, verify chunk hashes and check the
manifest again to prevent mixed snapshots. Unchanged subscriptions require one
manifest read. Polling cannot overwrite the authoritative command subscriptions.
Changes made during a scan take effect on its next tick.

Limits are 32 MiB of uncompressed logical data and twelve 512 KiB compressed
chunks per checkpoint. Exceeding a limit pauses processing instead of silently
dropping pending notifications. Disable indexes only on `payload` in the
`bot_checkpoints` collection group and `data` in `bot_checkpoint_chunks`.
Neither payload is queried. Existing unrelated collection indexes are untouched.

## Free allowance and safeguards

| Resource | Ongoing free allowance | Configuration |
| --- | --- | --- |
| Cloud Run request billing | 180,000 vCPU-seconds, 360,000 GiB-seconds and 2 million requests/month, across the billing account | Worker budgets 650,000 elapsed seconds for RFD and 700,000 for Crux; commands 20,000 seconds each, over conservative 33 calendar-day buckets. Combined nominal bound: 111,200 vCPU-seconds / 347,500 GiB-seconds. |
| Cloud Scheduler | 3 jobs per billing account | RFD every 3 minutes; Crux every minute (due-state chooses recent/15m, sitemap/1h, full/6h, delivery retry). Two jobs total. |
| Firestore | 50,000 reads, 20,000 writes and 20,000 deletes/day; 1 GiB stored | Each namespace caps acknowledged reads at 9,000 and writes/deletes together at 4,500 over conservative rolling 27 hour buckets. Four namespaces maximum. Mirror reads count toward the worker budget. |
| Artifact Registry | 0.5 GiB across billing account | Two static Rust images; rotate obsolete digests and avoid container scanning. |
| Regional Standard Cloud Storage | 5 GB-months in eligible US regions | Private Iowa backups; rotate snapshots; no paid soft-delete versions. |
| Cloud Run outbound traffic | 1 GiB/month within North America | Incoming scraper data is not outbound; small requests and Discord alerts still consume outbound allowance. |

The runtime reserves a whole 30-minute request before starting and refunds
unused time only on successful lease release, with an 11-second startup/response
allowance. Crashes keep the full reservation. Firestore usage and runtime budgets
survive restarts. On reaching a budget, processing stops until older usage ages
out. This is a conservative application guard, not a provider billing hard cap:
invalid public requests, cold-start/shutdown billing, failures before usage is
recorded, other account workloads, network traffic and provider changes remain
outside it. Budget notifications also are alerts, not spending limits.

RFD retains at most 2,000 deals and receipts by default; Crux keeps 90 days /
10,000 rows of change and completed-delivery history. Pending Crux deliveries
are protected, capped at 10,000, and new snapshots roll back at capacity. Company
identities and subscriptions have no TTL. These deletes run as ordinary bot
maintenance; paid Firestore TTL, PITR, scheduled managed backups and restore
services are not enabled. Conditional HTTP caches reset on cold starts, but the
Crux sitemap fingerprint, schedule attempt times and source-outage alert counters
persist. Alert suppression and recovery also survive cloud restarts; the native
profile keeps its existing process-local source counters. Failed source
checks wait for their normal interval, while outbox retries remain independent.

Sources: [GCP ongoing free resources](https://docs.cloud.google.com/free/docs/free-cloud-features),
[Cloud Run pricing](https://cloud.google.com/run/pricing),
[fractional CPU constraints](https://docs.cloud.google.com/run/docs/configuring/services/cpu),
[Scheduler pricing](https://cloud.google.com/scheduler/pricing),
[Firestore paid features](https://firebase.google.com/docs/firestore/pricing),
[Artifact Registry pricing](https://cloud.google.com/artifact-registry/pricing).

## Build and deploy

Build locally so a compiler never runs in a bot container. Both repositories
are independent; run these commands from the relevant checkout:

```sh
docker build --target cloud-run -t BOT-cloud:release .
docker tag BOT-cloud:release us-central1-docker.pkg.dev/PROJECT/REGISTRY/BOT:release
docker push us-central1-docker.pkg.dev/PROJECT/REGISTRY/BOT:release
```

Keep deployment JSON outside the repository with mode 0600. Required values:
`GCP_PROJECT`, `BOT_DEPLOYMENT_MODE`, `BOT_REQUEST_ROLE`, `FIRESTORE_NAMESPACE`,
`DISCORD_APP_ID`, `DISCORD_PUBLIC_KEY`, `SCHEDULER_TOKEN`. Production also requires
`DISCORD_BOT_TOKEN`. Workers require `SUBSCRIPTIONS_NAMESPACE`, distinct from
`FIRESTORE_NAMESPACE`; commands use that subscription namespace as their own
`FIRESTORE_NAMESPACE`. Namespace suffix must match `-fixture` or `-production`.
Preserve existing polling/affiliate settings; clear Gateway/local scheduler
settings. Gemini is blank by default and is always blank on command services.
No credentials are passed as CLI literals.

Optional cloud title cleanup requires exactly one dedicated key, model
`gemini-3.5-flash-lite`, `GEMINI_FREE_PROJECT` (an `rfd-gemini-free-*` project)
and its numeric `GEMINI_FREE_PROJECT_NUMBER`. Never link that project to billing.
Restrict its API key to the Gemini GenerateContent method, keep it only in the
private RFD worker environment, and grant only that worker service account
`apikeys.keys.lookup`, `resourcemanager.projects.get` and
`serviceusage.services.use` on the unbilled project. No new service-account key
is needed. The deploy script checks project billing and key ownership.

After checkpoint restoration, the worker verifies the key's parent project and
the absence of any billing account immediately before optional cleanup. It
skips verification when no titles need cleanup or the saved AI budget/cooldown
blocks requests. Verification failures, provider/key rejection and quota errors
leave ordinary deal processing enabled. Requests are capped at 20 per Pacific
day, spaced at least 60 seconds apart, reserved in the durable checkpoint before
each provider call, with at most 16 KiB prompt and 2,048 output tokens. Cooldown
expiry does not reset daily usage. The provider's free quota can still reject a
request earlier; RFD keeps the original titles. These limits do not enable paid
models, alternate keys, grounding, batch jobs or a paid fallback.

Successful deployments tag their immutable images `production-SERVICE`.
Registry cleanup should preserve tags with the `production` prefix and delete
other images older than one day. Referenced child manifests are retained by
Artifact Registry. Remove obsolete production tags after verifying all live
service digests; never delete a manifest required by a deployed image.

```sh
python3 deploy/gcp/deploy.py --project PROJECT --service BOT-worker   --image us-central1-docker.pkg.dev/PROJECT/REGISTRY/BOT@sha256:DIGEST   --config /private/BOT-worker.json
python3 deploy/gcp/deploy.py --project PROJECT --service BOT-commands   --image us-central1-docker.pkg.dev/PROJECT/REGISTRY/BOT@sha256:DIGEST   --config /private/BOT-commands.json
```

Deployment does not register commands, change Discord's endpoint or create a
scheduler. First use fixture mode with an offline Ed25519 test key, dummy
application `1001`, and separate namespaces. Signed subscription commands must
precede worker ticks to seed the command store. Fixture ticks exercise retention,
receipts/outbox completion and counters without contacting Discord or live
scraper sources. Deploy a new revision and check counters/subscriptions continue.
Also verify invalid signatures/admin tokens, permission denial and body limits.
For private worker requests, obtain a short-lived gcloud identity token and send
it as `Authorization: Bearer …`; never put token values in logs or CLI literals.
`/health` reports process readiness and mode/role; it does not prove Firestore or
upstream scraper availability. Review request logs and maintenance results.

## Production migration

Only one producer may run for each Discord identity. Preserve old images,
credentials and application/command metadata privately for rollback. Prepare
and verify both cloud roles in fixture mode before touching the old services.
Stop both legacy watchdog timer and service, then stop the old producer
containers gracefully. Snapshot each SQLite store using its backup API, including
committed WAL state. Export only Crux state from the combined database using
`scripts/export_crux.py`; preserve all RFD rows/application binding/receipts.
Use consistent snapshots as import sources, never running database files.

```sh
python3 deploy/gcp/import_state.py --config /private/BOT-worker-production.json   --snapshot /private/BOT.sqlite
python3 deploy/gcp/import_state.py --config /private/BOT-commands-production.json   --snapshot /private/BOT.sqlite
```

The importer checks integrity, schema width, RFD application binding, raw and
compressed limits. Legacy Go JSON BLOB values in known JSON columns are decoded
strictly as UTF-8 without changing their bytes or dropping records. The native
SQLite startup also upgrades those values transactionally; invalid UTF-8 fails
before journal/schema changes. It uses an atomic `exists:false` manifest precondition and
refuses any populated namespace. Commands receive subscriptions only, while
workers retain processing history and receipts. Imported row counts are printed,
not document contents. Keep and compare those migration records.

Deploy both roles with the production JSON. Register only the retained bot's
commands using the existing registration CLI; preserve global/guild scopes.
Set the application's Interactions Endpoint URL to the command service's
`/discord/interactions`, using `PATCH /applications/@me` or the Developer Portal.
Discord validates a signed PING and rejects invalid endpoints. Then activate:

```sh
python3 deploy/gcp/schedule.py --project PROJECT --service BOT-worker   --config /private/BOT-worker-production.json --schedule '*/3 * * * *'
```

Use `'* * * * *'` for Crux. No extra scheduler job is needed for its internal
checks. The script refuses an existing job and checks the regional job count;
verify jobs elsewhere in the billing account before adding any more. Keep the
legacy containers stopped and their watchdog disabled after cutover.

For rollback, pause the cloud jobs first and wait for active workers to finish.
Export current Firestore state and reconcile any deliveries after migration;
old SQLite snapshots lack those acknowledgements. Change Discord back to its
previous endpoint, then start exactly one old producer/watchdog. Never start
both copies or overwrite a populated cloud namespace.

Retain a few consistent private backups and immutable image digests, with totals
inside free allowances. Review Cloud Monitoring request durations, container
memory and startup latency, Firestore operations/storage, outbound traffic,
registry/GCS sizes and billing reports. A first-cent budget alert is useful but
cannot guarantee permanent zero cost or uninterrupted provider availability.

## Consistent operational export

Use the logged-in gcloud account to make a private, consistent SQLite snapshot:

```sh
python3 deploy/gcp/export_state.py --config /private/BOT-worker-production.json --output /private/BOT-current.sqlite
python3 deploy/gcp/export_state.py --config /private/BOT-commands-production.json --output /private/BOT-subscriptions.sqlite
```

The exporter uses one Firestore read-only transaction for the manifest and every
chunk, validates identity/hash/size/schema and restores SQLite with its indexes
and derived RFD aliases. It refuses an existing output, publishes atomically with
mode 0600, and prints only row counts and checkpoint time. Reads consume ordinary
Firestore allowance; this does not enable paid managed backup/restore services.
A backup made while a worker runs is a consistent committed point in time, but
pause jobs and wait for active workers before preparing a rollback. Merge the
latest command-store subscriptions into the worker snapshot before restarting
the VM profile, since command changes during a poll may be newer than its mirror.
See [Firestore read-only transactions](https://docs.cloud.google.com/firestore/docs/reference/rest/v1/projects.databases.documents/beginTransaction).

See [the live migration record](gcp-live-migration.md) for this installation.
