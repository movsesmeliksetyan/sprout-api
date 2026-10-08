# Sprout — Backend PRD (Go API)

Companion documents: [api-contract.md](api-contract.md) (wire format, authoritative) · [PRD-ui.md](PRD-ui.md) (the consumer).

**Repository.** The backend lives in its own repository. All paths in this document are relative to that repository's root. When the repository is created (BE-01), copy this file, `TASKS-backend.md` and `api-contract.md` into its `docs/` folder; the backend repository then owns the contract, and the iOS repository keeps a read-only copy.

## How to use this document

Progress is tracked in [TASKS-backend.md](TASKS-backend.md), not here: read it at the start of a session to find the next task, and tick the task there (with date and commit) once its "Done when" checks pass. Keep the tracker beside this file in the repository.

Work the tasks in order, one milestone per session where possible. Each task lists what to build, where it lives, and how to tell it is done. A task is finished only when its "Done when" checks pass **and** `make lint test` is green. If a task forces a change to the wire format, update `docs/api-contract.md` first and pass the change on to the iOS repository's copy.

---

## 1. Product summary

Sprout is an iOS app for tracking spending by category and saving toward goals. In v1 the user logs everything themselves — by typing an expense, importing a bank statement, or photographing a receipt. There is no bank connection. The backend owns: identity, the ledger, budgets, goals, all period aggregations, statement normalisation, categorisation, receipt extraction, insights, and push notifications.

### Goals

1. Serve every screen in the design with one or two calls, pre-aggregated, so the app does no financial maths.
2. Accept a statement from **any** bank and turn it into Sprout transactions without per-bank code.
3. Assign a sensible category to transactions that arrive with none, and get better from user corrections.
4. Never write to the ledger from an import or a receipt without explicit user confirmation.

### Non-goals (v1)

Bank/open-banking sync · multi-currency ledgers and FX · shared/household accounts · recurring transactions · web or Android clients · budget history (a budget change applies to all periods) · localisation of server-rendered strings (English only).

---

## 2. Architecture

| Concern | Choice |
|---|---|
| Language / HTTP | Go (current stable), `net/http` + `chi` |
| API definition | `api/openapi.yaml` → `oapi-codegen` (strict server + types) |
| Database | PostgreSQL 16+, `pgx/v5` pool, `sqlc` for queries, `goose` migrations |
| Jobs | `river` (Postgres-backed queue + periodic jobs), run in a `worker` process |
| Auth | Auth0. API validates RS256 access tokens via JWKS (`auth0/go-jwt-middleware/v3`) |
| Object storage | S3-compatible (`aws-sdk-go-v2`); MinIO locally (`pgsty/minio` image). Presigned PUT uploads |
| LLM | Behind an `llm.Client` interface. Default provider: Anthropic Claude (text + vision, structured output). Model ids come from config. The implementing session must check the provider's current Go SDK docs rather than rely on memory |
| Push | APNs HTTP/2, token-based (`.p8`), `sideshow/apns2` |
| Logging / metrics | `log/slog` JSON, OpenTelemetry traces, Prometheus metrics |
| Tests | `testing` + `testify`, `testcontainers-go` for Postgres/MinIO, golden files for parsers |
| Local dev | `docker compose` (postgres, minio), `make` targets |

One binary, three subcommands: `sprout api`, `sprout worker`, `sprout migrate`.

### 2.1 Layout

```
<repo root>/
  cmd/sprout/main.go
  api/openapi.yaml
  migrations/                 # goose SQL
  internal/
    config/  logging/  httpx/  auth/  session/  storage/  llm/  jobs/
    db/                       # sqlc output + queries/*.sql
    money/  period/
    users/  uploads/  categories/  transactions/  summary/  goals/
    categorize/               # merchant normaliser + layered engine
    statements/               # detect, extract, mapping, transform, dedup
    imports/  receipts/  insights/  notify/
  testdata/statements/  testdata/receipts/  testdata/categorize/
  docs/                       # PRD, api-contract, auth0-setup, runbook, perf and eval notes
  Dockerfile  docker-compose.yml  Makefile  .golangci.yml  sqlc.yaml
```

Package rule: feature packages (`transactions`, `goals`, …) expose a `Service` (business logic, takes `db.Querier`) and a `Handler` (implements the generated strict-server methods). Handlers contain no SQL; services contain no HTTP.

### 2.2 Cross-cutting rules

