# Extraction record

This project was extracted on 2026-09-05 from
`github.com/pauljones0/rfd-discord-bot`, source revision
`1363ea7`, plus the subsequently added, locally verified Discord Gateway transport.
The original license and its source attribution are retained unchanged.

The combined repository was subsequently renamed to `pauljones0/homelab-bots`
and made private. This standalone project now owns the public
`pauljones0/rfd-discord-bot` name and matching Go module path. Its fresh Git
history contains only the extracted source; access to the private project is
not required to build or run it. The source revision above belongs to the
combined project's history.

RFD scraping, processing, deduplication, engagement/discount filters, title cleanup,
formatting, and their regression tests were selected from the original source.
The notification client was trimmed to RFD declarations. This project supplies
its own startup/scheduler, command definitions, configuration, and SQLite schema.

The extraction excludes the other bots, combined HTTP command router, shared
document-store implementation, server-specific deployment files, public-ingress
configuration, browser/AI command runners, helper services, tracked logs, database
backups, environment files, and Git history. Scraper fixtures use synthetic HTML;
a cached full forum page was replaced with a minimal equivalent fixture.

Differences relevant to existing users:

- Commands are under `/rfd` and manage only RFD subscriptions. Existing combined
  `/deals` commands remain part of the original project.
- Persistence is an independent SQLite schema. An original combined database
  cannot simply be mounted here. New users configure subscriptions through
  `/rfd subscribe`; existing users can transfer versioned JSON into a new
  database with the [offline importer](MIGRATION.md).
- Personal affiliate identifiers were removed. Optional affiliate settings are
  explicit, and direct links are the default.
- Two matching filters in one channel produce one alert. A local integration test
  also verifies that retained message IDs prevent duplicates after restart.
- Imported notification receipts retain their original application ownership.
  They continue to prevent reposting in their recorded channels, while the new
  application leaves the old application's messages unchanged. The destination
  database is bound to its configured application ID.
- AI cleanup is optional. Configure supported Gemini model IDs if enabling it.
- No original Discord application, credentials, or live data are included.

The running combined deployment was not replaced during extraction. Migration
tools and local verification are preparation for a separate live cutover; their
presence does not mean that a production application was provisioned or a live
workload was moved. The source RFD service stays active until the new application,
permissions, and migration rehearsal are ready. For the final snapshot, stop and
drain the source RFD producer, import its current subscriptions/history, then
activate the new service. Keep unrelated bots and their data in their existing
deployment. The [migration procedure](MIGRATION.md) includes verification and
rollback ordering.

Future changes should be made in this project as the independent RFD codebase.
Do not add a local `replace` directive or sibling-repository dependency to make a
development checkout work; a clean checkout and Docker build must remain sufficient.
