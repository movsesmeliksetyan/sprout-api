# Sprout — API contract (v1)

Shared source of truth for [PRD-backend.md](PRD-backend.md) and [PRD-ui.md](PRD-ui.md). If either side needs a change, change this file first, then both PRDs' affected tasks.

The backend and the iOS app live in separate repositories. The backend repository owns this file (`docs/api-contract.md`); the iOS repository keeps a read-only copy, refreshed whenever the contract changes.

The backend turns this document into `api/openapi.yaml` (task BE-06). After that, the OpenAPI file is the machine-readable form and this file stays the human-readable explanation; they must not disagree.

---

## 1. Conventions

| Topic | Rule |
|---|---|
| Base URL | `https://<host>/v1` (local: `http://localhost:8080/v1`) |
| Format | JSON, UTF-8, `snake_case` keys. Unknown fields are ignored by both sides. |
| Auth | `Authorization: Bearer <Auth0 access token>` on every `/v1` route. Token audience = the Sprout API identifier. |
| IDs | UUID v7 strings. |
| Money | Integer **minor units** in fields suffixed `_minor` (e.g. `$408.55` → `40855`). Amounts are always **positive**; direction comes from `kind`. Net values (day totals, deltas) are signed and say so. One currency per user in v1 (`me.currency`, ISO 4217). |
| Time | Instants: RFC 3339 UTC (`2025-07-21T17:24:00Z`). Calendar days: `YYYY-MM-DD`. Months: `YYYY-MM`. Day grouping uses `me.timezone` (IANA). |
| Pagination | `?limit=` (default 50, max 200) and `?cursor=`. Response: `{ "items": [...], "next_cursor": "..." \| null }`. |
| Idempotency | `Idempotency-Key: <uuid>` accepted on every `POST` that creates ledger data. Same key + same user within 24 h returns the original response. |
| Nulls | Optional fields are present with `null`, not omitted. |

### 1.1 Errors

Every non-2xx response:

```json
{ "error": { "code": "validation_failed", "message": "Amount must be greater than zero.", "fields": { "amount_minor": "must be > 0" }, "request_id": "01J..." } }
```

| HTTP | `code` | Meaning |
|---|---|---|
| 400 | `bad_request` | Malformed JSON / params |
| 401 | `unauthenticated` | Missing, expired or invalid token |
| 403 | `forbidden` | Authenticated but not allowed |
| 404 | `not_found` | Resource missing or not owned by caller |
| 409 | `conflict` | State conflict (e.g. import already committed) |
| 413 | `payload_too_large` | Upload over limit |
| 422 | `validation_failed` | Field errors in `fields` |
| 429 | `rate_limited` | `Retry-After` header set |
| 500 | `internal` | Unexpected |
| 503 | `unavailable` | Dependency down |

`message` is safe to show to the user.

### 1.2 Periods

Used by every summary endpoint: `?period=week|month|year&offset=N`. `period` is required; `offset` ≥ 0 and defaults to `0` (`0` = current period, `1` = previous, …).

| period | Range (user timezone) | Trend / series buckets |
|---|---|---|
| `week` | Monday 00:00 → Sunday 23:59:59 | 7 buckets, one per day |
| `month` | Calendar month | 4 buckets: days 1–7, 8–14, 15–21, 22–end |
| `year` | Calendar year | 12 buckets, one per month |

Every summary response carries:

```json
"range": { "period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true }
```

The client renders labels (`July 2025`, `7–13 Jul`, `2025`, `W1…W4`, `M T W…`, `J F M…`) from `range` and bucket `start` dates.

**Budget scaling.** A category stores one `monthly_budget_minor`. Budget for a period: month = monthly; year = monthly × 12; week = round(monthly × 12 / 52).

**Trend.** `trend_pct` = round((spent − previous_spent) / previous_spent × 100) against the immediately preceding period of the same kind; `null` when previous is 0.

### 1.3 Enums

