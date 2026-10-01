# Offline Go/Rust runtime comparison

`go-reference` contains only this bot's optimized retired Go implementation, its
regression tests and offline golden exporters. It is excluded from runtime images
and the production build. Reference database/schema behavior remains compatible;
the RFD alias-trigger defect discovered during this port was fixed in both versions.

`regenerate_goldens.py` recomputes the Rust parity fixtures with that Go source
and compares parsed JSON by default. `--write` deliberately replaces goldens.
Run it with Go installed; no credentials, real APIs or production state are used.
The fixed RNG/timestamps make reconciliation cases repeatable.

Build the reference fixture (`CGO_ENABLED=0 go build -trimpath -ldflags='-s -w'
-o /private/go-fixture ./cmd/runtime-fixture` inside go-reference), then build the
production musl fixture with `docker build --target fixture -t bot-fixture .`.
Extract `/runtime-fixture` using `docker create`, `docker cp`, then remove the
stopped extraction container. Run `runtime_fixture.py --kind rfd --go
/private/go-fixture --rust /private/rust-fixture --runs 5 --polls 20
--output /private/report.json` from this repository.

The harness supplies only fake credentials and a fresh private temporary database,
loopback source/quote/Discord REST/WebSocket servers and seeded history. It runs
three warmup polls, one fixture delivery, command acknowledgement and real Gateway
heartbeats, then measures polling CPU separately with /proc ticks, idle RSS and
process peak RSS. Five samples alternate execution order. Go uses GOMAXPROCS=1
and the previous soft memory setting. Both retain WAL/FULL durability. The Linux production builds enable native
SQLite's supported `HAVE_FDATASYNC` path, matching Go's Linux sync primitive.

Crux exercises 500 companies across five pages, two-observation score confirmation,
five quote fetches and outbox delivery. RFD exercises 200 observed cards, 2,000
retained records, engagement changes and receipt-aware updates. The 200 records
already have details; this benchmark measures cached-detail steady polling.
Gemini is disabled. Source headers, TCP latency and server work are fixture costs;
server CPU is not included. TLS handshakes, live site behavior, large real message
queues and long-term production peaks are not measured. Go's SQLite runs in WASM;
Rust's is native, so gains include storage/runtime design, not language alone.

`--container IMAGE` additionally runs Rust under 128 MiB, 64 PID, read-only,
no-capability container limits, with a private mounted fixture directory and host
loopback networking. This is a resource/permissions check, not a portable CPU
benchmark. `--runner /path/to/qemu-aarch64-static` verifies an Arm binary in user
emulation; emulated CPU/RSS numbers must not be compared with native Go numbers.
The [runtime report](../docs/runtime-optimization.md) contains measured results.
