# Sprout — Backend task tracker

**Progress: 17 / 63**

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
- [ ] BE-18 · Transactions list
- [ ] BE-19 · Balance and `GET /home`
- [ ] BE-20 · Profile stats and streak

## Milestone 5 — Aggregations

- [ ] BE-21 · Categories overview summary
- [ ] BE-22 · Category detail summary
- [ ] BE-23 · Stats summary
- [ ] BE-24 · Aggregation performance

## Milestone 6 — Goals

- [ ] BE-25 · Goals schema and CRUD
- [ ] BE-26 · Contributions
- [ ] BE-27 · Pace and ETA

## Milestone 7 — Categorisation engine (§5)

- [ ] BE-28 · LLM client abstraction
- [ ] BE-29 · Merchant normaliser
- [ ] BE-30 · Rule stores and seeds
- [ ] BE-31 · Engine pipeline (deterministic layers)
- [ ] BE-32 · LLM classification layer
- [ ] BE-33 · Kind detection
- [ ] BE-34 · Learning loop
- [ ] BE-35 · `POST /categorization/suggest`
- [ ] BE-36 · Evaluation harness

## Milestone 8 — Statement normalisation (§4)

- [ ] BE-37 · Imports schema, endpoints, job plumbing
- [ ] BE-38 · Container detection
- [ ] BE-39 · CSV extractor
- [ ] BE-40 · OFX/QFX and XLSX extractors
- [ ] BE-41 · PDF extractor
- [ ] BE-42 · Mapping spec and deterministic transform
- [ ] BE-43 · Mapping resolution: templates and heuristics
- [ ] BE-44 · Mapping resolution: LLM inference and user mapping
- [ ] BE-45 · Validation, reconciliation, period filter
- [ ] BE-46 · De-duplication
- [ ] BE-47 · Review API and pipeline assembly
- [ ] BE-48 · Commit, discard, retention
- [ ] BE-49 · Statement corpus and end-to-end tests

## Milestone 9 — Receipt scanning

- [ ] BE-50 · Receipts schema, endpoints, job
- [ ] BE-51 · Receipt extraction
- [ ] BE-52 · Receipt confirm and retention

## Milestone 10 — Insights

- [ ] BE-53 · Insight rule engine
- [ ] BE-54 · Insight generation and `GET /insights`

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
