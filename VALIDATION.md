# Rust validation

Run Cargo format/clippy/tests as shown in README. All networking tests use
loopback HTTP/WebSocket fixtures and temporary databases. No runtime secrets or
production database are needed. Docker volume checks cover non-root/read-only
execution, reopen and consistent backups. `benchmarks/runtime_fixture.py` compares
full source/processing/SQLite/Discord REST/Gateway workflows with optimized Go;
see the [runtime report](docs/runtime-optimization.md) for actual measurements.

The [historical Go validation](docs/go-validation.md) remains a review record.

GCP fixture, migration and production evidence is recorded in
[the live migration record](docs/gcp-live-migration.md). The optional cloud
feature has its own clippy/test and Python import/export checks.
