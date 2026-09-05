# Standalone RFD bot

Keep this project runnable from this repository alone. Do not add imports,
filesystem paths, environment dependencies, credentials, or runtime services from
the combined bot or a developer's homelab. Other shopping monitors and unrelated
bots belong in their own projects.

Use local fixtures for scraper and Discord verification. Tests must not post to
real Discord channels or read a production database. Keep `.env`, logs, database
files, backups, and local credentials out of source control and build contexts.

For changes affecting processing, persistence, commands, or delivery, run
`go test -race -tags=integration ./...` and `go vet ./...`. Verify Docker and its
data-volume permissions when changing deployment files. Diagnose failed results
and distinguish a fixture problem, invalid assumptions, and implementation bugs
before accepting a negative result. Do not weaken assertions to make tests pass.
