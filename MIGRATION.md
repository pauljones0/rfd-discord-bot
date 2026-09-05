# Move an existing RFD workload

New installations do not need a migration: follow the README and use
`/rfd subscribe`. This procedure is for preserving another RFD deployment's
subscriptions and notification history while moving to this independent service.
The importer is offline. It does not read bot tokens, contact Discord, scrape
RFD, register commands, or start a scheduler.

Use a dedicated Discord application for this service. Keep tokens in the runtime
environment or your private `.env` file, never in an export or command-line
argument. The source and target application IDs are identifiers, not tokens.

## What is preserved

The version 1 export contains only RFD subscriptions and deal records, including
their channel-to-message receipts. Import restores them in one transaction and
records which application authored each imported message. The new bot uses those
receipts for deduplication and leaves messages authored by the old application
unchanged. New alerts and receipts belong to the new application and can be
updated normally.

This prevents replay of imported deals in channels that already have receipts.
It does not suppress legitimate new deals, newly eligible filters, or delivery to
a newly subscribed channel that never received a deal. Receipts must remain in
the database to provide this protection. Before activation, set
`MAX_STORED_DEALS` to at least the imported deal count, with room for new deals;
the default `2000` limit may be too small for a larger source history.

The source database schema is not the destination schema. Do not mount the
original combined database as `/data/rfd.sqlite`, replace a running database, or
pull this repository into a combined bot's checkout.

## Prepare and rehearse

1. Back up the source deployment's configuration and database. Use SQLite's
   backup API for a running database, or stop the writer before copying the
   database and its required journal files. Copying just a live SQLite main file
   can miss committed writes in its WAL.
2. Create the target application, leave its Interactions Endpoint URL empty, and
   install it in every subscribed server. Set its application ID and token in
   this deployment's private environment. Channel permission overrides must
   allow View Channels, Send Messages, and Embed Links. Keep the source RFD
   producer active while this setup and the rehearsal are in progress.
3. Use the source project's export utility against a consistent snapshot to
   produce version 1 JSON. The source project's exporter is a migration tool,
   not a build or runtime dependency of this bot. Keep the export private: it
   contains server/channel identifiers and deal/message history, but must not
   contain credentials, unrelated bot records, or source configuration.
4. Rehearse with a separate, empty SQLite file and explicit application IDs:

   ```sh
   ./rfd-bot import \
     --file rfd-export.json \
     --database rehearsal/rfd.sqlite \
     --source-app-id SOURCE_APPLICATION_ID \
     --target-app-id TARGET_APPLICATION_ID
   ```

   Replace the two ID placeholders with numeric application IDs. A native binary
   can be built with `go build -o rfd-bot ./cmd/rfd`. The command prints a JSON
   summary containing application IDs, export/import times, and counts of
   subscriptions, deals, and message receipts. Compare those counts with the
   source export. Do not start a polling service against the rehearsal database.

The destination must contain no subscriptions, deals, or other settings. An
application binding for the same target is the only permitted pre-existing
setting. Foreign database tables and incompatible table schemas are rejected
before the importer initializes storage. Repeating an import, even an import with empty record arrays, fails
instead of merging data. Start a new rehearsal with a different empty database
path. Invalid or unsupported records also fail without a partial import.

## Final cutover

Complete setup and rehearsal before interrupting source alerts. Keep backups and
the previous deployment available throughout the cutover.

1. Stop or disable only the source RFD producer and wait for any in-flight RFD
   polling/delivery work to finish. Unrelated bots can remain running. Do not run
   both RFD producers against the same subscriptions.
2. Take a new consistent source snapshot and export again. A rehearsal export is
   stale if the source has continued posting since it was created.
3. Keep the target service stopped and import the final export into its empty
   destination volume. With the supplied Compose file:

   ```sh
   docker compose build
   docker compose run --rm --no-deps -T bot import \
     --file /dev/stdin \
     --database /data/rfd.sqlite \
     --source-app-id SOURCE_APPLICATION_ID \
     --target-app-id TARGET_APPLICATION_ID < rfd-export.json
   ```

   JSON travels over standard input, so the container can use its ordinary
   unprivileged user without exposing a private export through a shared mount.
   Compose still loads `.env`; the import command itself does not use its token.
   If the destination already has data, stop and select a new data volume or a
   separately configured empty database. Do not delete an existing volume merely
   to make the import command pass.
4. Check the reported counts, confirm retention capacity, then run the read-only
   application/channel preflight before registering and starting:

   ```sh
   docker compose run --rm bot check-config
   docker compose run --rm bot check-discord
   docker compose run --rm bot register
   docker compose up -d
   docker compose logs -f bot
   ```

   `check-discord` checks the configured application and permissions in imported
   subscription channels without posting messages. Resolve failures before
   activation. With no subscriptions, it cannot check a destination channel.
5. Verify `/rfd list`, Gateway readiness, poll logs, and the next naturally
   occurring eligible alert. Imported messages should remain unchanged; they
   should not be replayed into channels with retained receipts. Only remove the
   source RFD implementation after the new deployment has been verified.

The target database is pinned to `DISCORD_APP_ID` on import or first startup.
Startup rejects a different application ID. It also rejects nonempty databases
without an identity binding; they must be transferred through an explicit
source-to-target import into a new database. Token rotation within the same
application does not change its ID or require a new database.

## Rollback

Stop the target producer and wait for it to finish before restarting the source
RFD producer. Keep the target database and logs for diagnosis. Never run both as
a fallback at the same time, and never restore the target schema over the source
database.

If the target has already posted new alerts, its receipts are absent from the
old source snapshot. Restarting that snapshot can repost those deals. Reconcile
that interval's history or intentionally account for those potential duplicates
before resuming the source. There is no automatic reverse importer.

## Export format

The outer JSON object has exactly these fields:

| Field | Required value |
| --- | --- |
| `version` | `1` |
| `source_application_id` | Source application's numeric Discord ID as a string |
| `exported_at` | Nonzero RFC3339 timestamp, with optional fractional seconds |
| `subscriptions` | Array of RFD subscription records; use `[]` when empty |
| `deals` | Array of deal records; use `[]` when empty |

Records use the exported Go field names in
[`Subscription`](internal/models/subscription.go) and
[`DealInfo`](internal/models/deal.go), including nested `ThreadContext` fields.
Preserve `DocumentID`, timestamps, and `DiscordMessageIDs` rather than deriving
new IDs. Supported `DealType` values are `rfd_all`, `rfd_tech`, `rfd_warm_hot`,
`rfd_warm_hot_tech`, `rfd_hot`, and `rfd_hot_tech`; `SubscriptionType` is `rfd`
or empty for legacy RFD records.

`AddedBy` is attribution text: older sources may record a username, while newer
ones record a user ID. Preserve that text; it is never used as an API destination.

The importer verifies the source ID against `--source-app-id`, validates records,
and assigns each imported message receipt to that source application. Discord
IDs must be positive decimal strings. Unknown JSON fields, unsupported versions,
duplicate subscriptions or deals, invalid IDs/URLs/timestamps, and inconsistent
message ownership are rejected. Files are limited to 64 MiB, with at most 10,000
subscriptions and 100,000 deals. This is an explicit RFD transfer format, not a
general-purpose dump of another application's database.