| Enum | Values |
|---|---|
| `transaction.kind` | `expense`, `income` |
| `transaction.source` | `manual`, `import`, `receipt` |
| `category.icon` | `utensils`, `car`, `house`, `droplet`, `bolt`, `sparkles`, `heart`, `cart`, `bag`, `plane`, `gift`, `book`, `paw`, `phone`, `film`, `dumbbell`, `baby`, `briefcase`, `tag` |
| `category.shade` | integer `0…6` (index into the slate ramp, 0 = lightest) |
| `goal.status` | `active`, `completed`, `archived` |
| `contribution.kind` | `topup`, `withdrawal` |
| `import.status` | `processing`, `needs_mapping`, `ready`, `committed`, `failed` |
| `import_row.kind` | `expense`, `income`, `transfer` |
| `import_row.status` | `ok`, `duplicate`, `error` |
| `categorization.source` | `user_rule`, `dictionary`, `mcc`, `keyword`, `llm`, `none` |
| `receipt.status` | `processing`, `ready`, `failed`, `confirmed` |
| `mapping.role` | `date`, `description`, `amount`, `debit`, `credit`, `balance`, `currency`, `reference`, `mcc`, `ignore` |
| `insight.tone` | `positive`, `warning`, `neutral` |
| `insight.icon` | `sparkle`, `warning`, `chart`, `target` |
| `upload.purpose` | `avatar`, `receipt`, `statement` |

---

## 2. Resources

### 2.1 Me

`GET /me` → `200`

```json
{
  "id": "…", "name": "Daisy Walker", "first_name": "Daisy", "email": "daisy.walker@email.com",
  "avatar_url": "https://…" , "currency": "USD", "timezone": "Europe/London",
  "starting_balance_minor": 0, "onboarding_completed": true,
  "preferences": { "notifications_enabled": true, "budget_alerts": true, "weekly_recap": true },
  "stats": { "categories_count": 7, "goals_count": 4, "streak_days": 128 },
  "created_at": "…"
}
```

The first authenticated call for an unknown Auth0 `sub` creates the user and seeds the 7 default categories (§2.3).

`PATCH` bodies in general: a field left out is unchanged. Where a value can be cleared (`avatar_upload_id`; a transaction's `category_id`, `merchant`, `note`; a goal's `emoji`, `image_upload_id`), sending `null` clears it.

`PATCH /me` — any subset of: `name`, `currency`, `timezone`, `starting_balance_minor`, `onboarding_completed`, `avatar_upload_id` (or `null` to remove), `preferences.{notifications_enabled,budget_alerts,weekly_recap}` → `200` full object.
`currency` can only change while the user has zero transactions (`409 conflict` otherwise).

`DELETE /me` → `204`. Irreversibly deletes all user data and the Auth0 identity.

`streak_days` = consecutive local days, ending today or yesterday, on which the user created at least one transaction or contribution.

### 2.2 Uploads

`POST /uploads`

```json
{ "purpose": "receipt", "content_type": "image/jpeg", "size_bytes": 812345, "filename": "IMG_0042.jpg" }
```

→ `201`

```json
{ "id": "…", "upload_url": "https://…", "method": "PUT", "headers": { "Content-Type": "image/jpeg" }, "expires_at": "…" }
```

The client `PUT`s the bytes to `upload_url`, then passes `id` to the consuming endpoint.

| purpose | Content types | Max |
|---|---|---|
| `avatar` | `image/jpeg`, `image/png`, `image/heic` | 5 MB |
| `receipt` | `image/jpeg`, `image/png`, `image/heic` | 10 MB |
| `statement` | `text/csv`, `application/x-ofx`, `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`, `application/pdf`, `application/octet-stream` | 15 MB |

### 2.3 Categories

```json
{ "id": "…", "name": "Food", "icon": "utensils", "shade": 0, "sort_order": 0, "monthly_budget_minor": 50000, "archived": false }
```

Defaults seeded per user (budgets start at `0`; the sample values from the design are for mocks only):

| sort | name | icon | shade |
|---|---|---|---|
| 0 | Food | `utensils` | 0 |
| 1 | Car | `car` | 1 |
| 2 | Home | `house` | 2 |
| 3 | Leisure | `bolt` | 3 |
| 4 | Self-care | `sparkles` | 4 |
| 5 | Health | `heart` | 5 |
| 6 | Communal | `droplet` | 6 |

