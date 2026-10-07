.DEFAULT_GOAL := help

BIN := bin/sprout

# Build tools run at a pinned version so generated code is identical everywhere.
SQLC := go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1

.PHONY: help build run worker test lint generate migrate-up migrate-down eval

help: ## List the available targets
	@awk 'BEGIN {FS = ":.*## "} /^[a-z-]+:.*## / {printf "  %-14s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## Build the sprout binary into bin/
	go build -o $(BIN) ./cmd/sprout

run: ## Run the HTTP API
	go run ./cmd/sprout api

worker: ## Run the job worker
	go run ./cmd/sprout worker

test: ## Run all tests with the race detector
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

generate: ## Regenerate code: sqlc queries (oapi-codegen is wired in BE-06)
	$(SQLC) generate
	go generate ./...

migrate-up: ## Apply all pending migrations
	go run ./cmd/sprout migrate up

migrate-down: ## Roll back the latest migration
	go run ./cmd/sprout migrate down

eval: ## Run the deterministic categorisation evaluation (built in BE-36)
	go test -run '^TestEval' ./internal/categorize/...
