.DEFAULT_GOAL := help

BIN := bin/sprout

# Build tools run at a pinned version so generated code is identical everywhere.
SQLC := go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
OAPI_CODEGEN := go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.0
VACUUM := go run github.com/daveshanley/vacuum@v0.30.6
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0

# Paths written by `make generate`.
GENERATED := internal/db internal/httpx/api_gen.go

.PHONY: help build run worker test lint lint-spec generate generate-check check migrate-up migrate-down seed-rules seed perf-notes eval eval-llm eval-receipts

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

check: generate-check lint test ## Everything a change must pass before it is committed (needs Docker)

migrate-up: ## Apply all pending migrations
	go run ./cmd/sprout migrate up

migrate-down: ## Roll back the latest migration
	go run ./cmd/sprout migrate down

seed-rules: ## Load the categorisation seeds (merchants, MCC codes, keywords); safe to run again
	go run ./cmd/sprout seed-rules

seed: ## Fill a development user's ledger with 50,000 generated transactions
	go run ./cmd/sprout seed

perf-notes: ## Measure the read endpoints on the seeded ledger and rewrite docs/perf-notes.md (needs Docker)
	PERF_NOTES=$(CURDIR)/docs/perf-notes.md go test -count=1 -run '^TestAggregationPerformance$$' ./cmd/sprout

eval: ## Run the deterministic categorisation evaluation (part of make test; needs Docker)
	go test -count=1 -v -run '^TestEval(Deterministic|LabelledSet)$$' ./internal/categorize

eval-llm: ## Evaluate the full pipeline with the real model and rewrite docs/categorization-eval.md (needs .env and Docker)
	set -a; . ./.env; set +a; LLM_CATEGORIZE_BATCH=$${LLM_CATEGORIZE_BATCH:-15} EVAL_REPORT=$(CURDIR)/docs/categorization-eval.md \
		go test -tags=live -count=1 -v -timeout 20m -run '^TestEvalLLM$$' ./internal/categorize

eval-receipts: ## Read testdata/receipts with the real vision model and check the expected totals (needs .env)
	set -a; . ./.env; set +a; go test -tags=live -count=1 -v -run '^TestExtractLive$$' ./internal/receipts