- **Tenancy.** Every table with user data has `user_id`; every query filters on it. A resource owned by someone else returns `404`, never `403`.
- **Money.** `BIGINT` minor units. No floats anywhere in ledger code. Shares/percentages are computed in integer maths and rounded once at the edge.
- **Time.** Store `TIMESTAMPTZ` plus a derived `local_date DATE` (computed from the user's timezone at write time) on transactions and contributions. All grouping uses `local_date`.
- **Errors.** One envelope (contract §1.1). Services return typed errors; `httpx` maps them.
- **Idempotency.** Middleware keyed on (`user_id`, `Idempotency-Key`), 24 h TTL.
- **Privacy.** Only the minimum text needed is sent to the LLM (headers + sample rows for mapping; merchant strings for classification; the image for receipts). Account numbers and IBANs are masked before any LLM call. No statement content in logs.

---

## 3. Data model

All tables: `id UUID PRIMARY KEY` (v7), `created_at`, `updated_at` unless noted.

| Table | Key columns | Notes |
|---|---|---|
| `users` | `auth0_sub UNIQUE`, `email`, `name`, `avatar_key`, `currency`, `timezone`, `starting_balance_minor`, `onboarding_completed`, `notifications_enabled`, `budget_alerts`, `weekly_recap` | |
| `categories` | `user_id`, `name`, `icon`, `shade`, `sort_order`, `monthly_budget_minor`, `archived_at` | Unique `(user_id, lower(name))` where not archived |
| `transactions` | `user_id`, `kind`, `amount_minor`, `category_id NULL`, `merchant`, `merchant_key`, `note`, `occurred_at`, `local_date`, `source`, `import_id NULL`, `receipt_id NULL`, `dedup_hash` | Indexes: `(user_id, local_date DESC, id DESC)`, `(user_id, category_id, local_date)`, `(user_id, dedup_hash)` |
| `goals` | `user_id`, `title`, `emoji`, `image_key`, `target_minor`, `status`, `sort_order`, `completed_at` | `saved_minor` is derived (sum of contributions), not stored |
| `goal_contributions` | `user_id`, `goal_id`, `kind`, `amount_minor`, `occurred_at`, `local_date` | |
| `uploads` | `user_id`, `purpose`, `object_key`, `content_type`, `size_bytes`, `filename`, `status` (`pending`/`uploaded`/`consumed`), `expires_at` | |
| `imports` | `user_id`, `upload_id`, `status`, `period`, `filename`, `format`, `bank_hint`, `mapping_source`, `mapping_spec JSONB`, `mapping_request JSONB`, `header_fingerprint`, `summary JSONB`, `failure JSONB`, `committed_at` | |
| `import_rows` | `import_id`, `user_id`, `row_index`, `raw JSONB`, `status`, `included`, `kind`, `amount_minor`, `occurred_at`, `local_date`, `merchant`, `merchant_key`, `raw_description`, `mcc`, `external_ref`, `category_id`, `category_confidence`, `category_source`, `dedup_hash`, `duplicate_of`, `error` | |
| `mapping_templates` | `header_fingerprint UNIQUE`, `format`, `bank_hint`, `spec JSONB`, `source` (`seed`/`llm`/`user`), `use_count`, `success_count` | Global, no user data inside |
| `merchant_rules` | `user_id`, `merchant_key`, `category_id`, `hits`, `last_used_at` | Unique `(user_id, merchant_key)` |
| `merchant_dictionary` | `merchant_key UNIQUE`, `category_type`, `display_name`, `source` (`seed`/`learned`), `votes` | Global |
| `category_types` | static reference | Canonical types (food, transport, housing, utilities, leisure, selfcare, health, shopping, …) that map to a user's categories by default-icon and name |
| `mcc_map` | `mcc`, `category_type` | Static seed |
| `keyword_rules` | `pattern`, `category_type`, `priority` | Static seed |
| `receipts` | `user_id`, `upload_id`, `status`, `merchant`, `purchased_at`, `total_minor`, `currency`, `payment_method`, `category_id`, `category_confidence`, `line_items JSONB`, `raw_extraction JSONB`, `transaction_id`, `failure JSONB` | |
| `insights` | `user_id`, `period`, `period_start`, `kind`, `tone`, `icon`, `title`, `body`, `category_id`, `goal_id`, `score` | Unique `(user_id, period, period_start, kind, category_id, goal_id)` |
| `devices` | `user_id`, `device_id`, `apns_token`, `environment`, `app_version`, `locale`, `last_seen_at`, `invalidated_at` | Unique `(user_id, device_id)` |
| `notifications` | `user_id`, `kind`, `title`, `body`, `deep_link`, `dedup_key`, `read_at`, `sent_at` | Unique `(user_id, dedup_key)` |
| `idempotency_keys` | `user_id`, `key`, `request_hash`, `status` (`processing`/`completed`), `locked_until`, `response_status`, `response_content_type`, `response_body`, `expires_at` | Primary key `(user_id, key)` |

**Category types** are the bridge between global knowledge ("TESCO is groceries") and a user's personal category list ("Food", or a custom "Groceries"). Each user category carries an optional `category_type` (set for the 7 defaults; inferred from name/icon for custom ones). Global layers resolve to a type, then to the user's category of that type.

---

## 4. Subsystem spec: statement normalisation

**Problem.** Every bank exports a different file: different containers (CSV, OFX, XLSX, PDF), column names, languages, date formats, decimal separators, sign conventions, one amount column or separate debit/credit, preamble rows before the header. Sprout needs one structure.

**Principle.** A single pipeline with one canonical output. Bank-specific knowledge lives in **data** (mapping templates), never in code. The LLM is only ever asked *how to read the file*, and deterministic Go code does the reading.

### 4.1 Canonical output

```go
type NormalizedTransaction struct {
    RowIndex       int
    PostedAt       time.Time // midnight in user tz if the file has only a date
    AmountMinor    int64     // always positive
    Direction      Direction // Debit (money out) | Credit (money in)
    Currency       string    // from file, else user's
    RawDescription string    // exactly as in the file, joined if multi-column
    Counterparty   string    // optional
    MCC            string    // optional
    ExternalRef    string    // optional bank reference / FITID
    BalanceAfter   *int64    // optional
}
```

### 4.2 Mapping spec

The complete, serialisable description of how to read one tabular layout:

```json
{
  "header_row": 4, "skip_footer_rows": 1,
  "columns": { "date": 0, "description": [2, 3], "amount": null, "debit": 5, "credit": 6,
               "balance": 7, "currency": null, "reference": 1, "mcc": null },
  "date_format": "DD.MM.YYYY", "decimal_separator": ",", "thousands_separator": ".",
  "amount_sign": "negative_is_expense", "currency_default": null
}
```

A spec is valid when: exactly one date column; ≥ 1 description column; either `amount` or both `debit` and `credit`; and applying it to the sample rows parses ≥ 95 % of them.

### 4.3 Pipeline stages

| # | Stage | Behaviour |
|---|---|---|
| 1 | **Detect** | Sniff by content, not extension: `%PDF`, ZIP+`xl/` (XLSX), `OFXHEADER`/`<OFX>`, else delimited text. For text: detect encoding (UTF-8/UTF-16/Windows-1252 → UTF-8), delimiter (`,` `;` tab `|`), quote char, and the header row (first row where most cells are non-numeric labels and following rows are consistently typed). |
| 2 | **Extract** | Produce `RawTable{Headers []string, Rows [][]string, Preamble []string}`. CSV: tolerant reader (ragged rows, BOM, embedded newlines). XLSX: first non-empty sheet via `excelize`. OFX/QFX: parse `STMTTRN` directly to `NormalizedTransaction` (`mapping_source: native`, skips stages 3–4). PDF: extract text; ask the LLM to return the transaction table as rows with headers (chunked by page, max 40 pages); then continue through stages 3–4 like a CSV. |
| 3 | **Resolve mapping** | (a) `header_fingerprint` = SHA-256 of lower-cased, trimmed, accent-folded header labels joined in order → look up `mapping_templates`. (b) Heuristics: multilingual header synonym table (date/datum/fecha/buchungstag…, amount/betrag/importe…, debit/credit/paid out/paid in…) plus value-pattern scoring per column (date-likeness, numeric-ness, text entropy). (c) If heuristics score below threshold: LLM mapping inference with headers + 10 sample rows (identifiers masked), returning a mapping spec via structured output. (d) If the result is still invalid or low-confidence: `needs_mapping`. Any spec that validates is saved as a template under the fingerprint. |
| 4 | **Transform** | Apply the spec row by row. Parse dates with the spec's format; parse numbers with the spec's separators (handle `(12.34)`, trailing `-`, `CR`/`DR` suffixes, currency symbols); derive direction from sign or debit/credit column; join description columns with a single space. A row that fails becomes `status: error` with a reason — it never aborts the import. |
| 5 | **Validate & reconcile** | Reject the import (`failure`) if < 50 % of rows parse or there are 0 rows. If balances are present, verify `balance[n] = balance[n-1] ± amount[n]`; a consistent mismatch means the sign convention is inverted → flip and re-check once. Apply the requested `period` window (rows outside are counted in `out_of_period_count` and dropped). Cap at 5,000 rows (`too_many_rows`). |
| 6 | **De-duplicate** | `dedup_hash` = SHA-256(`local_date` \| `amount_minor` \| `direction` \| `merchant_key`). A row matching an existing transaction or an earlier row in a committed import → `status: duplicate`, `included: false`, `duplicate_of` set. Identical rows *within* the same file are **not** duplicates (two coffees on one day are real). If `ExternalRef` exists, match on it first. |
| 7 | **Classify** | Detect kind (expense/income/transfer — §5.4), normalise merchant, run the categorisation engine over all rows in one batch. |
| 8 | **Draft** | Persist `import_rows`, compute `summary`, set `status: ready`. Nothing is in `transactions` yet. |

### 4.4 Feedback into templates

- A template's `use_count`/`success_count` update on every import; a template whose success rate drops below 80 % over ≥ 5 uses is disabled and falls through to heuristics.
- A mapping supplied by a user (`PUT /imports/{id}/mapping`) that validates is stored with `source: user` and wins over an `llm` template for the same fingerprint.

### 4.5 Limits and targets

1,000-row CSV end-to-end in < 20 s with warm templates and no LLM calls; < 60 s cold. File ≤ 15 MB, ≤ 5,000 rows, PDF ≤ 40 pages. Raw statement files are deleted from storage 24 h after commit/discard, or after 7 days if abandoned.

---

## 5. Subsystem spec: categorisation engine

**Problem.** Statements describe a transaction as `CARD PAYMENT TO TESCO STORES 3297 LONDON GB ON 14-07`. There is rarely a category, and never one from Sprout's — or this user's — list.

**Principle.** One shared `categorize.Engine`, used by statement import, receipt scanning and the manual-entry suggestion. Layers run cheapest first; the first layer whose confidence clears the threshold wins. Every result records where it came from.

```go
type Input  struct { RawDescription, Merchant, MCC string; AmountMinor int64; Direction Direction }
type Result struct { CategoryID *uuid.UUID; Confidence float64; Source Source; MerchantKey, DisplayMerchant string }
func (e *Engine) Classify(ctx context.Context, userID uuid.UUID, in []Input, opts Options) ([]Result, error)
```

### 5.1 Merchant normalisation

`RawDescription` → `merchant_key` (stable, lower-case, for matching) and `display_merchant` (title-cased, for the UI).

Steps, in order: upper-case and collapse whitespace → strip bank boilerplate prefixes (`CARD PAYMENT TO`, `POS`, `DEBIT CARD PURCHASE`, `CONTACTLESS`, `VISA`, `DIRECT DEBIT`, localised equivalents) → unwrap payment processors (`SQ *`, `PAYPAL *`, `SUMUP *`, `IZ *`, `GOOGLE *`, `APPLE.COM/BILL`) keeping the inner merchant → strip dates, times, card fragments (`*1234`, `XXXX1234`), long digit runs, store/terminal numbers, reference codes → strip trailing country codes and a city if it matches a known-city list → remove legal suffixes (`LTD`, `GMBH`, `INC`, `S.A.`) → collapse known aliases via the dictionary (`TESCO STORES`, `TESCO EXPRESS`, `TESCO METRO` → `tesco`).

Table-driven tests over ≥ 200 real-looking descriptions are part of the task.

### 5.2 Layers

| # | Layer | Source value | Confidence | Notes |
|---|---|---|---|---|
| 1 | User rules | `user_rule` | 0.99 | Exact `merchant_key` match in `merchant_rules` for this user |
| 2 | Global dictionary | `dictionary` | 0.90–0.97 (by votes) | Exact key, then longest-prefix match; resolves to a category type → user's category |
| 3 | MCC | `mcc` | 0.85 | Only when the file carries an MCC |
| 4 | Keywords | `keyword` | 0.70–0.80 | Ordered patterns: fuel/petrol, pharmacy, supermarket, gym, cinema, electricity/gas/water, rent, restaurant… |
| 5 | LLM | `llm` | model-reported, capped at 0.90 | Unresolved rows only, de-duplicated by `merchant_key`, batched ≤ 50 per call |
| 6 | Fallback | `none` | 0 | `category_id = null` → shown as "Uncategorised" |

Accept threshold: 0.60. Below it the result is `none`.

**Type → category resolution.** If the user has exactly one active category of the resolved type, use it. If several, prefer the one with the most historical transactions for that type. If none (they archived it), the layer misses and the next layer runs.

**LLM layer.** Prompt contains the user's active categories (id, name, type) and the distinct unresolved merchants (key, display name, one raw description, amount sign). Structured output: `[{merchant_key, category_id | null, confidence}]`. The model must choose from the supplied ids or return null — output is validated against the list and anything else is discarded. Results are cached per (`user category set hash`, `merchant_key`) for 30 days. Failures and timeouts degrade to `none`; they never fail the import.

### 5.3 Learning loop

- A user changing a row's category in Review import, or confirming a receipt, or saving a manual expense with a merchant → upsert `merchant_rules(user_id, merchant_key → category_id)`.
- On import commit, every included row's final `(merchant_key → category type)` is a vote. When ≥ 5 distinct users agree on a type for a key with ≥ 80 % agreement, the key is promoted to `merchant_dictionary` (`source: learned`). A nightly job recomputes promotions and demotes keys whose agreement falls below 60 %.
- Votes use category **type**, so custom category names never leak into global data.

### 5.4 Kind detection

Runs before categorisation, on normalised rows.

| Kind | Rule |
|---|---|
| `transfer` | Debit or credit whose description matches transfer patterns (`TRANSFER`, `TFR`, `TO SAVINGS`, own name, `STANDING ORDER TO <own account>`, card-repayment patterns), or a debit/credit pair of equal amount within 2 days inside the same file. Default `included: false`. |
| `income` | Credit not classified as transfer. Refund heuristic: a credit whose `merchant_key` matches a debit merchant in the same file or the user's history is kept as `income` with merchant set and note "Refund". |
| `expense` | Everything else. |

### 5.5 Quality bar

`testdata/categorize/labelled.jsonl` holds ≥ 500 anonymised, hand-labelled descriptions across ≥ 6 bank formats and ≥ 3 languages. `make eval` reports:

- deterministic layers only (1–4): coverage ≥ 60 %, precision ≥ 95 % on covered rows;
- full pipeline with LLM: top-1 accuracy ≥ 90 %;
- kind detection F1 ≥ 0.95.

The deterministic numbers run in CI and fail the build on regression. The LLM number runs on demand (`make eval-llm`).

---

## 6. Tasks

Format — **Do**: what to build · **Files**: main paths (from the repository root) · **Done when**: verifiable checks · **Needs**: prerequisite tasks.

### Milestone 1 — Foundation

**BE-01 · Repository scaffold**
- Do: Create the backend repository (`git init`), copy `PRD-backend.md`, `TASKS-backend.md` and `api-contract.md` into `docs/`, `go mod init`, directory layout from §2.1, `Makefile` (`run`, `worker`, `test`, `lint`, `generate`, `migrate-up`, `migrate-down`, `eval`), `.golangci.yml`, `.editorconfig`, `.gitignore`, `README.md` with setup steps, single `cmd/sprout` entry with `api`/`worker`/`migrate` subcommands (stubs).
- Files: `go.mod`, `Makefile`, `cmd/sprout/main.go`, `.golangci.yml`, `README.md`, `docs/PRD-backend.md`, `docs/TASKS-backend.md`, `docs/api-contract.md`
- Done when: `make lint` and `go build ./...` pass; `sprout --help` lists the three subcommands.
- Needs: —

**BE-02 · Configuration and logging**
- Do: Typed config loaded from env (with `.env` support for local), validated at start-up with a clear error per missing key. `slog` JSON logger with request-scoped fields. Keys: `HTTP_ADDR`, `DATABASE_URL`, `AUTH0_DOMAIN`, `AUTH0_AUDIENCE`, `S3_*`, `LLM_PROVIDER`, `LLM_API_KEY`, `LLM_MODEL_TEXT`, `LLM_MODEL_VISION`, `APNS_*`, `ENV`, `LOG_LEVEL`. `ENV` is `dev`, `staging` or `prod`; `LLM_API_KEY` and `APNS_*` may be empty in `dev` and are required elsewhere.
- Files: `internal/config/config.go`, `internal/config/secret.go`, `internal/logging/logging.go`, `.env.example`
- Done when: unit test covers missing/invalid values; secrets never appear in logged config.
- Needs: BE-01

**BE-03 · HTTP server skeleton**
- Do: `chi` router; middleware for request id, structured access log, panic recovery, 30 s timeout, 1 MB JSON body limit, gzip; graceful shutdown on SIGTERM; `/healthz`, `/readyz`.
- Files: `internal/httpx/server.go`, `internal/httpx/middleware.go`, `internal/httpx/health.go`, `internal/httpx/errors.go` (envelope only; BE-04 adds the typed errors and mapper)
- Done when: `curl /healthz` → 200; a handler that panics returns the `internal` error envelope with a `request_id`; shutdown drains in-flight requests (test).
- Needs: BE-02

**BE-04 · Error, response and pagination conventions**
- Do: Typed domain errors (`ErrNotFound`, `ErrConflict`, `ValidationError{Fields}`, …) and one mapper to the envelope in contract §1.1. The mapper is `httpx.Responder`: `Error` for handler and service errors (unknown → `internal`, logged), `RequestError` for request decoding (unknown → `bad_request`). Opaque cursor encode/decode helper (base64 of `local_date|id`). JSON write helper that emits `null` for nil optionals.
- Files: `internal/httpx/errors.go`, `internal/httpx/respond.go`, `internal/httpx/cursor.go`
- Done when: table test maps every error type to the right status + code; cursor round-trips; tampered cursor → `bad_request`.
- Needs: BE-03

**BE-05 · Postgres, migrations, sqlc**
- Do: `docker-compose.yml` with Postgres and MinIO; `pgx` pool with health check wired into `/readyz`; `goose` embedded migrations run by `sprout migrate up|down|status`; `sqlc.yaml` generating into `internal/db`; a transaction helper `db.WithTx`.
- Files: `docker-compose.yml`, `migrations/00001_init.sql`, `migrations/embed.go`, `sqlc.yaml`, `internal/db/*` (hand-written `pool.go`, `tx.go`, `migrate.go`, `queries/*.sql`; the rest is sqlc output)
- Notes: `sqlc` runs at a pinned version through `make generate`, so no local install is needed. `00001_init.sql` has no tables; it installs `pg_trgm` and the `set_updated_at()` trigger function. MinIO no longer publishes images, so Compose uses the community-maintained build `pgsty/minio`. The pool connects lazily: the API starts without Postgres and reports it through `/readyz`. `sprout migrate` needs only `DATABASE_URL`.
- Done when: `docker compose up -d && make migrate-up` succeeds from clean; `make generate` is reproducible (no diff on second run).
- Needs: BE-02

**BE-06 · OpenAPI spec and code generation**
- Do: Transcribe every endpoint and schema in `docs/api-contract.md` into `api/openapi.yaml`. Generate strict server interface + models with `oapi-codegen`. Mount the generated router under `/v1`; unimplemented operations return `501` for now. Serve the spec at `/v1/openapi.yaml` in non-production, with a Swagger UI page at `/v1/docs` (assets from a CDN, pinned by version and integrity hash). Generate optional response fields without `omitempty` so they are sent as `null` (contract §1), and plug `httpx.Responder` (BE-04) into the strict server's request and response error hooks.
- Files: `api/openapi.yaml`, `api/codegen.yaml`, `api/embed.go`, `api/vacuum.yaml`, `internal/httpx/api_gen.go` (generated), `internal/httpx/unimplemented.go`, `.github/workflows/backend.yml`
- Notes: `oapi-codegen` and the `vacuum` linter run at pinned versions through the Makefile. `httpx.NotImplemented` answers every operation with `501` (code `not_implemented`, temporary and not in the contract); feature handlers are layered over it by embedding it one level deeper, and passed to the server with `httpx.WithAPI`. The generated server binds parameters and decodes bodies but does not enforce schema constraints; services validate. The workflow holds only the generated-code check, so that this task's CI criterion is real; BE-07 adds lint and tests to it.
- Done when: spec validates with an OpenAPI linter; every path in the contract's traceability table exists in the spec; CI fails if generated code is stale.
- Needs: BE-04

**BE-07 · Test harness and CI**
- Do: `testcontainers` helper that starts Postgres once per package, applies migrations, and gives each test an isolated schema or truncation. Helpers: `NewTestUser`, `AuthedRequest(user)` (bypasses JWT with a test verifier), fixture builders for categories/transactions. GitHub Actions workflow: lint, generate-drift check, tests with race detector (the file and the drift job already exist from BE-06; add the lint and test jobs).
- Files: `internal/testutil/*`, `.github/workflows/backend.yml`
- Notes: isolation is a database per test, copied from a template that is migrated once per package, so tests may run in parallel. The helpers that depend on things built later are added by those tasks: the test token verifier and `AuthedRequest` in BE-09, `NewTestUser` in BE-10, the category fixture builder in BE-15 and the transaction fixture builder in BE-17. golangci-lint runs at a pinned version through the Makefile, like the other tools.
- Done when: a sample integration test hitting `/healthz` and a DB round-trip runs locally and in CI.
- Needs: BE-05, BE-06

**BE-08 · `money` and `period` packages**
- Do: `money`: format minor units for server-rendered strings (`$1,842.50`, `$210`), safe percentage helpers (integer rounding, shares that sum to 100 using largest-remainder). `period`: given `(period, offset, timezone, now)` return `Range{Start, End, IsCurrent}`, the previous range, and the bucket list per contract §1.2; budget scaling function.
- Files: `internal/money/money.go`, `internal/period/period.go`
- Notes: calendar days are held as midnight UTC (`period.LocalDate`), matching how `DATE` columns are read, so period arithmetic never touches daylight saving; the user's timezone only decides which day "now" is. `money.Format` knows the symbol for USD, EUR, GBP, JPY, CAD, AUD and AMD and writes any other currency as its code (`CHF 12.50`); extend the table when the launch currency list (open question 4) is settled. The binary embeds the timezone database.
- Done when: table tests cover week boundaries on Monday, month buckets for 28/29/30/31-day months, DST changes, year offsets, leap years; week budget = round(monthly×12/52).
- Needs: BE-01

### Milestone 2 — Auth and users

**BE-09 · Auth0 JWT middleware**
- Do: Validate RS256 tokens against the tenant JWKS (cached, rotated), check issuer and audience, extract `sub`; reject everything else with `unauthenticated`. Write `docs/auth0-setup.md`: create API (audience), Native application for iOS, enable Sign in with Apple + passwordless email (or database) connections, add an Action that puts `email` and `name` into the access token as namespaced claims, create a Machine-to-Machine app with `delete:users` for account deletion.
- Files: `internal/auth/middleware.go`, `internal/auth/claims.go`, `docs/auth0-setup.md`
- Notes: the middleware wraps every `/v1` operation and runs before parameters are read; the health endpoints and `/v1/openapi.yaml` stay open. `email` and `name` are read from `https://sprout.app/email` and `https://sprout.app/name` and are optional. Tokens without `sub` and client-credentials tokens (`sub` ending `@clients`) are rejected. If the JWKS cannot be fetched the answer is `503 unavailable`, not `401`, so an Auth0 outage does not sign users out. Keys are cached for 15 minutes; clock tolerance is 30 s.
- Done when: tests with locally signed tokens cover valid, expired, wrong audience, wrong issuer, bad signature, missing header. `internal/testutil` gains the test token verifier and `AuthedRequest` (deferred from BE-07).
- Needs: BE-03

**BE-10 · User provisioning and `GET /me`**
- Do: `users` migration. Middleware step after JWT: find user by `auth0_sub`, or create it (email/name from claims; timezone default `UTC`, currency default `USD`) inside a transaction that also calls an `OnUserCreated` hook (empty for now; BE-14 plugs category seeding into it); safe under concurrent first requests (unique constraint + retry). Put `User` in request context. Implement `GET /me` (stats stubbed to zeros until BE-20).
- Files: `migrations/00002_users.sql`, `internal/users/service.go`, `internal/users/handler.go`, `internal/db/queries/users.sql`
- Notes: a missing `email` or `name` claim is stored as an empty string (the contract has both as non-null strings). A `name` claim containing `@` is dropped: Auth0 fills the name of a user who has none (passwordless email) with their address, and the user gives a real name during onboarding. Claims never overwrite an existing user. Creation is `INSERT … ON CONFLICT DO NOTHING` in the hook's transaction, so a request that loses the race reads the winner's row, and starts over (up to three times) if the winner's hook rolled it back. `first_name` is the first word of `name`. `avatar_url` is `null` until BE-12. Response timestamps go through `httpx.Instant`: UTC, to the second.
- Done when: first call creates exactly one user even with 10 parallel requests; the hook runs exactly once per user. `internal/testutil` gains `NewTestUser` (deferred from BE-07).
- Needs: BE-09, BE-07

**BE-11 · `PATCH /me`, preferences, onboarding**
- Do: Partial update with validation (IANA timezone, ISO 4217 currency from an allow-list, name length). Currency change rejected with `conflict` once any transaction exists. Changing timezone does **not** rewrite historic `local_date`s.
- Files: `internal/users/service.go`, `internal/users/handler.go`
- Notes: supported currencies are the ones `internal/money` formats (USD, EUR, GBP, JPY, CAD, AUD, AMD; `money.Supported`), accepted in any case and stored upper-case. `name` is trimmed, 1–100 characters. `starting_balance_minor` may be negative and is limited to ±1 000 000 000 000. Every invalid field is reported in one `422`, and nothing is written. The currency lock asks an injected `TransactionCheck` (default: no transactions), which `sprout api` sets to `transactions.HasAny` (BE-17); sending the current currency is not a change. `avatar_upload_id: null` clears the avatar; an id is consumed as an `avatar` upload (BE-12).
- Done when: tests cover each field, invalid values (`422` with `fields`), and the currency lock.
- Needs: BE-10

**BE-12 · Object storage and `POST /uploads`**
- Do: `storage.Store` interface (presign PUT, presign GET, head, delete) with S3 implementation. `uploads` migration and endpoint enforcing purpose-specific content types and size limits (contract §2.2); object key `u/{user_id}/{purpose}/{upload_id}`. Helper `uploads.Consume(ctx, userID, id, purpose)` that verifies the object exists, checks real size, marks it consumed. Wire `avatar_upload_id` in `PATCH /me`; `avatar_url` is a presigned GET (1 h).
- Files: `internal/storage/s3.go`, `internal/uploads/*`, `internal/session/session.go`, `migrations/00003_uploads.sql`
- Notes: the presigned PUT signs `Content-Type` and `Content-Length`, so storage itself refuses a file of another type or size; `Consume` still checks the stored object's size and deletes one over the limit. Upload URLs work for 15 minutes. A declared size over the purpose's limit is `413 payload_too_large`; every other bad declaration is `422`. There is no "upload finished" call in the contract, so an upload goes straight from `pending` to `consumed` and the `uploaded` status is unused. `Consume` takes the caller's querier, so a failed write in the consuming feature leaves the upload usable, and it refuses with typed errors (`ErrNotFound`, also for another user's upload; `ErrWrongPurpose`; `ErrNotUploaded`; `ErrTooLarge`; `ErrAlreadyConsumed`) that the consumer reports as `422` on its own field. A replaced or removed avatar's object is deleted after the profile is saved; a failure there is logged, not returned. The request's user now lives in the leaf package `internal/session` (`session.User(ctx)`), so features can read it without importing `internal/users`. `/readyz` checks the bucket. Tests get a bucket of their own on a real MinIO through `testutil.NewStore`.
- Done when: integration test against MinIO uploads a file through the presigned URL and consumes it; wrong purpose, another user's upload, oversize and never-uploaded all fail correctly.
- Needs: BE-05, BE-10

