# Sprout — Backend task tracker

**Progress: 54 / 63**

Status board for the tasks specified in [PRD-backend.md](PRD-backend.md). The PRD says *what* each task is and when it counts as done; this file only records *where things stand*.

## Rules

- Tick a task only when every "Done when" check in the PRD passes and the build, lint and tests are green.
- When ticking, append the date and the commit: `- [x] BE-01 · Repository scaffold — 2026-10-09, a1b2c3d`.
- Mark a task in progress with `[~]`, and one that cannot proceed with `[!]` plus a one-line reason.
- If a task was finished differently from its spec, add an indented note under it and fix the PRD so the two never disagree.
- Update the progress count at the top whenever you tick a task.
- Start every session by reading this file to find the next unticked task; end every session by updating it.

## Milestone 1 — Foundation

- [x] BE-01 · Repository scaffold — 2026-10-07, 4d299f2
- [x] BE-02 · Configuration and logging — 2026-10-07, 8f35786
- [x] BE-03 · HTTP server skeleton — 2026-10-07, f2e4184
- [x] BE-04 · Error, response and pagination conventions — 2026-10-07, cc44c57
- [x] BE-05 · Postgres, migrations, sqlc — 2026-10-07, b87d1b3
- [x] BE-06 · OpenAPI spec and code generation — 2026-10-07, 5a7ada9
- [x] BE-07 · Test harness and CI — 2026-10-07, 700d81b
  - 2026-10-21: the GitHub workflow runs only by hand (`workflow_dispatch`); `make check` runs the same three jobs locally and is required before every commit, push and PR.
  - `NewTestUser`, `AuthedRequest` and the category and transaction fixture builders were deferred to BE-10, BE-09, BE-15 and BE-17, where what they depend on is built. The PRD records this under each task.
- [x] BE-08 · `money` and `period` packages — 2026-10-07, 44d20b1

## Milestone 2 — Auth and users

- [x] BE-09 · Auth0 JWT middleware — 2026-10-07, 65521ba
  - Uses `auth0/go-jwt-middleware/v3`, the current major, where the PRD named v2. A failure to fetch the JWKS answers `503 unavailable` rather than `401`. The PRD records both.
- [x] BE-10 · User provisioning and `GET /me` — 2026-10-07, c3fa6bc
  - A `name` claim that is an email address is not kept as the name; missing `email`/`name` are stored as empty strings. The PRD records both.
- [x] BE-11 · `PATCH /me`, preferences, onboarding — 2026-10-08, 3b87893
  - The currency lock uses an injected check until BE-17 adds transactions. The PRD records it.
- [x] BE-12 · Object storage and `POST /uploads` — 2026-10-08, b7ca530
  - Added `internal/session` for the request's user (it was in `internal/users`), which the task's file list did not have. The `uploaded` status is unused: the contract has no call that reports a finished upload. The PRD records both.
- [x] BE-13 · Idempotency middleware — 2026-10-08, 9bfbe4a
  - The stored response is status, content type and raw body instead of one `JSONB` column, and the purge of expired keys runs inside `sprout api` until BE-37 adds the job runner. The PRD records both.

## Milestone 3 — Categories and budgets

- [x] BE-14 · Categories schema and default seed — 2026-10-08, ee52375
- [x] BE-15 · Categories CRUD, archive, reorder — 2026-10-08, 3924886
- [x] BE-16 · Budgets bulk update — 2026-10-08, a0b482c

## Milestone 4 — Transactions

- [x] BE-17 · Transactions schema and CRUD — 2026-10-08, 5f5df89
- [x] BE-18 · Transactions list — 2026-10-08, c60e730
- [x] BE-19 · Balance and `GET /home` — 2026-10-08, 98400c3
- [x] BE-20 · Profile stats and streak — 2026-10-08, 7a683b8

## Milestone 5 — Aggregations

- [x] BE-21 · Categories overview summary — 2026-10-08, 3df3c40
- [x] BE-22 · Category detail summary — 2026-10-08, 38c93ac
- [x] BE-23 · Stats summary — 2026-10-08, 4e262e5
- [x] BE-24 · Aggregation performance — 2026-10-08, ce617d9

## Milestone 6 — Goals

- [x] BE-25 · Goals schema and CRUD — 2026-10-08, c1c374a
- [x] BE-26 · Contributions — 2026-10-08, fe5f58d
- [x] BE-27 · Pace and ETA — 2026-10-08, 11d0bf2

## Milestone 7 — Categorisation engine (§5)

