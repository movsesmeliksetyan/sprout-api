# Sprout API

Go backend for Sprout, an iOS app for tracking spending by category and saving toward goals. The backend owns identity, the ledger, budgets, goals, period aggregations, statement normalisation, categorisation, receipt extraction, insights and push. The iOS app does no financial maths.

## Source of truth

- [docs/PRD-backend.md](docs/PRD-backend.md) — what to build: architecture, data model, subsystem specs, tasks BE-01…BE-63 with "Done when" checks.
- [docs/TASKS-backend.md](docs/TASKS-backend.md) — where things stand.
- [docs/api-contract.md](docs/api-contract.md) — wire format, authoritative. This repo owns it; the iOS repo keeps a read-only copy.

Do not restate these documents elsewhere; read the relevant section before starting a task.

## Session workflow

1. Start by reading `docs/TASKS-backend.md` and take the next unticked task. Work tasks in order, respecting each task's "Needs".
2. Read that task in the PRD, plus any section it references (§4 statements, §5 categorisation) and the contract sections for the endpoints it touches.
3. A task is done only when every "Done when" check passes **and** `make lint test` is green.
4. Tick it in the tracker with date and commit (`- [x] BE-01 · Repository scaffold — 2026-10-09, a1b2c3d`), and update the progress count. Use `[~]` for in progress, `[!]` plus a one-line reason for blocked.
5. If a task was finished differently from its spec, add an indented note under it and fix the PRD so the two never disagree.
6. If the wire format has to change: edit `docs/api-contract.md` first, then `api/openapi.yaml`, then code — and say so in the summary so the change can be passed to the iOS repo.

## Stack

Go (current stable) · `net/http` + `chi` · `oapi-codegen` strict server from `api/openapi.yaml` · PostgreSQL 16+ with `pgx/v5`, `sqlc`, `goose` · `river` for jobs · Auth0 RS256 JWTs via JWKS · S3-compatible storage (MinIO locally) · `log/slog` JSON, OpenTelemetry, Prometheus · `testify`, `testcontainers-go`, golden files.

One binary, three subcommands: `sprout api`, `sprout worker`, `sprout migrate`.

## Commands

`make test` needs Docker running: database tests start their own Postgres with testcontainers.

```
docker compose up -d --wait   # postgres + minio
make migrate-up          # goose migrations
make run                 # API
make worker              # river worker
make generate            # sqlc + oapi-codegen (pinned, via go run); must produce no diff on a second run
make generate-check      # what CI runs: fails if committed generated code is stale
make lint test           # required green before a task is ticked
make eval                # deterministic categorisation eval (CI-gated)
make eval-llm            # on demand only; calls the real LLM
```

## Layout rules

- Layout follows PRD §2.1. Feature packages under `internal/` expose a `Service` (business logic, takes `db.Querier`) and a `Handler` (implements the generated strict-server methods). **Handlers contain no SQL; services contain no HTTP.**
- Adding or changing an endpoint: `docs/api-contract.md` → `api/openapi.yaml` → `make generate` → a method on `httpx.NotImplemented` (the compiler asks for it) → the feature handler. `api/spec_test.go` holds the endpoint list that must match the contract.
- Never hand-edit generated code (`internal/db` sqlc output, `internal/httpx/api_gen.go`). Change `queries/*.sql`, `migrations/` or `api/openapi.yaml` and run `make generate`.
- Never edit an applied migration; add a new one.
- Bank-specific and merchant-specific knowledge lives in **data** (mapping templates, embedded data files, seeds), never in Go code.

## Invariants

- **Tenancy.** Every table with user data has `user_id` and every query filters on it. A resource owned by someone else returns `404`, never `403`.
- **Money.** `BIGINT` minor units, `_minor` suffix. No floats in ledger code. Amounts are positive; direction comes from `kind`. Percentages use integer maths, rounded once at the edge; shares that must sum to 100 use largest-remainder.
- **Time.** Store `TIMESTAMPTZ` plus a `local_date DATE` derived from the user's timezone at write time. All grouping uses `local_date`. Changing a user's timezone does not rewrite history.
- **IDs.** UUID v7.
- **Errors.** One envelope (contract §1.1). Services return typed errors; `httpx` maps them.
- **JSON.** `snake_case`; optional fields are present as `null`, not omitted.
- **Idempotency.** `Idempotency-Key` on every `POST` that creates ledger data, keyed on (`user_id`, key), 24 h TTL.
- **Confirmation.** Imports and receipts never write to `transactions` without an explicit commit/confirm call.
- **Privacy.** Send the LLM only the minimum (headers + sample rows, merchant strings, the receipt image). Mask account numbers, IBANs, card PANs and emails first. No statement content in logs; secrets never in logged config.
- **LLM.** Always behind `llm.Client`; model ids come from config. The LLM decides *how to read* a file — deterministic Go does the reading. LLM failures degrade (`none`, `needs_mapping`), they never fail an import. Check the provider's current Go SDK docs before implementing rather than relying on memory.

## Testing

- Table tests for pure logic; golden files for parsers; `testcontainers-go` for Postgres/MinIO integration tests.
- A package that needs a database adds `func TestMain(m *testing.M) { os.Exit(testutil.Main(m)) }` and calls `testutil.NewDB(t)` in each test. Every test gets its own migrated database, so `t.Parallel()` is safe and no cleanup is needed. `internal/testutil` also has the HTTP helpers; add new shared fixtures there, not in individual packages.
- Tests use the `llm.Fake` and the fake APNs client. Live calls are opt-in behind `-tags=live`.
- Every endpoint gets a cross-user isolation test (expects `404`).
- Fixtures in `testdata/` must be anonymised — no real statements, names or account numbers.

## Out of scope for v1

Bank sync · multi-currency and FX · shared accounts · recurring transactions · web/Android · budget history · localised server strings. Do not build toward these.
