.PHONY: build-gateway build-message build-sync migrate-up migrate-down test

build-gateway:
	go build -o bin/gateway ./cmd/gateway

build-message:
	go build -o bin/message ./cmd/message

build-sync:
	go build -o bin/sync ./cmd/sync

build-all: build-gateway build-message build-sync

migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)

.PHONY: test test-unit test-cover
test: test-unit

test-unit:
	gotestsum --format pkgname -- -race -short -timeout 60s ./...

test-cover:
	gotestsum --format pkgname -- -race -short -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

.DEFAULT_GOAL := build-all

.PHONY: lint
lint:
	@which golangci-lint > /dev/null || (echo "install: brew install golangci-lint" && exit 1)
	golangci-lint run ./...

.PHONY: vuln check
vuln:
	@which govulncheck > /dev/null || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

check: lint vuln test-unit
