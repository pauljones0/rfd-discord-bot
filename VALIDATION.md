# Standalone validation — 2026-09-05

Validation is performed from this repository alone, using its own Go module and
Docker build context. No original bot checkout is needed at runtime or build time.

- The complete test suite, including integration tests, passes with Go's race
  detector. `go vet ./...` passes.
- RFD regression tests cover list/detail parsing, deduplication, URL handling,
  engagement filters, title cleanup, and notification formatting/retries.
- With Gemini disabled, regression tests process more than a full title batch
  without queueing AI work, send eligible alerts using original titles, and keep
  receipts across repeated polls. Unchanged titles retain prior cleanup; changed
  titles discard stale cleanup metadata while updating the existing receipt.
- New tests cover SQLite receipt persistence across restart, atomic batch rollback,
  guild-scoped subscriptions, management permissions, and direct-link defaults.
- Migration storage tests pass with the race detector. They verify receipt
  ownership, subscription/history persistence after reopening SQLite, duplicate
  prevention, application binding, rejection of repeat/nonempty imports, and
  malformed-record validation. A database trigger deliberately fails an insert
  after earlier records were written; the transaction leaves no partial import.
  Legacy username attribution is retained. Separate fixtures verify that a
  foreign database or incompatible table schema is rejected without changing
  database contents or journal mode, and that import rechecks for foreign tables.
- The source JSON exporter and standalone import CLI were exercised together with
  a synthetic fixture and networking disabled. The resulting deal payload retains
  every exported field, including timestamp precision and message IDs, and adds
  explicit source application ownership. The destination application binding is
  verified in SQLite. No production database is used for this test.
- A full local test wires the real RFD scraper, processor, SQLite store, and Discord
  notifier through synthetic HTTPS fixtures. Two matching subscriptions in one
  channel produce one alert, and a second process/store instance does not repost it.
- A simulated Discord Gateway exercises WebSocket Identify, command delivery,
  REST response, disconnection, and session Resume. No real Discord messages are sent.
- Docker contains a static Go binary and certificate roots, runs as UID 65532,
  and uses its own named data volume. The build context excludes environment
  files, databases, logs, and tests.
- The built container passes configuration and SQLite volume checks with networking
  disabled, a read-only root filesystem, and all Linux capabilities removed.
- A local scan found no copies of the running combined bot's credential values,
  environment files, logs, database files, backups, or deployment addresses in
  the extracted source. Existing license/source attribution remains included.

This validates extraction and migration behavior with local fixtures. It does
not claim that a separate production Discord application was provisioned, that a
live RFD scrape was performed, or that a production migration is complete. The
source service remains separate until its operator completes the
[cutover procedure](MIGRATION.md). A read-only `check-discord` preflight can verify
application configuration and subscribed-channel permissions; it does not send
an alert or prove a live notification was delivered.
