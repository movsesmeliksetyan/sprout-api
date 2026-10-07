# sprout-api

Go backend for Sprout, an iOS app for tracking spending by category and saving toward goals.

- [docs/PRD-backend.md](docs/PRD-backend.md) — architecture, data model and the task list.
- [docs/TASKS-backend.md](docs/TASKS-backend.md) — progress.
- [docs/api-contract.md](docs/api-contract.md) — the wire format.

## Requirements

- Go 1.27 or newer
- [golangci-lint](https://golangci-lint.run) v2.14 or newer
- Docker, for Postgres and MinIO once they are added in BE-05

## Setup

```bash
git clone git@github.com:movsesmeliksetyan/sprout-api.git
cd sprout-api
cp .env.example .env
make build
./bin/sprout --help
```

## Configuration

Configuration comes from environment variables; [.env.example](.env.example) lists every key. A `.env` file in the working directory is loaded on start-up, and real environment variables win over it.

`sprout api` and `sprout worker` validate the configuration before doing anything else and print one line per missing or invalid key. With `ENV=dev`, `LLM_API_KEY` and the `APNS_*` keys may be left empty; `staging` and `prod` require all of them.

## Commands

Run `make` to list every target.

| Target | What it does |
|---|---|
| `make build` | Build `bin/sprout` |
| `make run` | Run the HTTP API (`sprout api`) |
| `make worker` | Run the job worker (`sprout worker`) |
| `make test` | Run all tests with the race detector |
| `make lint` | Run golangci-lint |
| `make generate` | Regenerate code |
| `make migrate-up` / `make migrate-down` | Apply or roll back migrations (`sprout migrate`) |
| `make eval` | Run the deterministic categorisation evaluation |

The `api`, `worker` and `migrate` subcommands are stubs for now: `api` and `worker` load the configuration and log it (secrets redacted), then all three exit with "not implemented yet". They are filled in by later tasks in the PRD, as are code generation and the evaluation set.

## Layout

```
cmd/sprout/     entry point: api, worker, migrate
api/            OpenAPI spec
migrations/     goose SQL migrations
internal/       application packages, one per concern (see PRD §2.1)
testdata/       statement, receipt and categorisation fixtures
docs/           PRD, task tracker, API contract
```
