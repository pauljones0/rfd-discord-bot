# Standalone RFD bot

Keep this project runnable from this repository alone. Do not add imports,
filesystem paths, environment dependencies, credentials, or runtime services from
the combined bot or a developer's homelab. Other shopping monitors and unrelated
bots belong in their own projects.

Use local fixtures for scraper and Discord verification. Tests must not post to
real Discord channels or read a production database. Keep `.env`, logs, database
files, backups, and local credentials out of source control and build contexts.

For changes affecting processing, persistence, commands, or delivery, run
`cargo fmt --check`, `cargo clippy --all-targets --locked -- -D warnings`,
and `cargo test --locked`. Verify Docker and its
data-volume permissions when changing deployment files. Diagnose failed results
and distinguish a fixture problem, invalid assumptions, and implementation bugs
before accepting a negative result. Do not weaken assertions to make tests pass.

The production runtime is Rust/native SQLite. Optimized Go remains only under
`benchmarks/go-reference` for offline comparison and golden fixture generation;
it is not built into runtime images. Run Go race/vet there if changing that reference.

The optional `gcp` feature adds request-driven Cloud Run workers and separate
command services. Read `docs/gcp-deployment.md` for this profile; the VM/Gateway
profile remains supported. For GCP changes also run fmt, clippy and tests with
`--features gcp`, plus `python3 -m unittest discover -s deploy/gcp -p 'test_*.py'`.
Fixture and production Firestore namespaces must be separate. Never activate a
cloud scheduler while the old producer/watchdog remains active. Cloud state is
a fenced, compressed Firestore checkpoint, not a durable local SQLite file.