**BE-13 · Idempotency middleware**
- Do: For `POST` routes flagged idempotent: on first request store key + request hash, run handler, store response; replay returns the stored response; same key with a different body → `conflict`; concurrent duplicate waits or returns `conflict`. Cleanup job for expired keys.
- Files: `internal/httpx/idempotency.go`, `api/idempotent.go`, `migrations/00004_idempotency.sql`
- Notes: a route is idempotent when its operation declares the `Idempotency-Key` header in `api/openapi.yaml` (`api.IdempotentRoutes`); the header is ignored everywhere else. Only a `2xx` response is stored: a failed request created nothing, so its retry runs again. The response is kept as status, content type and raw body, not one `JSONB` column, so a replay is byte-identical; replays carry `Idempotency-Replayed: true`. The request hash covers method, path and body. A duplicate of a request still in flight waits up to 5 s for its response, then gets `409` with `Retry-After`. A `processing` key whose lock (1 min) has run out belongs to a request that died and is taken over by the retry. Expired keys are ignored and overwritten, so the purge only reclaims space; `sprout api` runs it hourly until BE-37 brings the job runner, which should take it over as a periodic job.
- Done when: test fires the same create twice (sequential and parallel) and exactly one row exists.
- Needs: BE-07

### Milestone 3 — Categories and budgets