- [x] BE-28 · LLM client abstraction — 2026-10-16, 7ce1ea3
  - 2026-10-21: Groq (`qwen/qwen3.8-27b`, over its OpenAI-compatible HTTP API) became the v1 provider; the Anthropic provider stays as `LLM_PROVIDER=anthropic`. The PRD's BE-28 notes describe both.
- [x] BE-29 · Merchant normaliser — 2026-10-21, 746366c
  - Five data files beyond the three named (countries, regions, suffixes, legal forms, aliases); aliases come from `aliases.tsv`, not the dictionary, which does not exist until BE-30. The backfill is goose Go migration 00012 in `internal/backfill`. The PRD's BE-29 notes have the details.
- [x] BE-30 · Rule stores and seeds — 2026-10-21, f569e1b
  - Migration `00013_categorize.sql` (00009 was taken) and the loader in `seed.go`. `mcc_map` and `keyword_rules` are keyed by code and pattern, without `id` or timestamps. `sprout seed-rules` makes the tables match the files: it deletes codes, patterns and seed merchants the files no longer have, and replaces `llm` rows but never `learned` ones. The PRD's BE-30 notes have the details.
- [x] BE-31 · Engine pipeline (deterministic layers) — 2026-10-21, 8ff3188
  - Credits are not categorised (income has no category); confidences are whole percent divided once; three car-rental keywords added. The PRD's BE-31 notes have the details.
- [x] BE-32 · LLM classification layer — 2026-10-21, e502bac
  - Merchants are re-normalised from redacted text before they are sent, and a key that changes is not saved; invented types are dropped in code. The PRD's BE-32 notes have the details.
- [x] BE-33 · Kind detection — 2026-10-21, de94ea8
  - The labelled fixture is `testdata/categorize/kinds.tsv` (75 rows); refunds match by key prefix and never pair as transfers. The PRD's BE-33 notes have the details.
- [x] BE-34 · Learning loop — 2026-10-21, cc22d27
  - Migration `00014_merchant_votes.sql`. The nightly river job is registered by BE-37, which brings river; BE-34 ships `PromoteDictionary`, the function it runs. Demotion needs at least 3 voters. The PRD's BE-34 notes have the details.
