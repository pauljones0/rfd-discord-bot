# September 2026 release review

The review covered the complete standalone service at rewrite snapshot
`a5ddec1`, including existing behavior outside the rewrite diff. Findings below
were reproduced with local fixtures and corrected before release verification.
The Go runtime, SQLite schema, six filters, numeric thresholds, and title-only
role of optional AI remain unchanged. See [ARCHITECTURE.md](ARCHITECTURE.md) for
the design rationale and the explicitly dated code-size snapshot.

## Scope

Review responsibilities covered `cmd/rfd` and every `internal` package: `ai`,
`api`, `config`, `dealquality`, `dealtypes`, `logger`, `metrics`, `models`,
`notifier`, `processor`, `scraper`, `storage`, `util`, and `validator`. The review
also included import/schema compatibility, dependencies, Docker/Compose and CI
configuration, the Python setup helper, and regression tests.

The review examined data flow, failure handling, cancellation, identity and
receipt ownership, command permissions, source boundaries, fallback behavior,
and deployment compatibility. Tests used disposable SQLite databases and local
HTTP/WebSocket servers; they did not send real Discord messages or call Gemini.

## Findings corrected

| Area | Failure and correction |
| --- | --- |
| Scraper redirects (P1) | Redirects could leave the allowed hosts. Every hop now validates scheme, host, credentials, cancellation, and redirect count; rejected passwords are redacted. |
| Product identity | Fuzzy grouping ran before detail URLs were available and could merge distinct products. Details now precede matching; conflicting product URLs and explicit capacity/model variants veto fuzzy matches. |
| Canonical content | A previously grouped duplicate could replace the canonical title, price, or publication time on later polls. Only the canonical source owns those updates; duplicate details still fill missing evidence. |
| Retained aliases | A grouped thread could replay after its canonical deal left the 48-hour fuzzy window. Retained thread identities now resolve to the canonical row across restart, preserving receipts. |
| Stored identity and scope | SQL keys and JSON payloads could disagree. Deal and subscription reads now reject mismatches; ambiguous retained alias ownership also fails visibly. |
| URL integrity | Referral targets were decoded twice, and URL cleanup could collapse unrelated searches. Destinations decode once; meaningful Amazon/eBay/BestBuy search parameters remain part of identity. |
| Discount evidence | Product attributes such as “100% cotton” could count as discounts. Free-form percentages now require discount wording; structured savings and numeric thresholds retain their existing behavior. |
| Detail extraction | Missing detail prices erased list-card fallbacks, earlier unusable links hid product links, and single-object JSON-LD was discarded. All three cases now preserve usable source data. |
| Discord deadlines | A stalled HELLO/READY could block reconnect and shutdown; SDK 429 retry and cached bucket waits could exceed request deadlines. Socket cancellation and a handshake timer unblock the Gateway; management REST operations return rate-limit errors promptly. |
| Selector validation | Malformed CSS overrides passed startup checks. All configured selectors now compile during validation, with the failing field identified. |

No additional actionable findings emerged from the optional AI and notifier
review. Existing protections were checked: persisted quota cooldown, model/key
fallback, partial title repair, usage accounting, mention suppression, bounded
rendered titles, partial send receipts, retry nonces, and foreign message ownership.

## Verification evidence

Focused race/integration runs passed for the API, AI, notifier, processor, rule,
scraper, and URL-helper packages. Reviewers also completed package-level vet
checks, storage identity fixtures, and all 11 Python setup-helper tests.

New regressions exercise observable outcomes: distinct products produce separate
deals and alerts; repeated duplicates preserve canonical content and ownership;
an alias beyond 48 hours survives a SQLite reopen without another send; corrupt
identities fail reads; rejected redirects never reach their target; encoded links
and fallback prices survive parsing; non-discount percentages do not qualify.
WebSocket fixtures verify HELLO/READY cancellation, timeout followed by reconnect,
and healthy resumed connections remaining open. REST fixtures cover immediate 429
handling and subsequent requests after bucket/global rate-limit headers.

Final combined offline validation passed on September 5, 2026:

```sh
go test -race -tags=integration ./...
go vet ./...
python3 -B -m unittest discover -s scripts -v
```

The repository-wide race/integration suite, vet, all 11 Python tests, and
`go mod tidy` passed. The canonical Docker image built successfully; `check-config`
and `check-storage` passed with fixture settings, a disposable volume, networking
disabled, a read-only root filesystem, and non-root UID/GID 65532.

CI and operational deployment verification are recorded separately from this
offline review evidence. These test results do not claim a particular deployment
state or live provider availability.

## Remaining limits

- Timestamp-derived document IDs can collide; changing the identity scheme
  requires an explicit migration. The fixed variant and stored-identity checks
  do not eliminate that separate limitation.
- Fuzzy matching and discount phrases remain heuristics. Their numeric thresholds
  were not retuned, and alerts do not guarantee value.
- Discord and SQLite cannot commit atomically. Saved receipts and recent nonces
  reduce duplicates, but a crash after sending and before saving can still replay
  after Discord's nonce window. A failed edit in one channel may repeat an edit
  that already succeeded elsewhere.
- Missing sends retry while the deal remains observed; there is no indefinite
  historical delivery queue. Retention eventually removes canonical rows and aliases.
- Rate-limited management operations require a later retry. Fixture success does
  not establish live RFD, Discord, or Gemini availability.
