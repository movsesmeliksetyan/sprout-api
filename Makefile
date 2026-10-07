.DEFAULT_GOAL := help

BIN := bin/sprout

# Build tools run at a pinned version so generated code is identical everywhere.
SQLC := go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
VACUUM := go run github.com/daveshanley/vacuum@v0.30.6
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

# Paths written by `make generate`.
GENERATED := internal/db internal/httpx/api_gen.go

.PHONY: help build run worker test lint lint-spec generate generate-check migrate-up migrate-down eval

help: ## List the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the sprout binary into bin/
	go build -o $(BIN) ./cmd/sprout

run: ## Run the HTTP API
	go run ./cmd/sprout api

worker: ## Run the job worker
	go run ./cmd/sprout worker

test: ## Run all tests with the race detector (needs Docker)
	go test -race ./...

lint: lint-spec ## Run golangci-lint and the OpenAPI linter
	$(GOLANGCI_LINT) run ./...

lint-spec: ## Lint api/openapi.yaml
	$(VACUUM) lint --details --fail-severity error --ruleset api/vacuum.yaml api/openapi.yaml

generate: ## Regenerate code: sqlc queries and the OpenAPI server
	$(SQLC) generate
	$(OAPI_CODEGEN) -config api/codegen.yaml api/openapi.yaml
	go generate ./...

generate-check: generate ## Fail if the committed generated code is out of date
	@status="$$(git status --porcelain -- $(GENERATED))"; \
	if [ -n "$$status" ]; then \
		echo "generated code is stale; run 'make generate' and commit the result:"; \
		echo "$$status"; \
		exit 1; \
	fi

migrate-up: ## Apply all pending migrations
	go run ./cmd/sprout migrate up

migrate-down: ## Roll back the latest migration
	go run ./cmd/sprout migrate down

eval: ## Run the deterministic categorisation evaluation (built in BE-36)
	go test -run '^TestEval' ./internal/categorize/...