- [x] BE-35 · `POST /categorization/suggest` — 2026-10-21, ca881ee
- [x] BE-36 · Evaluation harness — 2026-10-21, 967dc9b
  - Deterministic: coverage 89.9 %, precision 99.1 %; with `qwen/qwen3.8-27b`: accuracy 95.9 %. The live runs led to three fixes (Groq generation failures retried, `LLM_CATEGORIZE_BATCH` for the free tier's 1,000 output tokens a minute, a date before a web address in the normaliser, with backfill migration 00015). The PRD's BE-36 notes have the details.

## Milestone 8 — Statement normalisation (§4)

- [x] BE-37 · Imports schema, endpoints, job plumbing — 2026-10-21, 334a6e0
  - Migrations `00016` (river's tables, a Go migration in `internal/jobs/schema`) and `00017_imports.sql`. The idempotency purge moved from `sprout api` into the worker as an hourly job, and the BE-34 promotion runs nightly there. The PRD's BE-37 notes have the details.
- [x] BE-38 · Container detection — 2026-10-21, 8a6a7c1
- [x] BE-39 · CSV extractor — 2026-10-21, bf0017d
- [x] BE-40 · OFX/QFX and XLSX extractors — 2026-10-21, f344305
- [x] BE-41 · PDF extractor — 2026-10-21, 4efda93
- [x] BE-42 · Mapping spec and deterministic transform — 2026-10-21, 36086ac
- [x] BE-43 · Mapping resolution: templates and heuristics — 2026-10-21, 773c5f3
  - Adds an internal `direction` column role (debit/credit markers beside an unsigned amount, e.g. ING), not in the contract's user mapping. The PRD's BE-43 notes have the details.
- [x] BE-44 · Mapping resolution: LLM inference and user mapping — 2026-10-21, 59f5d35
  - The pipeline (`imports.Processor`: read file → detect → extract → resolve → transform → rows → ready/needs_mapping/failed) is assembled here so the done-when checks run end to end; BE-45–47 add their stages to it. Storage gains `Get`. The PRD's BE-44 notes have the details.
- [x] BE-45 · Validation, reconciliation, period filter — 2026-10-21, 24f5d88
  - The 5,000-row cap counts the rows the period keeps; under 50 % readable fails as `unreadable`. The PRD's BE-45 notes have the details.
- [x] BE-46 · De-duplication — 2026-10-21, b5d9b54
  - Migration 00018 adds `transactions.external_ref`, which commit fills, so bank references are matched against the ledger; `import_rows.dedup_hash` becomes `BYTEA`. The PRD's BE-46 notes have the details.
- [x] BE-47 · Review API and pipeline assembly — 2026-10-21, 483ab33
  - The stages run in `imports.Processor`, which the job calls, not in `process_import.go`. Migration 00019 adds `import_rows.note` for refunds. The `import_ready` notification is a hook with nothing wired until BE-56. Contract §2.10 now spells out the row PATCH rules. The PRD's BE-47 notes have the details.
- [x] BE-48 · Commit, discard, retention — 2026-10-21, 0b08dc2
  - Migration 00020 adds `uploads.purge_after`/`purged_at` for retention; a committed row's transaction takes the row's id. Contract §2.10 spells out the commit's `422` and `409`. The PRD's BE-48 notes have the details.
- [x] BE-49 · Statement corpus and end-to-end tests — 2026-10-21, e84d233
  - Expected files sit next to the existing fixtures rather than in one corpus directory, so the extraction goldens and the corpus share files. It found two reader bugs, both fixed: delimiter detection on decimal-comma debit/credit files, and the PDF row check on short statements. The PRD's BE-49 notes have the details.

## Milestone 9 — Receipt scanning

- [x] BE-50 · Receipts schema, endpoints, job — 2026-10-21, 401b356
  - Migration is 00021, not 00013. `DELETE` on a confirmed receipt is `409` (contract §2.11 now says so). The PRD's BE-50 notes have the details.
- [x] BE-51 · Receipt extraction — 2026-10-21, 4b55ddf
  - The live fixtures are synthetic receipts rendered from text, not photos. HEIC decodes without cgo. The PRD's BE-51 notes have the details.
- [x] BE-52 · Receipt confirm and retention — 2026-10-21, 93727ba
  - A failed scan can be confirmed with what the user typed. The statement-file purge became the generic `uploads.PurgeFiles` (`purge_upload_files`) for receipt photos too. The PRD's BE-52 notes have the details.

## Milestone 10 — Insights

- [x] BE-53 · Insight rule engine — 2026-10-21, b43b031
  - Notes: the engine is pure: `insights.Snapshot` is built by the caller (BE-54's service reads it from the summaries, the ledger and the goals). Rules: category spend down (≥5 % less, once a current period is half over) or up (≥20 % more), either only when the change is ≥2 % of the period's spending; over budget (naming the expense that is more than half the category's spend); near budget (≥85 %, current period only); peak weekday (≥20 % of a month's or year's spend); total spend down/up on the same thresholds; goal almost there (≥80 % saved) and on track (positive pace with an ETA). Selection also keeps one card per goal. Golden files are `testdata/insights/<rule>.golden.json` (`go test ./internal/insights -update`). Copy says "one $90 expense at Serenity Spa" where the design says "one $90 spa visit": the server knows the merchant, not what was bought.
- [x] BE-54 · Insight generation and `GET /insights` — 2026-10-21, da75278
  - Notes: migration `00022_insights.sql` (00014 was taken). `users.ledger_version` is bumped by statement-level triggers on `transactions`, `categories`, `goals` and `goal_contributions` (one bump per statement, so an import commit costs one update) and by a change of the user's currency or timezone. `insight_runs` records, per (user, period, period_start), the version and the user's calendar day the cards were computed at; `GET /insights` serves the stored `insights` rows while both match and otherwise recomputes under the user's row lock (a second reader waits and serves what the first stored). A new day recomputes too, since near-budget and "less than last month" depend on how far the period is. Cards keep their `position`; the unique key uses `NULLS NOT DISTINCT`. The nightly job is `refresh_insights` at 03:00 UTC: the current month of every user who added a transaction in the last 30 days. Postman gains the insights folder with List insights.

## Milestone 11 — Notifications

- [ ] BE-55 · Device registration
- [ ] BE-56 · APNs sender and inbox
- [ ] BE-57 · Budget alerts
- [ ] BE-58 · Weekly recap

## Milestone 12 — Hardening and release

- [ ] BE-59 · Rate limits and request hardening
- [ ] BE-60 · Account deletion
- [ ] BE-61 · Observability
- [ ] BE-62 · Deployment
- [ ] BE-63 · Contract conformance and demo data
