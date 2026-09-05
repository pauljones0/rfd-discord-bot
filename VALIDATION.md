# Standalone validation — 2026-09-05

Validation is performed from this repository alone, using its own Go module and
Docker build context. No original bot checkout is needed at runtime or build time.

- The complete test suite, including integration tests, passes with Go's race
  detector. `go vet ./...` passes.
- RFD regression tests cover list/detail parsing, deduplication, URL handling,
  engagement filters, title cleanup, and notification formatting/retries.
- New tests cover SQLite receipt persistence across restart, atomic batch rollback,
  guild-scoped subscriptions, management permissions, and direct-link defaults.
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

This validates extraction and deployment behavior with local fixtures. It does
not claim that a separate production Discord application was provisioned or that
a live RFD scrape was performed for this extraction. The original combined bot
and its working Gateway deployment continue running separately.
