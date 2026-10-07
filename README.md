# sprout-api

Go backend for Sprout, an iOS app for tracking spending by category and saving toward goals.

- [docs/PRD-backend.md](docs/PRD-backend.md) — architecture, data model and the task list.
- [docs/TASKS-backend.md](docs/TASKS-backend.md) — progress.
- [docs/api-contract.md](docs/api-contract.md) — the wire format.

## Requirements

- Go 1.22 or newer
- [golangci-lint](https://golangci-lint.run) v1.57 or newer (v1 configuration format)
- Docker, for Postgres and MinIO once they are added in BE-05

## Setup

```bash
git clone git@github.com:movsesmeliksetyan/sprout-api.git
cd sprout-api
make build
./bin/sprout --help
```

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

The `api`, `worker` and `migrate` subcommands are stubs for now and exit with "not implemented yet". They are filled in by later tasks in the PRD, as are code generation and the evaluation set.

## Layout

```
cmd/sprout/     entry point: api, worker, migrate
api/            OpenAPI spec
migrations/     goose SQL migrations
internal/       application packages, one per concern (see PRD §2.1)
testdata/       statement, receipt and categorisation fixtures
docs/           PRD, task tracker, API contract
```
