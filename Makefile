GOFUMPT_VERSION := v0.11.0
SQLC_VERSION    := v1.31.1
GOOSE_VERSION   := v3.29.0

.PHONY: hooks run build test lint fmt generate sqlc-check migrate-status docker parity

hooks: ## install the gofumpt + lint + check-all pre-commit hook
	git config core.hooksPath .githooks
	chmod +x .githooks/pre-commit

run:
	go run ./cmd/api serve

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/norviq-api ./cmd/api

test:
	go test ./... -count=1 -race

lint:
	golangci-lint run

fmt:
	go run mvdan.cc/gofumpt@$(GOFUMPT_VERSION) -w .

generate: ## regenerate sqlc config + code from db/migrations and db/queries/<domain>/
	scripts/gen-sqlc-config.sh
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

sqlc-check: ## fail when committed sqlc output is stale
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) diff

migrate-status:
	go run ./cmd/api migrate status

docker:
	docker build -t norviq-backend-v2:dev .

check-fmt: ## Format validation — list files with wrong formatting
	@bad=$$(find . -name '*.go' -not -path './vendor/*' | xargs gofmt -l); \
	if [ -n "$$bad" ]; then \
		echo "The following files need gofmt:"; echo "$$bad"; exit 1; \
	fi
	@echo "gofmt: OK"

vet: ## Built-in static analysis
	go vet ./...

staticcheck: ## Advanced static analysis (requires staticcheck)
	staticcheck ./...

test-race: ## Unit tests with race detection (×10)
	go test -race -count=10 -timeout 30m ./...

check-all: check-fmt vet staticcheck test-race ## Run all checks (fmt, vet, staticcheck, tests)