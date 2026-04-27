.PHONY: build-gateway build-message build-sync migrate-up migrate-down test run-dev run-pre-tunnel

# Local dev: start gateway pointing at pre middleware via Consul KV.
# - Loads im-go/dev/config.yaml from consul-pre (PG/Redis/Pulsar via NodePort)
# - Writes to im_dev database (NOT im_pre — keeps the cluster-deployed pod safe)
# - Sends OTel traces to jaeger-cses
# Override IM_GATEWAY_JWT_SECRET only if you need a non-default JWT secret.
run-dev:
	IM_ENV=dev \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/dev/config.yaml \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	OTEL_SERVICE_NAME=im-gateway-local \
	go run ./cmd/gateway

# Local debugging against the pre cluster's exact config (im_pre database).
# DANGEROUS: hits the same DB the cluster pod uses. Use only when reproducing
# a pre-only bug locally.
run-pre-tunnel:
	IM_ENV=pre \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/pre/config.yaml \
	IM_GATEWAY_JWT_SECRET=$$(kubectl -n im-v2 get secret im-gateway-secret -o jsonpath='{.data.jwt-secret}' | base64 -d) \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	go run ./cmd/gateway


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

.PHONY: test-integration
test-integration:
	gotestsum --format pkgname -- -race -tags=integration -timeout 5m ./...

check: lint vuln test-unit

.PHONY: check-full
check-full: lint vuln test-unit test-integration

.PHONY: mocks
mocks:
	@which mockery > /dev/null || GOTOOLCHAIN=go1.26.2 go install github.com/vektra/mockery/v2@v2.50.0
	@grep -q "^packages:" .mockery.yaml 2>/dev/null && mockery || echo "no packages registered yet — skip"

# -----------------------------------------------------------------------------
# Tiered verification (OVERALL.md §5.1):
#   V1 = build + lint   (fast, no infra)
#   V2 = race-enabled unit tests (no infra)
#   V3 = integration tests (requires docker-compose: pg + redis + pulsar)
#
# `make verify-all` runs V1 + V2. V3 is opt-in via `make verify-integration`.
# -----------------------------------------------------------------------------
.PHONY: verify-all verify-build verify-unit verify-integration

verify-all: verify-build verify-unit
	@echo "V1 + V2 GREEN. integration/smoke/cluster require a running docker-compose."

verify-build:
	@echo "==> V1 build + lint"
	go build ./...
	go vet ./...
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run || echo "(golangci-lint not installed, skipped)"

verify-unit:
	@echo "==> V2 unit tests"
	go test -race -short ./...

verify-integration:
	@echo "==> V3 integration (requires docker-compose running pg+redis+pulsar)"
	@echo "    (V5 group scenarios spin up one Postgres testcontainer per test;"
	@echo "     600s timeout accommodates the container start-up overhead)"
	go test -race -tags=integration -timeout 600s ./...
