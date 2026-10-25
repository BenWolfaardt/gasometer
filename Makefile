.DEFAULT_GOAL := help
SHELL := /bin/bash

# podman when its machine is up, otherwise docker (Docker Desktop, OrbStack, Colima).
CONTAINER ?= $(shell podman info >/dev/null 2>&1 && echo podman || echo docker)
COMPOSE ?= $(CONTAINER) compose
COMPOSE_FILE := deploy/docker-compose.yml

FUZZTIME ?= 10s
FUZZ_PKGS := $(shell grep -rlE '^func Fuzz' tests/unit | xargs -n1 dirname 2>/dev/null | sort -u | sed 's|^|./|')

TEST_PG_NAME := gasometer-test-pg
TEST_PG_PORT ?= 55432
GASOMETER_TEST_DATABASE_URL ?= postgres://gasometer:gasometer@localhost:$(TEST_PG_PORT)/gasometer?sslmode=disable
export GASOMETER_TEST_DATABASE_URL

MIGRATE := go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@v4.20.1
DATABASE_URL ?= $(GASOMETER_TEST_DATABASE_URL)

.PHONY: help
help: ## List available targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-16s %s\n", $$1, $$2}'

.PHONY: build
build: ## Compile all packages and the gasometer binary into bin/
	go build ./...
	go build -o bin/gasometer ./cmd/gasometer

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test -race ./...

.PHONY: fuzz
fuzz: ## Run every fuzz target for FUZZTIME (default 10s)
	@set -e; for pkg in $(FUZZ_PKGS); do \
		for t in $$(grep -hoE '^func Fuzz[A-Za-z0-9_]+' $$pkg/*_test.go | sed 's/^func //'); do \
			echo "fuzz $$pkg $$t"; \
			go test $$pkg -run '^$$' -fuzz "^$$t$$" -fuzztime $(FUZZTIME); \
		done; \
	done

.PHONY: lint
lint: ## Run go vet and golangci-lint
	go vet ./...
	golangci-lint run ./...

.PHONY: test-db-up
test-db-up: ## Start a throwaway Postgres for integration tests
	$(CONTAINER) run -d --rm --name $(TEST_PG_NAME) -p $(TEST_PG_PORT):5432 \
		-e POSTGRES_USER=gasometer -e POSTGRES_PASSWORD=gasometer -e POSTGRES_DB=gasometer \
		postgres:17-alpine
	@for i in $$(seq 1 30); do \
		$(CONTAINER) exec $(TEST_PG_NAME) pg_isready -U gasometer >/dev/null 2>&1 && exit 0; sleep 1; \
	done; echo "postgres did not become ready" >&2; exit 1

.PHONY: test-db-down
test-db-down: ## Stop the integration test Postgres
	-$(CONTAINER) rm -f $(TEST_PG_NAME)

.PHONY: integration
integration: ## Run integration tests against GASOMETER_TEST_DATABASE_URL (see test-db-up)
	go test -tags integration -count=1 ./tests/integration/...

.PHONY: live
live: ## Replay recent Base and OP headers against the fee model (needs network)
	go test -tags live -count=1 -v ./tests/integration/...

.PHONY: record-fixtures
record-fixtures: ## Re-record the RPC fixtures tests/unit/rpc replays (needs network)
	go test -tags record -count=1 -run TestRecordFixtures ./tests/integration/...

.PHONY: migrate
migrate: ## Apply migrations to DATABASE_URL
	$(MIGRATE) -path internal/infrastructure/repositories/migrations -database "$(DATABASE_URL)" up

.PHONY: compose-up
compose-up: ## Start gasometer, Postgres and Alloy (needs deploy/.env)
	$(COMPOSE) -f $(COMPOSE_FILE) up -d --build

.PHONY: compose-down
compose-down: ## Stop the compose stack, keeping the database volume
	$(COMPOSE) -f $(COMPOSE_FILE) down

.PHONY: pre-commit
pre-commit: ## Run every pre-commit hook over the repo
	pre-commit run --all-files

.PHONY: clean
clean: ## Remove build artefacts
	rm -rf bin
