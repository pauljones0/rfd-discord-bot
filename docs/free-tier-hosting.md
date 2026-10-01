# Free VM hosting

See [Oracle deployment and migration](oracle-deployment.md) for the independent
Rust image, free-resource limits, database/TTL policy, consistent backups, and
moving this bot between hosts. The Compose profile disables Gemini and exposes
no bot port. [Runtime measurements](runtime-optimization.md) compares optimized
Go with the actual static Rust processing/SQLite/Discord runtime.