(The design bundle shows a house icon on *Communal* and a droplet on *Home*; that is treated as a swap and corrected here.)

| Method | Path | Body | Result |
|---|---|---|---|
| `GET` | `/categories?include_archived=false` | — | `{ "items": [Category] }` ordered by `sort_order` |
| `POST` | `/categories` | `name`, `icon`, `shade?`, `monthly_budget_minor?` | `201` Category |
| `PATCH` | `/categories/{id}` | any of `name`, `icon`, `shade`, `monthly_budget_minor`, `archived` | `200` Category |
| `DELETE` | `/categories/{id}` | — | `204` if it has no transactions; otherwise `409` (client archives instead) |
| `PUT` | `/categories/order` | `{ "ids": ["…"] }` (all non-archived ids) | `200` `{ "items": [...] }` |
| `PUT` | `/budgets` | `{ "items": [{ "category_id": "…", "monthly_budget_minor": 50000 }] }` | `200` `{ "items": [Category] }` |

Limits: name 1–24 chars, unique per user (case-insensitive); max 30 active categories.

### 2.4 Transactions

```json
{
  "id": "…", "kind": "expense", "amount_minor": 2440, "category_id": "…",
  "merchant": "Tesco", "note": "Groceries",
  "occurred_at": "2025-07-21T11:02:00Z", "local_date": "2025-07-21",
  "source": "manual", "receipt_id": null, "import_id": null, "created_at": "…"
}
```

- `expense` requires `category_id`; `income` has `category_id: null`.
- `merchant` and `note` are optional (≤ 80 / ≤ 140 chars).
- Display rule (client): title = `merchant` ?? category name ?? "Income"; subtitle = `note` ?? category name.

| Method | Path | Notes |
|---|---|---|
| `POST` | `/transactions` | Body: `kind`, `amount_minor` (> 0), `category_id?`, `merchant?`, `note?`, `occurred_at?` (default now). `201`. Idempotent. |
| `GET` | `/transactions/{id}` | |
| `PATCH` | `/transactions/{id}` | Same fields as create. |
| `DELETE` | `/transactions/{id}` | `204` |
| `GET` | `/transactions` | Filters: `from`, `to` (dates, inclusive), `category_id`, `kind`, `q` (merchant/note contains). Newest first. |

List response:

```json
{
  "items": [Transaction],
  "days": [ { "date": "2025-07-21", "net_minor": -2440, "count": 2 } ],
  "next_cursor": null
}
```

`days` holds the **full-day** net total (income − expense) and count under the same filters for every date that appears in `items`, so day headers are correct even at page boundaries.

### 2.5 Home

`GET /home` → `200`

```json
{
  "balance_minor": 40855,
  "month": {
    "range": { "period": "month", "offset": 0, "start": "2025-07-01", "end": "2025-07-31", "is_current": true },
    "spent_minor": 184250, "previous_spent_minor": 205250, "delta_minor": -21000,
    "categories_count": 7,
    "segments": [ { "category_id": "…", "spent_minor": 48250, "share": 0.26 } ]
  },
  "recent": { "items": [Transaction], "days": [ { "date": "2025-07-21", "net_minor": -2440, "count": 2 } ] },
  "has_unread_notifications": true
}
```

- `balance_minor` = starting balance + Σ income − Σ expense − Σ top-ups + Σ withdrawals (may be negative).
- `segments`: categories with spend this month, largest first; `share` sums to 1.
- `recent`: the 10 newest transactions.

### 2.6 Summaries

All take `?period=&offset=`.

`GET /summary/categories`

```json
{
  "range": { },
  "total_spent_minor": 184250, "total_budget_minor": 197000, "budget_used_pct": 94,
  "items": [ {
    "category_id": "…", "spent_minor": 48250, "budget_minor": 50000, "remaining_minor": 1750,
    "over_budget": false, "budget_used_pct": 97, "share_pct": 26, "txn_count": 24, "trend_pct": -8
  } ]
}
```

