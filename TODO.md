# TODO

Current production is the Stormtrooper Docker Compose runtime with Postgres.
This file tracks near-term cleanup and follow-up work.

## Next

- Watch one full local scheduler cycle after each deploy and confirm RFD, eBay,
  Memory Express, and Best Buy remain healthy.
- Continue scrape-lab evidence collection for eBay coupon pages and keep
  Browserless as the final fallback only.
- Remove legacy shell-string scraper command env support after one deploy validation
  window confirms the new `*_COMMAND_ARGS` settings work in production.
- Re-register Discord commands after any `/deals` option or label change.

## Audit Follow-Ups

- [x] Update Best Buy compute target tests to match the intended narrower sweep.
- [x] Protect manual processing endpoints with an env-backed admin bearer token and
  restrict public routing to Discord interactions plus health checks.
- [x] Make Discord interaction signature verification fail closed by default unless
  unsigned mode is explicitly enabled for local development or tests.
- [x] Refactor `internal/api/interactions.go` into shared option parsing, response,
  and subscription-kind helpers for common setup/remove paths.
- [x] Centralize JSONB document-store helpers for batch loads, batch sets,
  predicate deletes, and time-based pruning.
- [x] Replace scraper command execution with argv-based `*_COMMAND_ARGS` support,
  keeping legacy shell-string env support temporarily with a warning.

## Later

- Decide whether to keep or remove optional Facebook, Carfax token-service,
  Reddit relay, and HardwareSwap support.
- Revisit whether the JSONB document table should become typed tables after the
  runtime has settled.
