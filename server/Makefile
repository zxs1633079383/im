.PHONY: build-gateway build-message build-sync build-all migrate-up migrate-down test \
        run-dev run-pre-tunnel run-message-dev run-sync-stub run-all-dev run-all-dev-windows \
        run-all-dev-with-sync run-all-dev-windows-with-sync

# Local dev: start gateway pointing at pre middleware via Consul KV.
# - Loads im-go/dev/config.yaml from consul-pre (PG/Redis/Pulsar via NodePort)
# - Writes to im_dev database (NOT im_pre — keeps the cluster-deployed pod safe)
# - Sends OTel traces to jaeger-cses
# Override IM_GATEWAY_JWT_SECRET only if you need a non-default JWT secret.
run-dev:
	IM_ENV=dev \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/dev/config.yaml \
	IM_REDIS_CLUSTER=true \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	OTEL_SERVICE_NAME=im-gateway-local \
	go run ./cmd/gateway
# IM_REDIS_CLUSTER=true 必须强制 override：consul-pre 的 im-go/dev/config.yaml 里
# cluster: false，但实际 pre Redis 是 Cluster 部署。不 override 的话 go-redis 跑
# single client 模式连 cluster headless DNS 拿不到 UserData:<id> → 鉴权全 401。
# 详见 harness/C010 §4.6 + docs/apifox-sync/01-auth-and-sync.md §6 troubleshoot。

# Local debugging against the pre cluster's exact config (im_pre database).
# DANGEROUS: hits the same DB the cluster pod uses. Use only when reproducing
# a pre-only bug locally.
run-pre-tunnel:
	IM_ENV=pre \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/pre/config.yaml \
	IM_REDIS_CLUSTER=true \
	IM_GATEWAY_JWT_SECRET=$$(kubectl -n im-v2 get secret im-gateway-secret -o jsonpath='{.data.jwt-secret}' | base64 -d) \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	go run ./cmd/gateway

# message: Pulsar consumer (no HTTP listen). 仅当本机需要消费 im_dev incoming-message
# topic、把 message 写库 + 触发 push 时启动。配置同 run-dev。
run-message-dev:
	IM_ENV=dev \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/dev/config.yaml \
	IM_REDIS_CLUSTER=true \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	OTEL_SERVICE_NAME=im-message-local \
	go run ./cmd/message

# sync-stub: 仅启动 cmd/sync/main.go 16 行预留入口，**会立即退出**。
# sync 业务功能（POST /api/sync）实际在 gateway 进程里，run-dev 已经覆盖。
# 这条 target 只用来验证 cmd/sync binary 可编译——不要把它当成"启动 sync 服务"。
run-sync-stub:
	IM_ENV=dev \
	IM_CONSUL_URL=http://consul-pre.jinqidongli.com \
	IM_CONSUL_KEY=im-go/dev/config.yaml \
	IM_REDIS_CLUSTER=true \
	OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317 \
	OTEL_EXPORTER_OTLP_INSECURE=true \
	OTEL_SERVICE_NAME=im-sync-local \
	go run ./cmd/sync

# 全栈一键启 —— 单窗口聚合 gateway + message 两个真实服务，
# ANSI 颜色 + 前缀 tag 区分。Ctrl+C 整体退。
# 同时写一份到 server/logs/dev/{gateway,message}.log 便于事后 grep。
# sync 业务在 gateway 进程内，不需要额外启。
run-all-dev:
	./scripts/run-all-dev.sh aggregated

# 同上，但额外启 sync stub（cmd/sync 预留入口，会立即退出）。仅用于演示 / 验证编译。
run-all-dev-with-sync:
	./scripts/run-all-dev.sh aggregated --with-sync-stub

# 多窗口模式（优先 tmux session 'im-dev'，回退 macOS Terminal.app）。
# 用 tmux attach -t im-dev 进入；Ctrl+B 0/1 切 gateway/message 窗口；
# tmux kill-session -t im-dev 整体停。
run-all-dev-windows:
	./scripts/run-all-dev.sh windows

# 同上 + sync stub 第三个窗口。
run-all-dev-windows-with-sync:
	./scripts/run-all-dev.sh windows --with-sync-stub


build-gateway:
	go build -o bin/gateway ./cmd/gateway

build-message:
	go build -o bin/message ./cmd/message

build-sync:
	go build -o bin/sync ./cmd/sync

build-all: build-gateway build-message build-sync


DATABASE_URL := $(shell yq e '.pg.dsn' config.yaml)


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
.PHONY: verify-all verify-build verify-unit verify-integration check-handler-coverage

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

# C008 §4.1/§4.3: route + test count gate.  Runs first so a regression on
# the handler/test surface fails loudly before we burn 10+ min on testcontainer
# setup. Bump MIN_ROUTES / MIN_TESTS in scripts/check-handler-coverage.sh as
# new endpoints + tests land.
check-handler-coverage:
	@echo "==> C008 handler-coverage gate"
	@./scripts/check-handler-coverage.sh

verify-integration: check-handler-coverage
	@echo "==> V3 integration (requires docker / testcontainers)"
	@echo "    (each test spins up one Postgres + Redis testcontainer pair;"
	@echo "     45m timeout accommodates the full Batch-A/B/C/D/E surface)"
	go test -race -tags=integration -timeout 45m ./...