Items cover every active category, sorted by `spent_minor` desc. `remaining_minor` is negative when over budget. `budget_used_pct` is `null` when the budget is 0.

`GET /summary/categories/{id}`

```json
{
  "range": { }, "category_id": "…",
  "spent_minor": 48250, "budget_minor": 50000, "remaining_minor": 1750, "over_budget": false,
  "budget_used_pct": 97, "txn_count": 24,
  "trend": {
    "unit": "week",
    "buckets": [ { "start": "2025-07-01", "end": "2025-07-07", "amount_minor": 11800 } ],
    "average_minor": 12050, "peak_minor": 14200, "peak_index": 1
  }
}
```

`trend.unit` is `day` (week), `week` (month) or `month` (year). The transaction list for this screen is `GET /transactions?category_id=&from=&to=`.

`GET /summary/stats`

```json
{
  "range": { },
  "total_spent_minor": 184250, "previous_total_minor": 205250, "delta_minor": -21000, "delta_pct": -10,
  "series": { "unit": "week", "buckets": [ { "start": "…", "end": "…", "amount_minor": 41000 } ], "peak_index": 3 },
  "breakdown": [ { "category_id": "…", "amount_minor": 48250, "share_pct": 26 }, { "category_id": null, "amount_minor": 27000, "share_pct": 15 } ]
}
```

`breakdown`: top 5 categories by spend, then one `category_id: null` row ("Other") aggregating the rest (omitted if empty).

### 2.7 Insights

`GET /insights?period=&offset=` → `{ "items": [Insight] }` (max 5, most relevant first)

```json
{ "id": "…", "kind": "category_over_budget", "tone": "warning", "icon": "warning",
  "title": "Self-care is over budget",
  "body": "$10 above your $150 limit. Most of it was one $90 spa visit on the 12th.",
  "category_id": "…", "goal_id": null, "created_at": "…" }
```

`title`/`body` are server-rendered English strings. `kind` ∈ `category_spend_down`, `category_spend_up`, `category_over_budget`, `category_near_budget`, `peak_weekday`, `total_spend_down`, `total_spend_up`, `goal_on_track`, `goal_almost_there`, `no_data`.

### 2.8 Goals

```json
{ "id": "…", "title": "Labubu", "emoji": "🧸", "image_url": null,
  "target_minor": 12000, "saved_minor": 6800, "remaining_minor": 5200, "pct": 57,
  "monthly_pace_minor": 2500, "eta_month": "2025-09", "status": "active", "sort_order": 0, "created_at": "…" }
```

- `monthly_pace_minor`: average net contributions per month over the last 90 days (or since creation if younger); `0` if none.
- `eta_month`: month the goal completes at the current pace; `null` when pace is 0 or the goal is complete.

| Method | Path | Notes |
|---|---|---|
| `GET` | `/goals` | `{ "total_saved_minor", "total_target_minor", "pct", "items": [Goal] }` (active + completed) |
| `POST` | `/goals` | `title` (1–40), `emoji?`, `image_upload_id?`, `target_minor` (> 0) |
| `GET` | `/goals/{id}` | Goal + `"recent_contributions": [Contribution]` (5 newest) |
| `PATCH` | `/goals/{id}` | `title`, `emoji`, `image_upload_id`, `target_minor`, `status`, `sort_order` |
| `DELETE` | `/goals/{id}` | `204`; its contributions are removed and the money returns to balance |
| `GET` | `/goals/{id}/contributions` | Paginated |
| `POST` | `/goals/{id}/contributions` | `{ "kind": "topup", "amount_minor": 2500, "occurred_at"? }` → `201` `{ "contribution": Contribution, "goal": Goal }`. Idempotent. Withdrawal cannot exceed `saved_minor`. |
| `DELETE` | `/goals/{id}/contributions/{cid}` | `200` Goal |

Contribution: `{ "id", "kind", "amount_minor", "occurred_at", "local_date" }`. A goal becomes `completed` automatically when `saved_minor ≥ target_minor`.

### 2.9 Categorisation

