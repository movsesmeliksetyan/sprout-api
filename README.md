# sprout-api

Go backend for Sprout, an iOS app for tracking spending by category and saving toward goals.

- [docs/PRD-backend.md](docs/PRD-backend.md) — architecture, data model and the task list.
- [docs/TASKS-backend.md](docs/TASKS-backend.md) — progress.
- [docs/api-contract.md](docs/api-contract.md) — the wire format.

## Requirements

- Go 1.27 or newer
- Docker, for local Postgres and MinIO and for the database tests

## Setup

```bash
git clone git@github.com:movsesmeliksetyan/sprout-api.git
cd sprout-api
cp .env.example .env
docker compose up -d --wait
make migrate-up
make run
```

`docker compose` starts Postgres on `localhost:5432` and MinIO on `localhost:9000` (console on `:9001`, bucket `sprout`), with the credentials in `.env.example`. `docker compose down -v` wipes both.

The API listens on `HTTP_ADDR` (default `:8080`). `GET /healthz` answers while the process is up and `GET /readyz` once Postgres is reachable; the API starts either way. Stop it with Ctrl-C or SIGTERM; in-flight requests are allowed to finish.

## Configuration

Configuration comes from environment variables; [.env.example](.env.example) lists every key. A `.env` file in the working directory is loaded on start-up, and real environment variables win over it.

`sprout api` and `sprout worker` validate the configuration before doing anything else and print one line per missing or invalid key. With `ENV=dev`, `LLM_API_KEY` and the `APNS_*` keys may be left empty; `staging` and `prod` require all of them.

## Commands

Run `make` to list every target.

| Target | What it does |
|---|---|
| `make build` | Build `bin/sprout` |
| `make run` | Run the HTTP API (`sprout api`) until interrupted |
| `make worker` | Run the job worker (`sprout worker`) |
| `make test` | Run all tests with the race detector (needs Docker: database tests start their own Postgres) |
| `make lint` | Run golangci-lint and lint `api/openapi.yaml` (pinned tools; the first run compiles them) |
| `make generate` | Regenerate the sqlc queries in `internal/db` and the OpenAPI server in `internal/httpx/api_gen.go` (pinned tools; nothing to install) |
| `make generate-check` | Fail if the committed generated code is out of date (what CI runs) |
| `make migrate-up` / `make migrate-down` | Apply all pending migrations / roll back the latest one |
| `make eval` | Run the deterministic categorisation evaluation |

The API is described by [api/openapi.yaml](api/openapi.yaml), served at `/v1/openapi.yaml` outside production. Every `/v1` operation exists and answers `501` until its task in the PRD implements it.

`sprout migrate up|down|status` needs only `DATABASE_URL`. The `worker` subcommand is a stub for now and exits with "not implemented yet"; it and the evaluation set are filled in by later tasks in the PRD.

## Layout

```
cmd/sprout/     entry point: api, worker, migrate
api/            OpenAPI spec
migrations/     goose SQL migrations
internal/       application packages, one per concern (see PRD §2.1)
testdata/       statement, receipt and categorisation fixtures
docs/           PRD, task tracker, API contract
```
