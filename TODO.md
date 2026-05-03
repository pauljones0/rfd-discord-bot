# TODO

Current production is the Stormtrooper Docker Compose runtime with Postgres.
This file tracks only near-term cleanup and follow-up work.

## Next

- Watch one full local scheduler cycle after each deploy and confirm RFD, eBay,
  Memory Express, and Best Buy remain healthy.
- Continue scrape-lab evidence collection for eBay coupon pages and keep
  Browserless as the final fallback only.
- Re-register Discord commands after any `/deals` option or label change.

## Later

- Decide whether to keep or remove optional Facebook, Carfax token-service,
  Reddit relay, and HardwareSwap support.
- Add Postgres integration tests behind `POSTGRES_TEST_DSN`.
- Revisit whether the JSONB document table should become typed tables after the
  runtime has settled.