`POST /categorization/suggest` — `{ "merchant": "Tesco", "note": null, "amount_minor": 2440 }` → `200`

```json
{ "category_id": "…", "confidence": 0.93, "source": "user_rule" }
```

`category_id` is `null` when nothing clears the confidence threshold. Deterministic layers only; never blocks on an LLM call.

### 2.10 Statement imports

Flow: `POST /uploads` (purpose `statement`) → `PUT` file → `POST /imports` → poll `GET /imports/{id}` → (`needs_mapping` → `PUT …/mapping` → poll) → `ready` → review rows → `POST …/commit`.

`POST /imports` — `{ "upload_id": "…", "period": "month" }` (`period` ∈ `week|month|year|all`, the window of transactions to keep) → `202` Import.

`GET /imports/{id}`

```json
{
  "id": "…", "status": "ready", "filename": "statement.csv", "period": "month",
  "detected": { "format": "csv", "bank_hint": "Monzo", "mapping_source": "template" },
  "summary": { "row_count": 7, "included_count": 7, "total_expense_minor": 31169, "total_income_minor": 0,
               "duplicate_count": 0, "uncategorized_count": 1, "error_count": 0, "out_of_period_count": 12 },
  "mapping_request": null,
  "failure": null,
  "created_at": "…"
}
```

- Poll every 1.5 s while `processing` (server target: < 20 s for 1,000 rows).
- `failure`: `{ "code": "unsupported_format" | "empty_file" | "unreadable" | "too_many_rows" | "internal", "message": "…" }`.
- While `processing`, `detected` and `summary` are still present: each field of `detected` is `null` until known, and every count in `summary` is `0`. `filename` is `null` if the upload had none.
- `detected.mapping_source` ∈ `template`, `heuristic`, `llm`, `user`, `native` (OFX).

When `status = needs_mapping`:

```json
"mapping_request": {
  "columns": [ { "index": 0, "header": "Buchungstag", "samples": ["14.07.2025", "13.07.2025"], "suggested_role": "date" } ],
  "suggested": { "date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense" }
}
```

`PUT /imports/{id}/mapping`

```json
{ "columns": [ { "index": 0, "role": "date" }, { "index": 3, "role": "description" }, { "index": 5, "role": "amount" } ],
  "date_format": "DD.MM.YYYY", "amount_sign": "negative_is_expense" }
```

→ `202` Import (`processing`). Requires exactly one `date`, at least one `description`, and either one `amount` or a `debit`/`credit` pair. `date_format` tokens: `DD`, `MM`, `YYYY`, `YY`, `MMM`. `amount_sign` ∈ `negative_is_expense`, `positive_is_expense`.

`GET /imports/{id}/rows?cursor=&limit=` — ordered: uncategorised first, then newest first.

```json
{ "id": "…", "status": "ok", "included": true, "kind": "expense", "amount_minor": 4280,
  "occurred_at": "2025-07-14T00:00:00Z", "local_date": "2025-07-14",
  "merchant": "Tesco", "raw_description": "TESCO STORES 3297 LONDON GB",
  "category_id": "…", "category_confidence": 0.97, "category_source": "dictionary",
  "duplicate_of": null, "error": null }
```

- `duplicate` rows and `transfer` rows default to `included: false`.
- `error` rows cannot be included; on them `kind`, `amount_minor`, `occurred_at`, `local_date` and `category_source` may be `null`.

`PATCH /imports/{id}/rows/{row_id}` — any of `category_id`, `included`, `kind`, `merchant` → `200` row. Changing `category_id` teaches the categoriser (user rule).

`POST /imports/{id}/commit` → `200` `{ "created_count": 7, "skipped_count": 0 }`. Idempotent. Included expense rows must have a category (`422` listing offending row ids otherwise). After commit the import is read-only.

`DELETE /imports/{id}` → `204` (discard; not allowed after commit).

### 2.11 Receipts

Flow: `POST /uploads` (purpose `receipt`) → `PUT` image → `POST /receipts` → poll `GET /receipts/{id}` → `POST …/confirm`.

`POST /receipts` — `{ "upload_id": "…" }` → `202` Receipt.