**BE-14 · Categories schema and default seed**
- Do: `categories`, `category_types` migrations; seed function creating the 7 defaults from contract §2.3, each tagged with its `category_type`, registered on the `OnUserCreated` hook from BE-10 so it runs in the user-creation transaction. Type inference helper for custom categories (name synonyms + icon).
- Files: `migrations/00005_categories.sql`, `internal/categories/seed.go`, `internal/categories/types.go`, `internal/categories/data/types.csv`, `internal/db/queries/categories.sql`
- Notes: the types are `food`, `transport`, `housing`, `utilities`, `leisure`, `selfcare`, `health`, `shopping`, `travel`, `education`, `pets`, `gifts`, `family`, `work`, inserted by the migration; a category's `category_type` references them and may be null. The defaults are typed Food→food, Car→transport, Home→housing, Leisure→leisure, Self-care→selfcare, Health→health, Communal→utilities. The seed skips a name the user already has among their active categories, which is what makes it idempotent. Inference reads its name synonyms and icon→type pairs from the embedded `data/types.csv`: the whole name decides first (lower-cased, punctuation ignored), then each word of it, then the icon; `tag` has no type.
- Done when: a new user gets exactly 7 categories even with 10 parallel first requests; seed is idempotent; inference test maps e.g. "Groceries"→food, "Petrol"→transport, "Gym"→health, unknown→null.
- Needs: BE-10

**BE-15 · Categories CRUD, archive, reorder**
- Do: All category endpoints. Enforce name uniqueness, 30-category cap, valid `icon`/`shade`. `DELETE` only when unused, else `conflict`; archive via `PATCH archived:true` (keeps transactions, hides from lists and pickers). `PUT /categories/order` requires the exact set of active ids.
- Files: `internal/categories/service.go`, `handler.go`, `internal/db/queries/categories.sql`, `internal/testutil/categories.go`
- Notes: every write locks the user's row first, so one user's category changes happen one at a time and the cap, the names and the order are checked against a list that cannot move. Names are trimmed, 1–24 characters; a new category goes last, and without a `shade` takes its position modulo 7. `monthly_budget_minor` is limited to 1 000 000 000 000. Every invalid field is reported in one `422`. A duplicate name is `422` on `name`; reaching the cap is `409 conflict`, because no field of the request is wrong. Only active categories hold their name and count toward the cap. `archived: false` restores a category to the end of the list and is refused when the list is full (`409`) or the name is taken again (`422`); an archived category can still be edited. A category with no type gets one inferred when its name or icon changes; a type, once set, stays. `DELETE` asks an injected `UsageCheck` (default: unused) whether the category has transactions; `sprout api` sets it to `transactions.CategoryInUse` (BE-17). `PUT /categories/order` answers `422` on `ids` for a missing, unknown, archived, duplicate or foreign id, and leaves archived categories' positions alone. `GET /categories?include_archived=true` lists the archived ones after the active ones.
- Done when: integration tests for each endpoint incl. cross-user `404`, duplicate name `422`, reorder with a missing id `422`. `internal/testutil` gains the category fixture builder (deferred from BE-07).
- Needs: BE-14, BE-10

