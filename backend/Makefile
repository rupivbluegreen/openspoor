# Open Spoor backend. Run `make help`.
GO        ?= go
BIN       := bin/openspoor
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS   := -s -w -X github.com/openspoor/openspoor/internal/app.Version=$(VERSION)
DEV_ENV   := OPENSPOOR_DEV_AUTH=true OPENSPOOR_DATABASE_URL=postgres://openspoor:openspoor@localhost:5432/openspoor?sslmode=disable \
             OPENSPOOR_CORS_ORIGINS=http://localhost:5173,http://localhost:5174,http://localhost:5175

.PHONY: help build test test-unit test-integration bench lint arch-lint vet fmt migrate seed run worker docker cover

help:
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-18s %s\n", $$1, $$2}'

build: ## build the single static binary
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/openspoor

test: ## all tests (integration tests need OPENSPOOR_TEST_* env, see scripts/testdb.sh)
	$(GO) test -race -count=1 ./...

test-unit: ## tests that need no database
	$(GO) test -count=1 ./pkg/... ./internal/serialization/...

test-integration: ## start a throwaway Postgres 16 and run every test
	eval "$$(../scripts/testdb.sh)" && $(GO) test -count=1 ./...

cover: ## coverage report for domain code
	eval "$$(../scripts/testdb.sh)" && $(GO) test -count=1 -coverpkg=./internal/...,./pkg/... -coverprofile=coverage.out ./... && $(GO) tool cover -func=coverage.out | tail -1

bench: ## 100k-event capture + allocation benchmark (writes ../bench-results)
	eval "$$(../scripts/testdb.sh)" && OPENSPOOR_BENCH=1 $(GO) test -count=1 -timeout 30m -run 'TestBench' -v ./internal/... | grep -v ' INFO '
	$(GO) test -run xxx -bench . -benchtime 5x ./internal/serialization/ ./pkg/...

vet:
	$(GO) vet ./...

fmt:
	gofmt -w ./cmd ./internal ./pkg ./migrations

lint: ## golangci-lint
	golangci-lint run ./...

arch-lint: ## module boundary check
	go-arch-lint check

migrate: ## apply migrations to the dev database
	$(DEV_ENV) $(GO) run ./cmd/openspoor migrate

seed: ## load demo data (3 fictional companies, 10k packs)
	$(DEV_ENV) $(GO) run ./cmd/openspoor seed --packs 10000

run: ## api + worker in one process on :8080
	$(DEV_ENV) $(GO) run ./cmd/openspoor all

worker:
	$(DEV_ENV) $(GO) run ./cmd/openspoor worker

docker: ## container image openspoor/openspoor
	docker build -t openspoor/openspoor:$(VERSION) -f ../deploy/Dockerfile ..

validation-pack: ## generate docs/validation/OQ-report.md from a full test run
	eval "$$(../scripts/testdb.sh)" && OPENSPOOR_BENCH=1 OPENSPOOR_BENCH_EVENTS=20000 $(GO) test -count=1 -json ./... | $(GO) run ./cmd/validationpack -trace ../docs/validation/traceability.yaml > ../docs/validation/OQ-report.md