`GET /receipts/{id}`

```json
{ "id": "…", "status": "ready", "image_url": "https://…",
  "merchant": "Tesco", "purchased_at": "2025-07-14T18:24:00Z",
  "total_minor": 4280, "currency": "USD", "payment_method": "Visa ·· 4821",
  "category_id": "…", "category_confidence": 0.95,
  "line_items": [ { "name": "Semi-skimmed milk 2L", "quantity": 1, "amount_minor": 185 } ],
  "transaction_id": null, "failure": null }
```

Poll every 1 s (server target: < 8 s). `failure.code` ∈ `not_a_receipt`, `unreadable`, `internal`. Any extracted field may be `null`; the client lets the user fill it in.

`POST /receipts/{id}/confirm` — `{ "amount_minor", "category_id", "occurred_at", "merchant"?, "note"? }` → `201` Transaction (`source: "receipt"`). Idempotent.

`DELETE /receipts/{id}` → `204`.

### 2.12 Devices & notifications

`PUT /devices/{device_id}` — `{ "apns_token": "…", "environment": "sandbox" | "production", "app_version": "1.0.0", "locale": "en_GB" }` → `204`. `device_id` is a client-generated UUID kept in the Keychain.
`DELETE /devices/{device_id}` → `204` (on logout).

`GET /notifications?cursor=` →

```json
{ "items": [ { "id": "…", "kind": "budget_alert", "title": "Self-care is over budget", "body": "You're $10 over your $150 limit.",
               "deep_link": "sprout://category/…", "read_at": null, "created_at": "…" } ], "next_cursor": null }
```

`POST /notifications/read` — `{ "ids": ["…"] }` or `{ "all": true }` → `204`.

`kind` ∈ `budget_alert` (80 % and 100 % of a monthly budget), `weekly_recap`, `goal_completed`, `import_ready`.

Deep links: `sprout://home`, `sprout://stats`, `sprout://category/{id}`, `sprout://goal/{id}`, `sprout://import/{id}`. APNs payloads carry `deep_link` and `notification_id` in custom data.

### 2.13 Health (unauthenticated, outside `/v1`)

`GET /healthz` → `200` when the process is up. `GET /readyz` → `200` when Postgres and object storage are reachable.

---

## 3. Traceability

| Endpoint(s) | Backend task | iOS task (API wiring) | iOS screens |
|---|---|---|---|
| `/healthz`, `/readyz` | BE-03 | — | — |
| `GET /me` | BE-10, BE-20 | UI-51 | UI-32, UI-23 |
| `PATCH /me` | BE-11, BE-12 | UI-51 | UI-40, UI-44, UI-32 |
| `DELETE /me` | BE-60 | UI-51 | UI-47 |
| `POST /uploads` | BE-12 | UI-53 | UI-36, UI-38, UI-44 |
| `/categories*` | BE-14, BE-15 | UI-51 | UI-24, UI-41 |
| `PUT /budgets` | BE-16 | UI-51 | UI-42 |
| `/transactions*` | BE-17, BE-18 | UI-51 | UI-23, UI-25, UI-26, UI-34 |
| `GET /home` | BE-19 | UI-51 | UI-23 |
| `GET /summary/categories` | BE-21 | UI-52 | UI-24 |
| `GET /summary/categories/{id}` | BE-22 | UI-52 | UI-25 |
| `GET /summary/stats` | BE-23 | UI-52 | UI-27 |
| `GET /insights` | BE-53, BE-54 | UI-52 | UI-28 |
| `/goals*`, contributions | BE-25, BE-26, BE-27 | UI-52 | UI-29, UI-30, UI-31, UI-43 |
| `POST /categorization/suggest` | BE-35 | UI-53 | UI-34 |
| `/imports*` | BE-37, BE-44, BE-47, BE-48 | UI-53 | UI-35, UI-36, UI-46 |
| `/receipts*` | BE-50, BE-51, BE-52 | UI-53 | UI-37, UI-38 |
| `/devices/*` | BE-55 | UI-57 | — |
| `/notifications*` | BE-56 | UI-57 | UI-45 |