**BE-16 · Budgets bulk update**
- Do: `PUT /budgets` updating many categories atomically (all-or-nothing validation; amounts ≥ 0).
- Files: `internal/categories/budgets.go`, `internal/db/queries/categories.sql`
- Notes: an item must name one of the user's active categories, once; an unknown, archived, foreign or repeated id is `422` like a bad amount, not `404`, since the request names many resources. Errors are keyed `items.<index>.category_id` and `items.<index>.monthly_budget_minor`, all reported together. Amounts have the same upper limit as in BE-15. The response holds only the categories in the request, in display order; an empty `items` answers `200` with an empty list.
- Done when: one invalid item rejects the whole request; response returns updated categories.
- Needs: BE-15

### Milestone 4 — Transactions

**BE-17 · Transactions schema and CRUD**
- Do: Migration with indexes from §3. Create/get/update/delete. Compute `local_date` from the user's timezone, `merchant_key` via the normaliser (stub until BE-29: lower-cased trim), `dedup_hash`. Validation: amount > 0; expense requires an active category owned by the user; income forbids one. Saving a manual expense with a merchant upserts a user merchant rule (hook, no-op until BE-34).
- Files: `migrations/00006_transactions.sql`, `internal/transactions/service.go`, `handler.go`, `dedup.go`, `internal/db/queries/transactions.sql`, `internal/testutil/transactions.go`
- Notes: `amount_minor` is limited to 1 000 000 000 000. `merchant` and `note` are trimmed and an empty one is stored as `null`. A category that is missing, unknown, archived or someone else's is `422` on `category_id`, not `404`. Every invalid field is reported in one `422`. `PATCH` validates the result of the change: a transaction that becomes income loses its category unless the request sends one, which is `422`; one that becomes an expense needs a category in the same request; a category archived after the transaction was filed may stay, since only a newly chosen one must be active. `local_date` is recomputed, in the user's current timezone, only when `occurred_at` changes. `merchant_key` and `dedup_hash` follow every change; `source`, `import_id` and `receipt_id` never change. `DELETE` removes the row. `import_id` and `receipt_id` have no foreign keys until BE-37 and BE-50 create their tables. `dedup_hash` is `BYTEA`, computed by `transactions.DedupHash`, which BE-46 shares. The merchant-rule hook (`WithMerchantRuleHook`) runs inside the saving database transaction for a `manual` expense with a merchant, on create and on update. `sprout api` wires `transactions.HasAny` into the currency lock (BE-11) and `transactions.CategoryInUse` into category deletion (BE-15).
- Done when: integration tests for all four verbs, validation matrix, idempotent create, cross-user isolation. `internal/testutil` gains the transaction fixture builder (deferred from BE-07).
- Needs: BE-13, BE-15

**BE-18 · Transactions list**
- Do: Keyset pagination on `(local_date DESC, id DESC)`; filters `from`, `to`, `category_id`, `kind`, `q` (ILIKE on merchant/note; trigram index). Second query for full-day `days` totals covering the dates in the page.
- Files: `internal/transactions/list.go`, `internal/db/queries/transactions_list.sql`, `migrations/00007_transactions_search.sql`
- Notes: `limit` outside 1–200, a `kind` that is not `expense` or `income`, a `q` longer than 100 characters and a cursor that is not one are `400 bad_request`; an empty `cursor` is the first page. `from` and `to` are inclusive on `local_date`, and `from` after `to` is an empty page. A `category_id` that is unknown or someone else's is an empty page, not `404`. `q` is trimmed, an empty one filters nothing, and `%` and `_` in it stand for themselves. The cursor is only a position: the client sends the same filters with it. `days` is computed under the same filters, newest first, by a second query that is not in a database transaction with the first. The search uses two trigram GIN indexes, on `merchant` and on `note`. `items` and `days` are `[]` when there is nothing, never `null`.
- Done when: paging through 500 seeded rows with limit 50 yields each row once; a day split across two pages reports the same full `net_minor` on both pages.
- Needs: BE-17

**BE-19 · Balance and `GET /home`**
- Do: Balance query per contract §2.5 (goal contributions wired after BE-26; until then treat as 0). Month block from the summary service, `segments` with shares via largest-remainder, 10 recent transactions with `days`, `has_unread_notifications` (false until BE-56).
- Files: `internal/summary/home.go`, `handler.go`, `internal/db/queries/home.sql`
- Notes: the month is the current calendar month in the user's timezone, compared with the one before it; `delta_minor` is this month minus the previous one. `month.categories_count` is the number of the user's active categories, the same figure as `me.stats.categories_count`, whether or not they have spend. A segment's `share` is its whole-percent share (largest remainder, summing to 100) divided by 100. Segments with equal spend come in display order; an archived category with spend this month is still a segment. `recent` is the first page of the transactions list (BE-18) with a limit of 10. The balance counts every transaction, whatever its date. The reads are not in one database transaction.
- Done when: fixture reproducing the design (balance, $1,842.50 month, 7 segments) returns the expected numbers; empty user returns zeros and empty arrays, not errors.
- Needs: BE-18, BE-08

**BE-20 · Profile stats and streak**
- Do: `stats.categories_count`, `goals_count` (0 until BE-25), `streak_days` — consecutive local days with ≥ 1 created transaction/contribution, ending today or yesterday; computed by a single SQL gaps-and-islands query.
- Files: `internal/users/stats.go`, `internal/db/queries/stats.sql`, `migrations/00008_transactions_created_at.sql`
- Notes: a day counts for the streak when a transaction was *added* on it (`created_at`), whenever the transaction happened and however it arrived (by hand, import or receipt), so one import is one day of activity, not a streak. The days are read in the timezone the user has now: `created_at` has no stored local date, so a timezone change can move a day boundary. `categories_count` is the number of active categories. The stats are part of every `Me` response, `PATCH /me` included. Contributions join the streak in BE-26; an index on `(user_id, created_at)` serves the query.
- Done when: tests for no activity (0), today only (1), gap yesterday (resets), activity yesterday but not yet today (still counts).
- Needs: BE-17

### Milestone 5 — Aggregations

**BE-21 · Categories overview summary**
- Do: `GET /summary/categories`: per-category spent, scaled budget, remaining, over-budget flag, % used, share of total, txn count, trend vs previous period; totals row. Includes active categories with zero spend. Sorted by spend desc, then sort_order.
- Files: `internal/summary/categories.go`, `internal/db/queries/summary.sql`
- Notes: an archived category is an item in a period it has spend in, as it is a Home segment, so the total equals Home's month figure; it keeps its stored budget. `total_budget_minor` is the sum of the items' scaled budgets, so the rows add up to it even for a week, where each is rounded. `remaining_minor` is budget minus spend whatever the budget; `over_budget` needs a budget, so a category with none is never over and its `budget_used_pct` is `null`. Spending exactly the budget is not over it. Equal spend comes in display order. `share_pct` is all `0` when nothing was spent. An unknown `period` and an `offset` below 0 or above 1200 are `400 bad_request`, checked by the summary service for all three summary endpoints. One query reads the period and the one before it.
- Done when: fixture test matches the design's numbers for month (Food $482.50 / $500 = 97 %, Self-care over by $10) and correct scaling for week/year; `share_pct` sums to 100.
- Needs: BE-08, BE-17

