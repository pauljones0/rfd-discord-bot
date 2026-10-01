# Rust production runtime measurements

Measured 2026-09-30 on Linux x86_64, Intel i7-7700K. Both shipping bots are now
Rust 1.95 with static musl/native SQLite; the independently runnable optimized
Go 1.26.0 implementation is archived under each repo's `benchmarks/go-reference`.
All measurements use synthetic fresh databases and loopback APIs, never real
credentials, production state or real Discord destinations.

Five samples per language alternate execution order, after three warmup polls.
The table reports medians, with Go / Rust values. Crux measures 40 polls per
sample and RFD 20. CPU is process user+system time from /proc, not elapsed time.
Idle RSS is measured after a two-second idle interval; peak RSS includes startup
and warmup. The fixture server's CPU is excluded.

| Bot | CPU ms/poll Go / Rust | CPU reduction | Idle RSS MiB Go / Rust | Peak RSS MiB Go / Rust | Total elapsed seconds Go / Rust |
| --- | ---: | ---: | ---: | ---: | ---: |
| Crux | 37.8 / 31.5 | 17% | 19.2 / 9.9 | 19.5 / 11.4 | 3.703 / 2.493 |
| RFD | 204.0 / 119.5 | 41% | 25.0 / 19.1 | 28.5 / 25.4 | 15.162 / 13.040 |

The primary fixture exercises actual source parsing, processing, database writes,
Discord REST delivery, slash-command acknowledgement and Gateway heartbeats.
Crux processes 500 companies across five pages, confirms score changes, fetches
five quotes and delivers an outbox record. RFD observes 200 cards against 2,000
retained records, updates engagement and preserves channel receipts. Cached
details and disabled Gemini make the RFD steady workload repeatable.

These are full fixture workflows, not isolated language kernels. Live TLS,
external site latency, anti-bot challenges, optional AI, prolonged outages and
large real queues are excluded. SQLite is 3.53.4 in Go's WASM driver and 3.53.2
in Rust's native driver: improvements include runtime/storage design, not just
the programming language. Idle CPU rounds to zero over this short interval;
periodic real heartbeats and scheduled jobs still consume CPU.

## Investigating negative results

An initial GNU Rust RFD build retained more memory than Go. We checked allocator
behavior and measured the actual production musl binary, then removed two
whole-history clones per poll from the canonical-URL index. Its index now holds
positions into retained history, while preserving the Go reconciliation corpus.
We also found and fixed a duplicate-alias SQLite trigger defect in both Rust and
Go; existing Rust databases replace old triggers transactionally on open.

The first musl measurement showed slow RFD elapsed time despite lower CPU.
Wall-time syscall tracing found native SQLite using fsync while Go used Linux
fdatasync: approximately 909 native sync calls took 2.40 seconds across seeding,
warmup and one poll; the equivalent Go path used about 913 fdatasync calls.
The Linux production Dockerfile and Arm cross-build now enable SQLite's
HAVE_FDATASYNC path. Linux fdatasync persists file data and metadata needed
for retrieval, including file-size changes; this keeps WAL/FULL commits enabled.
See the [Linux manual](https://man7.org/linux/man-pages/man2/fsync.2.html) and
the bundled SQLite os_unix implementation. This does not prove hardware
power-loss durability, which also depends on the host filesystem/device.

The original fsync samples are retained as `*-fsync-diagnostic.json`; they are
not the shipping benchmark. Host I/O contention affected wall times, so CPU/RSS
results and raw ranges are reported separately from elapsed time. No real
throughput or network latency guarantee follows from loopback timings.

## Deployment validation and limits

Both static images pass non-root data-volume create/reopen and consistent
backup checks, plus complete fixtures with 128 MiB, 64 PID and read-only limits.
Arm64 static binaries pass executable/Gateway/SQLite workflows in QEMU; emulated
CPU/RSS cannot be compared with native Go. Native Oracle VM execution is blocked by provider capacity; authenticated
upload/readback results and launch diagnostics are recorded in
[Oracle validation](oracle-validation.md).

Runtime images contain one bot executable and no Go, compiler, browser, SQLite
server or cloud SDK. Crux's registration alias shares the same executable.
The Compose ceilings remain 256 MiB for Crux and 384 MiB for RFD, with bounded
logs and no exposed bot ports, to allow workload peaks beyond these fixtures.
Persistent data uses one native SQLite worker, a 2 MiB cache target, bounded
queue/deadlines, WAL/FULL and 256-page checkpoints.

RFD keeps receipt/history identity under a 2,000-row cap rather than an age TTL
that could repost visible old threads. Crux retains company identity and pending
delivery without TTL; completed delivery/history use 90-day/10,000-row limits.
A 10,000-pending outbox rejects new snapshots atomically until delivery resumes.
These are row limits, not hard disk byte quotas; short read transactions and
off-VM backup rotation still matter.

Reproduce with each repo's independent `benchmarks/README.md`. Historical Go
optimization and C++/Rust kernel numbers remain separate review artifacts and
must not be presented as measurements of these complete shipping bots.

Raw results: [local five-run data](../benchmarks/runtime-rfd.json).

## Cloud Run profile

The hosted installation now uses optional `gcp` Rust builds, disposable in-memory
SQLite, compressed Firestore checkpoints and HTTP commands instead of persistent
Gateway processes. The Go comparison above measures the VM/native profile; it
must not be presented as a cloud CPU comparison. Cloud request billing depends
on elapsed request time and allocated memory, including source pacing/network
waits, rather than only useful process CPU.

RFD's first scheduled cloud poll processed 39 real deals in 27.550 seconds,
sent one normal alert and preserved all 513 historical receipts. Early production
Cloud Monitoring minute-distribution means were about 31 MiB for RFD and 22–23
MiB for Crux while scanning. These are initial observed means, not workload peak
or memory guarantees. Allocations remain 256 MiB with explicit persisted usage
budgets. See [live cloud evidence](gcp-live-migration.md) and
[the deployment/quota runbook](gcp-deployment.md).
