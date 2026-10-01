# Rust port review

The runtime is fully Rust, with no Go/browser/AI runtime requirement. See
[architecture](ARCHITECTURE.md), [validation](VALIDATION.md), and the
[historical Go review](docs/go-review.md).

Go JSON payload/identity compatibility is checked against stored corpora; fixture
tests cover receipt ownership, missing-channel retry, signed/permission commands,
Gateway reconnect and acknowledgement, optional AI cooldown, schema rejection,
SQLite snapshots and shutdown during acknowledged receipt persistence.