**BE-22 · Category detail summary**
- Do: `GET /summary/categories/{id}`: header figures plus `trend` buckets (zero-filled), average over buckets, peak value and index (first on ties). Future buckets in the current period are returned with `0`.
- Files: `internal/summary/category_detail.go`, `internal/db/queries/summary.sql`
- Notes: one query reads the category's spend day by day and Go spreads it over `period.Range.Buckets()`, so the three periods share it. The header figures follow BE-21's budget rules. The average divides by every bucket of a past period, and by only the buckets that have started (start on or before today in the user's timezone) in the current one, so it is not diluted by days still to come. With no spend `peak_minor` and `peak_index` are `0`. A category that is unknown or someone else's is `404`.
- Done when: bucket counts are 7/4/12; average and peak verified on fixtures; archived category still readable; unknown id `404`.
- Needs: BE-21

**BE-23 · Stats summary**
- Do: `GET /summary/stats`: total, previous total, delta (signed) and pct, time series with peak, breakdown top-5 + Other with `share_pct` summing to 100.
- Files: `internal/summary/stats.go`, `internal/db/queries/summary.sql`
- Notes: the total counts every expense of the period, an archived category's included, so it equals Home's month figure and BE-21's total; the previous total is the whole preceding period. The series reuses BE-22's bucket filling over a day-by-day query; `peak_index` is the first on a tie and `0` with no spend. The breakdown reuses Home's `SpentByCategory`: only categories with spend are rows, equal spend comes in display order, and the "Other" row is last even when it outweighs a named one. Shares are computed over the rows, so they sum to 100; with no spend the breakdown is `[]`. The three reads are not in one database transaction.
- Done when: fixtures for all three periods; previous total 0 → `delta_pct: null`; fewer than 6 categories → no Other row.
- Needs: BE-21

**BE-24 · Aggregation performance**
- Do: Seed script for a user with 50,000 transactions over 3 years. Benchmark the five read endpoints; add/adjust indexes so each is p95 < 100 ms at the DB on that dataset. Record `EXPLAIN ANALYZE` output in `docs/perf-notes.md`.
- Files: `cmd/sprout/seed.go` (hidden `seed` subcommand), `cmd/sprout/perf_test.go`, `migrations/00009_transactions_aggregation_index.sql`, `internal/db/queries/transactions.sql`, `docs/perf-notes.md`
- Notes: `sprout seed [-sub] [-transactions] [-years]` (`make seed`) finds or creates the user, gives the categories budgets and bulk-loads the ledger with `COPY`; the same options give the same ledger. It needs only `DATABASE_URL`, refuses `ENV=prod` and a user who already has transactions, and is left out of the usage text. Merchants are generated names, not real ones. The benchmark is an ordinary test, `TestAggregationPerformance`, part of `make test` (skipped with `-short`): it seeds the 50,000-transaction user next to four users with 25,000 each, so that one user is a part of the table as in production, vacuums, then runs each request 40 times on one connection and asserts the 95th percentile of the request's queries together, measured from Go, is under 100 ms. A pgx tracer collects every statement the requests run; each is explained with its arguments and fails the test if its plan has a sequential scan on `transactions`. The five endpoints are `/home`, `/transactions` (first page, a deep cursor, category and dates, kind, search) and the three summaries for week, month and year. Before this task only the balance read the whole table. The one new index, `(user_id, kind, local_date) INCLUDE (amount_minor, category_id)`, answers the balance and the period sums from the index alone. The balance still reads every transaction a user has, from the index; it is linear in the ledger. `make perf-notes` rewrites `docs/perf-notes.md` from a run of the test.
- Done when: benchmark test asserts the budget; no sequential scan on `transactions` in any plan.
- Needs: BE-19, BE-22, BE-23

### Milestone 6 — Goals

**BE-25 · Goals schema and CRUD**
- Do: Migrations for `goals`, `goal_contributions`. List (with totals), create, get (with 5 recent contributions), patch, delete. `saved_minor`, `remaining_minor`, `pct` derived in SQL. Max 20 active goals. Image via `image_upload_id` (presigned GET in `image_url`).
- Files: `migrations/00010_goals.sql`, `internal/goals/service.go`, `handler.go`, `internal/db/queries/goals.sql`, `internal/uploads/policy.go`, `internal/users/stats.go`, `internal/testutil/goals.go`
- Notes: a goal's picture is an upload of the new purpose `goal_image` (JPEG, PNG or HEIC, 5 MB), added to the contract; the migration widens the `uploads.purpose` check. A refused upload is `422` on `image_upload_id`; a replaced or removed picture, and a deleted goal's, is deleted from storage, a failure only logged. `saved_minor` is a `sum` in SQL; `remaining_minor` (never below 0) and `pct` (never above 100) are computed in Go with the `money` helpers, like every other percentage. `monthly_pace_minor` is `0` and `eta_month` `null` until BE-27. The list holds active and completed goals by `sort_order`, then id; its totals cover those goals and its `pct` is `0` without any. `title` is trimmed, 1–40 characters; `emoji` is trimmed, an empty one is `null`, at most 16 characters; `target_minor` is limited to 1 000 000 000 000; every invalid field is reported in one `422`. A new goal goes last. `sort_order` in a `PATCH` is stored as given (≥ 0): there is no reorder endpoint. The cap of 20 counts `active` goals only and is `409 conflict`, on create and when an archived goal is brought back; a goal reopened by a higher target is not refused. `PATCH status` accepts `archived` and `active`; `completed` is `422`, because a goal that is not archived is completed exactly when saved ≥ target, which the service re-evaluates on every update (a changed target, a goal brought back). `completed_at` is set when a goal becomes completed, cleared when it reopens and kept while archived. Every write locks the user's row first, as categories do. `GET /goals/{id}` also reads an archived goal. `POST /goals` takes no `Idempotency-Key`: a goal is not ledger data. `me.stats.goals_count` is the number of active and completed goals. `goal_contributions` has its insert query and an index for the streak already; the endpoints are BE-26. `internal/testutil` gains the goal and contribution fixture builders.
- Done when: integration tests incl. totals matching the design fixture ($1,338 of $2,169 → 62 %), delete cascades contributions.
- Needs: BE-13

**BE-26 · Contributions**
- Do: Create/list/delete. Top-up and withdrawal rules (withdrawal ≤ saved). Auto-complete the goal (set `status`, `completed_at`, enqueue a `goal_completed` notification — no-op until BE-56) when saved ≥ target; reopen if a delete/withdrawal drops it below. Wire contributions into the balance formula (BE-19) and streak (BE-20).
- Files: `internal/goals/contributions.go`, `handler.go`, `internal/db/queries/goals.sql`, `home.sql`, `stats.sql`, `migrations/00011_goal_contributions_list_index.sql`, `internal/summary/home.go`
- Notes: `amount_minor` is limited to 1 000 000 000 000, like a target; a withdrawal of more than is saved is `422` on `amount_minor`, and one of exactly what is saved is allowed. `occurred_at` defaults to now and `local_date` is its day in the user's timezone. The create is idempotent through the `Idempotency-Key` middleware, as transactions are. A contribution writes under the user's row lock, like every goal write, and re-evaluates the goal with the rule of BE-25: completed exactly when saved ≥ target, `completed_at` set then and cleared on reopening. A completed goal can be topped up further. An archived goal takes contributions and stays archived; brought back, it is completed if fully saved. Deleting a top-up that later withdrawals depend on would leave less than nothing saved and is `409 conflict`. A contribution that is unknown, someone else's or of another goal is `404`. The list and the goal's five recent contributions are ordered as the ledger is, by `local_date` then id, newest first, and share its cursor; the migration replaces the `occurred_at` index of BE-25 with one for that order. "Becoming completed" (by a top-up, a deleted withdrawal or a lowered target) runs the service's `CompletionHook` inside the transaction; nothing is hooked in until BE-56 enqueues the `goal_completed` notification there. The balance subtracts the net of every contribution, those of archived goals included; a deleted goal's contributions go with it, so its money returns. The streak query takes the days of `goal_contributions.created_at` together with the transactions'.
- Done when: balance decreases by top-ups and increases by withdrawals; over-withdrawal `422`; idempotent create; completion toggles both ways.
- Needs: BE-25, BE-19

**BE-27 · Pace and ETA**
- Do: `monthly_pace_minor` = net contributions in the trailing 90 days ÷ 3 (or ÷ months since creation, min 1, if younger). `eta_month` = current month + ceil(remaining / pace); null if pace ≤ 0 or completed.
- Files: `internal/goals/pace.go`, `service.go`, `contributions.go`, `internal/db/queries/goals.sql`
- Notes: the trailing 90 days are the user's calendar days up to and including today, read from `local_date`; a contribution dated later than today does not count until its day comes. One formula covers both ages: pace = net × 30 ÷ the goal's age in days, the age held between 30 and 90, rounded once — a third of the net for a goal 90 days old or older, and for a younger one the average over the days it has existed, a goal under a month old counting as a month. The pace is signed: negative when more was withdrawn than topped up. `eta_month` counts from the current month in the user's timezone; it is `null` when the pace is ≤ 0, when nothing remains (a completed goal, or an archived one that is fully saved), and when it would be more than 1 200 months away. An archived goal has both figures like any other. Every response that holds a goal carries them: one grouped query reads the window for the whole list, so the list costs one query more, not one per goal. The service methods that return a goal take the user, for the timezone.
- Done when: table tests for new goal, steady saver, stalled goal, completed goal, withdrawal-heavy goal.
- Needs: BE-26

### Milestone 7 — Categorisation engine (§5)

**BE-28 · LLM client abstraction**
- Do: `llm.Client` interface with `CompleteJSON(ctx, req, schema) ` and `VisionJSON(ctx, req, image, schema)`. Provider implementation (Anthropic), a deterministic `Fake` for tests (canned responses keyed by prompt hash), per-call timeout, bounded retries with backoff on 429/5xx, token + latency + cost logging, a global concurrency limiter, a `redact` helper masking IBANs/account numbers/card PANs/emails. Read the provider's current SDK documentation before implementing.
- Files: `internal/llm/client.go`, `internal/llm/anthropic.go`, `internal/llm/fake.go`, `internal/llm/redact.go`
- Done when: fake-backed tests cover schema-invalid output (rejected), timeout, retry; redact tests cover each identifier type; one opt-in live smoke test (`-tags=live`).
- Needs: BE-02

**BE-29 · Merchant normaliser**
- Do: Implement §5.1 as an ordered list of pure transforms returning `(merchant_key, display_merchant)`. Rules and prefix lists in embedded data files, not code. Backfill `merchant_key` on existing transactions via migration job.
- Files: `internal/categorize/normalize.go`, `internal/categorize/data/prefixes.txt`, `processors.txt`, `cities.txt`, `testdata/categorize/normalize.tsv`
- Done when: ≥ 200-row table test passes; normaliser is idempotent (`f(f(x)) == f(x)`); benchmark ≥ 100k descriptions/s.
- Needs: BE-17

**BE-30 · Rule stores and seeds**
- Do: Migrations + queries for `merchant_rules`, `merchant_dictionary`, `mcc_map`, `keyword_rules`. Seed files: ≥ 300 common merchants across UK/EU/US, the standard MCC ranges → category types, ≥ 60 keyword patterns (multi-language). Loader command `sprout seed-rules` (idempotent upsert).
- Files: `migrations/00009_categorize.sql`, `internal/categorize/store.go`, `internal/categorize/data/dictionary.csv`, `mcc.csv`, `keywords.csv`
- Done when: seeds load twice without duplicates; lookups are batch (one query per layer for N inputs).
- Needs: BE-14

**BE-31 · Engine pipeline (deterministic layers)**
- Do: `Engine.Classify` running layers 1–4 and 6 from §5.2 in batch, with type→category resolution, thresholding, and `Source` on every result. `Options{AllowLLM bool}`.
- Files: `internal/categorize/engine.go`, `internal/categorize/resolve.go`
- Done when: tests prove precedence (user rule beats dictionary beats MCC beats keyword), archived-category fallthrough, multi-category-of-same-type tie-break, and that N inputs cause O(layers) queries, not O(N).
- Needs: BE-29, BE-30

**BE-32 · LLM classification layer**
- Do: Layer 5: collect unresolved distinct merchant keys, batch ≤ 50, prompt with user's categories, validate output ids, cap confidence at 0.90, cache table keyed by (category-set hash, merchant_key) with 30-day TTL. Degrade silently on failure.
- Files: `internal/categorize/llm_layer.go`, `migrations/00010_llm_cache.sql`
- Done when: fake-LLM tests cover hallucinated id (dropped), cache hit (no call), partial batch failure (others still returned), custom user category chosen.
- Needs: BE-28, BE-31

**BE-33 · Kind detection**
- Do: §5.4 — transfer patterns (data file, multi-language), in-file debit/credit pairing, refund heuristic, own-name match using `users.name`.
- Files: `internal/categorize/kind.go`, `internal/categorize/data/transfer_patterns.txt`
- Done when: labelled fixture F1 ≥ 0.95; salary credit → income; card repayment → transfer; store refund → income with "Refund" note.
- Needs: BE-29

**BE-34 · Learning loop**
- Do: `Learn(userID, merchantKey, categoryID)` upserting user rules (called from transaction create/update, import row patch, receipt confirm). Vote recording on import commit. Nightly `river` job computing promotions/demotions per §5.3.
- Files: `internal/categorize/learn.go`, `internal/jobs/dictionary_promote.go`, `migrations/00011_votes.sql`
- Done when: correcting a merchant once changes the next classification for that user only; 5 agreeing users promote a key; disagreement below 60 % demotes it.
- Needs: BE-31

**BE-35 · `POST /categorization/suggest`**
- Do: Deterministic-only classification for a single input; p95 < 50 ms.
- Files: `internal/categorize/handler.go`
- Done when: returns the user's rule after one correction; never calls the LLM (asserted with a fake that fails on use).
- Needs: BE-31

**BE-36 · Evaluation harness**
- Do: Build the labelled set (§5.5), `make eval` (deterministic, CI-gated) and `make eval-llm` (on demand) printing coverage, precision, accuracy and a confusion table per category type.
- Files: `internal/categorize/eval_test.go`, `testdata/categorize/labelled.jsonl`, `Makefile`
- Done when: deterministic thresholds from §5.5 met and enforced in CI; LLM run result recorded in `docs/categorization-eval.md`.
- Needs: BE-32, BE-33

### Milestone 8 — Statement normalisation (§4)

**BE-37 · Imports schema, endpoints, job plumbing**
- Do: Migrations for `imports`, `import_rows`, `mapping_templates`. `river` client + worker process wiring (first job in the codebase: queue config, retries, error handler, graceful stop). `POST /imports` (consume upload, insert, enqueue `ProcessImport`), `GET /imports/{id}`, `DELETE /imports/{id}`. Job skeleton that marks `failed: internal` until stages exist.
- Files: `migrations/00012_imports.sql`, `internal/imports/service.go`, `handler.go`, `internal/jobs/client.go`, `internal/jobs/process_import.go`
- Done when: posting an import returns `202 processing` and the job runs in the worker; job retry is idempotent (re-run wipes and rebuilds rows).
- Needs: BE-12, BE-13

**BE-38 · Container detection**
- Do: §4.3 stage 1 — magic-byte sniffing, encoding detection + transcoding, delimiter/quote detection by consistency scoring, header-row location.
- Files: `internal/statements/detect.go`
- Done when: fixtures for each container, UTF-16 with BOM, Windows-1252 with `£`/`€`, semicolon-delimited, 6-line preamble, and a `.csv` that is really XLSX all detect correctly; garbage → `unsupported_format`.
- Needs: BE-01

**BE-39 · CSV extractor**
- Do: Tolerant delimited reader → `RawTable` (ragged rows padded, embedded newlines, stray quotes, trailing summary rows identified as footer).
- Files: `internal/statements/extract_csv.go`
- Done when: golden tests over ≥ 10 varied CSV fixtures.
- Needs: BE-38

**BE-40 · OFX/QFX and XLSX extractors**
- Do: OFX 1.x (SGML) and 2.x (XML) `STMTTRN` → `NormalizedTransaction` directly (`TRNAMT` sign, `DTPOSTED`, `NAME`+`MEMO`, `FITID` → `ExternalRef`). XLSX first non-empty sheet → `RawTable`, converting Excel serial dates to ISO strings.
- Files: `internal/statements/extract_ofx.go`, `extract_xlsx.go`
- Done when: golden tests for OFX 1/2, credit-card and bank statements, XLSX with merged header cells.
- Needs: BE-38

**BE-41 · PDF extractor**
- Do: Text extraction; if there is no text layer, fail with `unreadable` (no OCR of scanned PDFs in v1). Page-chunked LLM call returning `{headers, rows}`; merge pages, drop repeated headers; row-count sanity check against a regex count of date-like line starts (± 10 %), else `unreadable`.
- Files: `internal/statements/extract_pdf.go`
- Done when: fake-LLM tests for 1-page and multi-page; scanned PDF → `unreadable`; > 40 pages → `too_many_rows`.
- Needs: BE-28, BE-38

**BE-42 · Mapping spec and deterministic transform**
- Do: `MappingSpec` type + validation (§4.2); transform (§4.3 stage 4) with date-token parser (`DD`,`MM`,`YYYY`,`YY`,`MMM` incl. localised month names), number parser (separators, parentheses, trailing minus, `CR`/`DR`, currency symbols), direction logic, description joining.
- Files: `internal/statements/mapping.go`, `transform.go`, `parse_date.go`, `parse_amount.go`
- Done when: property/table tests for the parsers (≥ 60 amount cases, ≥ 30 date cases); bad row yields a row error, not a panic or abort.
- Needs: BE-39

**BE-43 · Mapping resolution: templates and heuristics**
- Do: Header fingerprint; template lookup/save with counters; heuristic scorer (multilingual synonym table for EN/DE/ES/FR/IT/NL/PT/PL/RU/HY + value-pattern scoring) producing a spec and a confidence; date-format inference from samples (disambiguate DD/MM vs MM/DD using values > 12, else locale hint, else low confidence); decimal-separator inference.
- Files: `internal/statements/resolve.go`, `heuristics.go`, `internal/statements/data/header_synonyms.yaml`
- Done when: ≥ 12 real-layout fixtures resolve by heuristics alone; second import of the same layout hits the template (asserted, zero heuristic calls).
- Needs: BE-42, BE-37

**BE-44 · Mapping resolution: LLM inference and user mapping**
- Do: LLM mapping inference (headers + 10 masked sample rows → `MappingSpec` via structured output), validated by applying it to samples. On failure/low confidence set `needs_mapping` with `mapping_request`. Implement `PUT /imports/{id}/mapping` (convert column-role list + hints into a full spec, validate, save as `user` template, re-enqueue). §4.4 template feedback rules.
- Files: `internal/statements/resolve_llm.go`, `internal/imports/mapping.go`
- Done when: a deliberately obscure-header fixture resolves via fake LLM; invalid LLM spec → `needs_mapping`; user mapping completes the import and a second file with the same headers needs no mapping.
- Needs: BE-28, BE-43

**BE-45 · Validation, reconciliation, period filter**
- Do: §4.3 stage 5 — parse-rate gate, running-balance check with single sign-flip retry, period window in user timezone, row cap, summary counters.
- Files: `internal/statements/validate.go`
- Done when: fixture with inverted sign convention is auto-corrected via balances; `period=week` keeps only the current week and reports `out_of_period_count`.
- Needs: BE-42

**BE-46 · De-duplication**
- Do: §4.3 stage 6 — hash computation shared with `transactions.dedup_hash`; match by `ExternalRef` first; preserve genuine in-file repeats by matching counts (if the ledger has 1 and the file has 2 identical rows, only 1 is a duplicate).
- Files: `internal/statements/dedup.go`
- Done when: re-importing the same file yields 100 % duplicates; overlapping-month files only flag the overlap; two identical coffees survive.
- Needs: BE-45, BE-17

**BE-47 · Review API and pipeline assembly**
- Do: Assemble all stages in `ProcessImport` (detect → extract → resolve → transform → validate → dedup → kind → categorise → persist → `ready`). `GET /imports/{id}/rows` (uncategorised first), `PATCH …/rows/{row_id}` (recompute summary; category change calls `Learn`). Enqueue `import_ready` notification when processing took > 10 s.
- Files: `internal/jobs/process_import.go`, `internal/imports/rows.go`
- Done when: end-to-end integration test from upload to `ready` for CSV, OFX, XLSX; patching a row updates `summary` counts.
- Needs: BE-44, BE-46, BE-33, BE-32

**BE-48 · Commit, discard, retention**
- Do: `POST …/commit` in one DB transaction: validate included rows, insert transactions (`source: import`, `import_id`), record dictionary votes, mark committed. Idempotent. Retention job per §4.5 deleting stored files and `import_rows.raw`.
- Files: `internal/imports/commit.go`, `internal/jobs/import_retention.go`
- Done when: commit of 1,000 rows < 2 s; double commit creates nothing new; missing category → `422` with row ids; summaries reflect the new transactions immediately.
- Needs: BE-47, BE-34

**BE-49 · Statement corpus and end-to-end tests**
- Do: Assemble ≥ 20 anonymised statements (≥ 8 countries/banks; CSV, OFX, XLSX, PDF; debit/credit-column and signed-amount styles; `,` and `.` decimals; with and without balance column), each with an expected-output JSON. Runner asserts row count, sum, dates, and reports mapping source per file. Document how to add a new bank fixture.
- Files: `testdata/statements/*`, `internal/statements/e2e_test.go`, `docs/adding-a-statement-fixture.md`
- Done when: ≥ 90 % of fixtures reach `ready` with no user mapping; all reach `ready` or `needs_mapping` (none `failed`); CI runs it with the fake LLM.
- Needs: BE-48

### Milestone 9 — Receipt scanning

**BE-50 · Receipts schema, endpoints, job**
- Do: Migration; `POST /receipts`, `GET /receipts/{id}` (`image_url` presigned), `DELETE`; `ProcessReceipt` job skeleton.
- Files: `migrations/00013_receipts.sql`, `internal/receipts/service.go`, `handler.go`, `internal/jobs/process_receipt.go`
- Done when: post → `processing` → job runs; cross-user `404`.
- Needs: BE-37

**BE-51 · Receipt extraction**
- Do: Normalise the image (HEIC→JPEG, EXIF rotate, max 2,000 px long edge), vision LLM call with structured output: merchant, purchase date/time, total, currency, payment method (brand + last 4 only), line items, and `is_receipt`. Validation: `is_receipt=false` → `not_a_receipt`; line items sum within 2 % of total else keep total and flag items; currency mismatch with user's → keep amount, note in `raw_extraction`. Category via `categorize.Engine` (merchant + line-item names as hint).
- Files: `internal/receipts/extract.go`, `internal/receipts/image.go`
- Done when: fake-vision tests for clean receipt, missing date, non-receipt photo, long receipt; opt-in live test over `testdata/receipts/*` with expected totals.
- Needs: BE-28, BE-31, BE-50

**BE-52 · Receipt confirm and retention**
- Do: `POST …/confirm` creates the transaction (`source: receipt`, `receipt_id`), marks receipt `confirmed`, calls `Learn`. Idempotent. Retention: unconfirmed receipts and their images deleted after 7 days; confirmed images kept and exposed via the transaction's `receipt_id`.
- Files: `internal/receipts/confirm.go`, `internal/jobs/receipt_retention.go`
- Done when: confirm with user-edited amount/category succeeds; second confirm returns the same transaction.
- Needs: BE-51, BE-17

### Milestone 10 — Insights

**BE-53 · Insight rule engine**
- Do: `Rule` interface (`Evaluate(ctx, snapshot) []Candidate` with score). Snapshot = the summaries for the period and the previous one, per-weekday totals, largest transactions per category, goals with pace. Rules for each `kind` in contract §2.7 with the copy templates (warm, second-person, sentence case, money formatted via `money`). Selection: top 5 by score, max 1 per category, at least one positive if any exists. `no_data` when the period has < 3 transactions.
- Files: `internal/insights/engine.go`, `internal/insights/rules_*.go`, `internal/insights/copy.go`
- Done when: fixture reproducing the design yields the three cards ("You spent 8% less on Food…", "Self-care is over budget", "Your biggest day was Saturday"); golden tests per rule.
- Needs: BE-23, BE-27

**BE-54 · Insight generation and `GET /insights`**
- Do: Compute on read with a short cache: stored rows refreshed when the user's ledger changed since last computation (track `users.ledger_version`, bumped on writes). Nightly periodic job pre-computes the current month for active users.
- Files: `internal/insights/service.go`, `handler.go`, `internal/jobs/insights_refresh.go`, `migrations/00014_insights.sql`
- Done when: adding a transaction changes the next read; unchanged ledger serves from the table with no recomputation (asserted).
- Needs: BE-53

### Milestone 11 — Notifications

**BE-55 · Device registration**
- Do: `PUT`/`DELETE /devices/{device_id}`; upsert token, environment, version; a token re-registered under another user moves to that user.
- Files: `migrations/00015_notifications.sql`, `internal/notify/devices.go`
- Done when: tests for upsert, move between users, delete.
- Needs: BE-10

**BE-56 · APNs sender and inbox**
- Do: `notify.Send(ctx, userID, Notification)` → insert into `notifications` (dedup by `dedup_key`) → enqueue `SendPush` unless `notifications_enabled=false` or the kind's sub-preference is off. `SendPush` delivers to all valid devices; `410`/`BadDeviceToken` invalidates the device. `GET /notifications`, `POST /notifications/read`; wire `has_unread_notifications` into `/home`. APNs client behind an interface with a fake.
- Files: `internal/notify/service.go`, `apns.go`, `handler.go`, `internal/jobs/send_push.go`
- Done when: fake-APNs tests for delivery, token invalidation, disabled preference (stored in inbox, not pushed), dedup.
- Needs: BE-55, BE-37

**BE-57 · Budget alerts**
- Do: After any write that adds expense to a category in the current month (transaction create/update, import commit, receipt confirm), enqueue a debounced check; send one alert when crossing 80 % and one at 100 % per category per month (`dedup_key = budget:{category}:{YYYY-MM}:{threshold}`). No alerts for budget 0. An import commit sends at most one combined alert.
- Files: `internal/notify/budget_alerts.go`, `internal/jobs/budget_check.go`
- Done when: crossing 80 then 100 produces exactly two notifications; deleting and re-adding does not re-send; bulk import → one notification.
- Needs: BE-56, BE-21

**BE-58 · Weekly recap**
- Do: Periodic job running hourly; for users whose local time is Monday 09:00–09:59, with `weekly_recap` on and ≥ 1 transaction last week, send a recap (total, delta vs prior week, top category) deep-linking to `sprout://stats`.
- Files: `internal/jobs/weekly_recap.go`
- Done when: time-travel tests across three timezones send exactly once per user per week.
- Needs: BE-56, BE-23

### Milestone 12 — Hardening and release

**BE-59 · Rate limits and request hardening**
- Do: Per-user token bucket (default 120 req/min; uploads/imports/receipts 10/min; suggest 60/min) with `Retry-After`; per-IP limit before auth; strict content-type checks; security headers; per-user daily caps on LLM-backed operations (imports 20, receipts 50) returning `rate_limited`.
- Files: `internal/httpx/ratelimit.go`
- Done when: tests exceed each limit and receive `429` with the header.
- Needs: BE-52, BE-48

**BE-60 · Account deletion**
- Do: `DELETE /me`: mark user deleted, enqueue job that removes all rows (FK cascades), all objects under `u/{user_id}/`, and the Auth0 user via the Management API; requests with a deleted user's token get `401`. Global learned dictionary entries are retained (they contain no user data).
- Files: `internal/users/delete.go`, `internal/jobs/delete_account.go`, `internal/auth/management.go`
- Done when: integration test leaves zero rows referencing the user id in any table and zero objects in storage.
- Needs: BE-12, BE-56

**BE-61 · Observability**
- Do: OpenTelemetry tracing (HTTP, pgx, river, LLM calls), Prometheus metrics (request latency by route, job durations/failures, import outcomes by `mapping_source`, categorisation source distribution, LLM tokens/cost), error reporting hook, `/metrics` on an internal port. Dashboards-as-code optional.
- Files: `internal/obs/*`
- Done when: local run shows a trace spanning request → job → LLM; metrics expose the import and categorisation counters.
- Needs: BE-47

**BE-62 · Deployment**
- Do: Multi-stage `Dockerfile` (distroless, non-root; includes any PDF text tooling), release workflow building and pushing the image, migration step before rollout, separate `api` and `worker` services, environment matrix (dev/staging/prod), `docs/runbook.md` (deploy, rollback, rotating APNs/LLM/Auth0 secrets, re-running a stuck import).
- Files: `Dockerfile`, `.github/workflows/release.yml`, `docs/runbook.md`
- Done when: staging runs both services; `/readyz` green; a statement imports end-to-end on staging.
- Needs: BE-59, BE-61

**BE-63 · Contract conformance and demo data**
- Do: Test that walks `api/openapi.yaml` and validates real responses from every endpoint against their schemas. `sprout seed-demo --email=…` creating the exact dataset shown in the design (Daisy: balance $408.55, 7 categories with the sample budgets, month spend $1,842.50, 4 goals) so the iOS app can be compared to the mock-ups against a live API.
- Files: `internal/httpx/contract_test.go`, `cmd/sprout/seed_demo.go`
- Done when: conformance test covers 100 % of operations; demo user's `/home`, `/summary/*`, `/goals` match the design's numbers.
- Needs: all previous

---

## 7. Milestone exit criteria

| Milestone | You can… |
|---|---|
| 1 | Run the API and worker locally with migrations and CI green |
| 2 | Log in with a real Auth0 token and read/update your profile |
| 3–4 | Create categories, budgets and transactions; see balance and Home |
| 5 | Drive Categories, Category detail and Stats for any period |
| 6 | Save toward goals and see pace/ETA |
| 7 | Classify arbitrary merchant strings with measured accuracy |
| 8 | Import a statement from an unseen bank into the ledger |
| 9 | Photograph a receipt and save it as an expense |
| 10 | See generated insight cards |
| 11 | Receive budget alerts and a weekly recap on a device |
| 12 | Deploy to staging/production safely |

## 8. Open questions for the owner

1. Hosting target (Fly.io, Render, AWS, GCP)? Affects BE-62 only.
2. Auth0 email login: passwordless code or password? Affects `auth0-setup.md` only.
3. Is sending masked statement samples and receipt images to a third-party LLM acceptable for the privacy policy, or is a self-hosted model required? The `llm.Client` interface allows either.
4. ~~Supported currencies at launch (the allow-list in BE-11).~~ Answered 2026-10-08: USD, EUR, GBP, JPY, CAD, AUD, AMD.
