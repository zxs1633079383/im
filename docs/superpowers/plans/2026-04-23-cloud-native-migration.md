# IM 云原生改造迁移计划（Gin + GORM + testify/testcontainers + OTel）

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 `im-server` 从 `net/http` + 手写 `pgx` SQL + shell 集成测试，**渐进**迁移到 Gin + GORM + testify/mockery/testcontainers-go/httpexpect 测试栈，并在 HTTP / DB / WebSocket / Pulsar 全链路接入 OpenTelemetry trace + metrics。**全程保证生产可运行**。

**Architecture:** 两段式迁移：
- **数据层一次性切换**：Phase 5 把 `pgx + 手写 SQL` 全量替换为 `GORM`，`store/` 重写为 `repo/` 包，pgx 依赖移除（**GORM 与 pgx 不共存**）。所有现有 `net/http` handler 同步切到新 repo，业务行为完全不变。
- **HTTP 层渐进切换**：Gin 引擎包裹现有 `net/http.ServeMux`，旧 handler 通过 `gin.WrapH` 透传，新 Gin handler 按"端点切片"逐个上线，每片完成 service + handler + 测试 + OTel 接入并立即删除对应旧 handler。
- **WebSocket** 不换库（保留 `gorilla/websocket`），只增量埋 OTel span。
- **Pulsar** 生产/消费两端注入/提取 trace context，打通跨服务 trace。

**Tech Stack:**
- Web：Gin + `otelgin` + `validator/v10`
- ORM：GORM v2 + `gorm.io/plugin/opentelemetry/tracing` + `gorm.io/driver/postgres`
- 测试：testify + mockery + testcontainers-go + httpexpect + gotestsum + go-cover-treemap
- 质量：golangci-lint + govulncheck
- 可观测：OpenTelemetry SDK（OTLP gRPC）+ Jaeger + Prometheus（via Collector）+ trace-aware slog
- **不变**：PostgreSQL 16 / Redis / Pulsar / golang-migrate / `gorilla/websocket` / JWT / 现有 `auth/`、`config/`、`pulsar/`、`model/` 包

**Module path:** `im-server`（用于本计划所有 import 路径）

---

## 迁移策略（必读）

**核心原则**：每个 commit 后线上必须可用，旧路径与新路径并存到该端点完全切换。

**阶段分组**：
1. **Phase 0–1：质量门 + 依赖**（不动业务，安全 PR）
2. **Phase 2–3：可观测性 SDK + 测试基础设施**（不动业务）
3. **Phase 4：Gin 共存外壳**（不动业务）
4. **Phase 5：pgx → GORM 全量切换**（一次性，业务行为不变；pgx 完全移除）
5. **Phase 6：Auth 切片完整 Gin TDD 模板**（建立可复用模式）
6. **Phase 7：剩余 8 个切片路线图**（每片独立子计划，按 Auth 模板执行）
7. **Phase 8：WebSocket + Pulsar OTel 跨服务 trace 闭环**
8. **Phase 9：清理 + 文档**

每完成一个端点切片，旧 handler / 旧 store 立即删除，避免双轨永久化。

---

## File Structure（迁移前 → 迁移后）

```
迁移前                                  迁移后
server/internal/                        server/internal/
├── handler/      (net/http)            ├── http/         (Gin handlers — 新)
│   ├── auth.go                         │   ├── router.go
│   ├── channel.go                      │   ├── auth.go
│   └── ...                             │   ├── channel.go
├── store/        (pgx + raw SQL)       │   └── ...
│   ├── pg.go                           ├── service/      (业务逻辑层 — 新)
│   ├── user.go                         │   ├── auth.go
│   └── ...                             │   └── ...
├── middleware/   (net/http)            ├── repo/         (GORM — 新)
│   └── auth.go                         │   ├── db.go
├── gateway/      (WebSocket — 不变)    │   ├── models.go
├── pulsar/       (+ OTel)              │   ├── user.go
├── auth/         (不变)                │   ├── mocks/    (mockery 生成)
├── config/       (不变)                │   └── ...
└── testutil/                           ├── middleware/   (Gin middleware — 新)
                                        │   └── jwt_gin.go
                                        ├── observability/ (OTel SDK 初始化 — 新)
                                        ├── gateway/      (WS + 加 OTel span)
                                        ├── pulsar/       (+ OTel context 传播)
                                        ├── auth/
                                        ├── config/
                                        └── testutil/
                                            └── containers/ (testcontainers helpers — 新)

server/tests/                          server/tests/
├── sync_test.sh                       ├── integration/   (Go 集成测试 — 新)
                                       │   ├── auth_test.go
                                       │   ├── channel_test.go
                                       │   └── ...
                                       └── (sync_test.sh 删除)
```

---

## Phase 0：CI 质量门

### Task 0.1：添加 golangci-lint 配置

**Files:**
- Create: `server/.golangci.yml`
- Modify: `server/Makefile`

- [ ] **Step 1：写 `.golangci.yml`**

```yaml
# server/.golangci.yml
run:
  timeout: 5m
  go: "1.26"
linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - gocritic
    - revive
    - gosec
    - misspell
    - unconvert
    - unparam
    - bodyclose
    - rowserrcheck
issues:
  exclude-rules:
    - path: _test\.go
      linters: [gosec, errcheck, unparam]
    - path: internal/repo/mocks/
      linters: [unused, gocritic, revive]
```

- [ ] **Step 2：在 `server/Makefile` 追加 lint target**

```makefile
.PHONY: lint
lint:
	@which golangci-lint > /dev/null || (echo "install: brew install golangci-lint" && exit 1)
	golangci-lint run ./...
```

- [ ] **Step 3：跑一次记录基线**

```bash
cd server && make lint 2>&1 | tee .lint-baseline.txt || true
```

预期：可能数十/上百条历史警告，记录数量。后续 PR 不允许回归。

- [ ] **Step 4：Commit**

```bash
git add server/.golangci.yml server/Makefile server/.lint-baseline.txt
git commit -m "ci: add golangci-lint config and baseline"
```

### Task 0.2：替换 test target 为 gotestsum + 覆盖率

**Files:**
- Modify: `server/Makefile`
- Modify: `server/.gitignore`（追加 `coverage.out` `coverage.html`）

- [ ] **Step 1：安装 gotestsum**

```bash
go install gotest.tools/gotestsum@latest
```

- [ ] **Step 2：把现有 `test:` 与 `test-short:` 替换为**

```makefile
.PHONY: test test-unit test-cover
test: test-unit

test-unit:
	gotestsum --format pkgname -- -race -short -timeout 60s ./...

test-cover:
	gotestsum --format pkgname -- -race -short -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"
```

- [ ] **Step 3：跑一次确认全绿**

```bash
cd server && make test-unit
```

预期：现有所有 `_test.go` 通过；如失败先修复再继续。

- [ ] **Step 4：Commit**

```bash
git add server/Makefile server/.gitignore
git commit -m "ci: switch to gotestsum and add coverage target"
```

### Task 0.3：添加 govulncheck

**Files:**
- Modify: `server/Makefile`

- [ ] **Step 1：加 vuln + check 聚合**

```makefile
.PHONY: vuln check
vuln:
	@which govulncheck > /dev/null || go install golang.org/x/vuln/cmd/govulncheck@latest
	govulncheck ./...

check: lint vuln test-unit
```

- [ ] **Step 2：运行验证**

```bash
cd server && make vuln
```

预期：列出所有已知 CVE 与受影响代码路径；高危的立即升级依赖（独立 commit）。

- [ ] **Step 3：Commit**

```bash
git add server/Makefile
git commit -m "ci: add govulncheck and check aggregator"
```

### Task 0.4：GitHub Actions 集成

**Files:**
- Create: `.github/workflows/ci.yml`

- [ ] **Step 1：写 workflow**

```yaml
name: CI
on: [push, pull_request]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.26', cache-dependency-path: server/go.sum }
      - uses: golangci/golangci-lint-action@v6
        with: { working-directory: server, version: v1.62 }
      - run: cd server && go install gotest.tools/gotestsum@latest
      - run: cd server && go install golang.org/x/vuln/cmd/govulncheck@latest
      - run: cd server && make vuln
      - run: cd server && make test-cover
      - uses: codecov/codecov-action@v4
        with: { files: server/coverage.out }
        if: always()
```

- [ ] **Step 2：Commit**

```bash
git add .github/workflows/ci.yml
git commit -m "ci: add GitHub Actions workflow"
```

---

## Phase 1：依赖与 Compose 基础

### Task 1.1：添加新依赖到 go.mod

**Files:**
- Modify: `server/go.mod`、`server/go.sum`

- [ ] **Step 1：批量 go get**

```bash
cd server
go get github.com/gin-gonic/gin@v1.10.1
go get gorm.io/gorm@v1.30.1
go get gorm.io/driver/postgres@v1.6.0
go get gorm.io/plugin/opentelemetry@latest
go get github.com/stretchr/testify@v1.10.0
go get github.com/testcontainers/testcontainers-go@v0.34.0
go get github.com/testcontainers/testcontainers-go/modules/postgres@v0.34.0
go get github.com/testcontainers/testcontainers-go/modules/redis@v0.34.0
go get github.com/gavv/httpexpect/v2@v2.16.0
go get go.opentelemetry.io/otel@v1.32.0
go get go.opentelemetry.io/otel/sdk@v1.32.0
go get go.opentelemetry.io/otel/sdk/metric@v1.32.0
go get go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc@v1.32.0
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc@v1.32.0
go get go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin@v0.57.0
go get go.opentelemetry.io/contrib/instrumentation/runtime@v0.57.0
go mod tidy
```

- [ ] **Step 2：验证编译**

```bash
cd server && go build ./...
```

预期：编译通过（依赖已下载，暂无新代码使用）。

- [ ] **Step 3：Commit**

```bash
git add server/go.mod server/go.sum
git commit -m "deps: add gin/gorm/testify/testcontainers/otel"
```

### Task 1.2：mockery 配置

**Files:**
- Create: `server/.mockery.yaml`
- Modify: `server/Makefile`

- [ ] **Step 1：安装 CLI**

```bash
go install github.com/vektra/mockery/v2@v2.50.0
```

- [ ] **Step 2：写空配置**

```yaml
# server/.mockery.yaml
with-expecter: true
quiet: false
disable-version-string: true
mockname: "{{.InterfaceName}}Mock"
filename: "{{.InterfaceName | snakecase}}_mock.go"
dir: "{{.InterfaceDir}}/mocks"
outpkg: mocks
packages:
  # 后续切片在此注册接口；保留示例：
  # im-server/internal/repo:
  #   interfaces:
  #     UserRepo:
```

- [ ] **Step 3：Makefile 加 mocks target**

```makefile
.PHONY: mocks
mocks:
	@which mockery > /dev/null || go install github.com/vektra/mockery/v2@v2.50.0
	mockery
```

- [ ] **Step 4：Commit**

```bash
git add server/.mockery.yaml server/Makefile
git commit -m "test: add mockery configuration"
```

### Task 1.3：docker-compose 加 OTel Collector + Jaeger

**Files:**
- Modify: `docker-compose.yml`
- Create: `docker/otel-collector-config.yaml`

- [ ] **Step 1：写 collector 配置**

```yaml
# docker/otel-collector-config.yaml
receivers:
  otlp:
    protocols:
      grpc: { endpoint: 0.0.0.0:4317 }
      http: { endpoint: 0.0.0.0:4318 }
processors:
  batch:
    timeout: 1s
exporters:
  debug:
    verbosity: normal
  prometheus:
    endpoint: 0.0.0.0:8889
  otlp/jaeger:
    endpoint: jaeger:4317
    tls: { insecure: true }
service:
  pipelines:
    traces:
      receivers: [otlp]
      processors: [batch]
      exporters: [debug, otlp/jaeger]
    metrics:
      receivers: [otlp]
      processors: [batch]
      exporters: [debug, prometheus]
```

- [ ] **Step 2：在 `docker-compose.yml` 追加两个服务**

```yaml
  otel-collector:
    image: otel/opentelemetry-collector-contrib:0.110.0
    command: ["--config=/etc/otel-collector-config.yaml"]
    volumes:
      - ./docker/otel-collector-config.yaml:/etc/otel-collector-config.yaml
    ports: ["4317:4317", "4318:4318", "8889:8889"]
  jaeger:
    image: jaegertracing/all-in-one:1.62
    ports: ["16686:16686"]
    environment: { COLLECTOR_OTLP_ENABLED: "true" }
```

- [ ] **Step 3：启动验证**

```bash
docker compose up -d otel-collector jaeger
curl -fI http://localhost:16686/   # Jaeger UI
curl -f  http://localhost:8889/metrics | head
```

预期：两端点均 200。

- [ ] **Step 4：Commit**

```bash
git add docker-compose.yml docker/otel-collector-config.yaml
git commit -m "infra: add OTel collector + Jaeger to compose"
```

---

## Phase 2：可观测性 SDK

### Task 2.1：写 observability 包

**Files:**
- Create: `server/internal/observability/otel.go`
- Test: `server/internal/observability/otel_test.go`

- [ ] **Step 1：写测试**

```go
// server/internal/observability/otel_test.go
package observability

import (
	"context"
	"testing"
	"github.com/stretchr/testify/require"
)

func TestInit_DisabledNoop(t *testing.T) {
	shutdown, err := Init(context.Background(), Config{ServiceName: "t", Disabled: true})
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}

func TestInit_WithEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
	shutdown, err := Init(context.Background(), Config{ServiceName: "t"})
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}
```

- [ ] **Step 2：跑测试 — FAIL**

```bash
cd server && go test ./internal/observability/... -run TestInit -v
# Expected: FAIL — Init/Config undefined
```

- [ ] **Step 3：实现**

```go
// server/internal/observability/otel.go
package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type Config struct {
	ServiceName    string
	ServiceVersion string
	SampleRatio    float64 // 默认 1.0
	Disabled       bool
}

type ShutdownFunc func(context.Context) error

func Init(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	if cfg.Disabled {
		return func(context.Context) error { return nil }, nil
	}
	if cfg.SampleRatio == 0 {
		cfg.SampleRatio = 1.0
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.ServiceVersion),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	traceExp, err := otlptracegrpc.New(ctx, otlptracegrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp, sdktrace.WithBatchTimeout(5*time.Second)),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))

	metricExp, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, fmt.Errorf("metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(15*time.Second))),
	)
	otel.SetMeterProvider(mp)

	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		return nil, fmt.Errorf("runtime metrics: %w", err)
	}

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		return nil
	}, nil
}
```

- [ ] **Step 4：跑测试 PASS + Commit**

```bash
cd server && go test ./internal/observability/... -v
git add server/internal/observability/
git commit -m "feat(observability): OpenTelemetry SDK initialization"
```

### Task 2.2：trace-aware slog handler

**Files:**
- Create: `server/internal/observability/slog_handler.go`
- Test: `server/internal/observability/slog_handler_test.go`

- [ ] **Step 1：写测试**

```go
// server/internal/observability/slog_handler_test.go
package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTraceHandler_InjectsTraceID(t *testing.T) {
	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	var buf bytes.Buffer
	logger := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))

	ctx, span := tp.Tracer("t").Start(context.Background(), "op")
	defer span.End()

	logger.InfoContext(ctx, "hello")

	var entry map[string]any
	require := assert.New(t)
	require.NoError(json.Unmarshal(buf.Bytes(), &entry))
	require.Equal(span.SpanContext().TraceID().String(), entry["trace_id"])
	require.Equal(span.SpanContext().SpanID().String(), entry["span_id"])
}
```

- [ ] **Step 2：实现**

```go
// server/internal/observability/slog_handler.go
package observability

import (
	"context"
	"log/slog"
	"go.opentelemetry.io/otel/trace"
)

type TraceHandler struct{ slog.Handler }

func NewTraceHandler(h slog.Handler) slog.Handler { return &TraceHandler{Handler: h} }

func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

func (h *TraceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithAttrs(a)}
}
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithGroup(name)}
}
```

- [ ] **Step 3：PASS + Commit**

```bash
cd server && go test ./internal/observability/... -v
git add server/internal/observability/slog_handler*
git commit -m "feat(observability): slog handler injecting trace/span IDs"
```

### Task 2.3：cmd/gateway 接入 OTel + slog

**Files:**
- Modify: `server/cmd/gateway/main.go`

- [ ] **Step 1：在 `main()` 起手处插入**

```go
import (
    "im-server/internal/observability"
)

// ...
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

shutdownOtel, err := observability.Init(ctx, observability.Config{
    ServiceName:    "im-gateway",
    ServiceVersion: version,
    Disabled:       os.Getenv("OTEL_DISABLED") == "true",
})
if err != nil {
    slog.Error("otel init", "err", err)
    os.Exit(1)
}
defer shutdownOtel(context.Background())

base := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
slog.SetDefault(slog.New(observability.NewTraceHandler(base)))
```

- [ ] **Step 2：启动验证**

```bash
cd server && OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 go run ./cmd/gateway &
sleep 2
curl http://localhost:8080/healthz || true   # 暂无 healthz，Phase 4 加
kill %1
```

预期：gateway 正常启动无 panic；日志为 JSON。

- [ ] **Step 3：Commit**

```bash
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): wire OTel init/shutdown and trace-aware slog"
```

### Task 2.4：cmd/message 同样接入

**Files:**
- Modify: `server/cmd/message/main.go`

- [ ] **Step 1：复制 Task 2.3 同样代码块，把 `ServiceName` 改为 `"im-message"`**

- [ ] **Step 2：启动验证**

```bash
cd server && OTEL_EXPORTER_OTLP_ENDPOINT=localhost:4317 go run ./cmd/message &
sleep 2; kill %1
```

- [ ] **Step 3：Commit**

```bash
git add server/cmd/message/main.go
git commit -m "feat(message): wire OTel init/shutdown and trace-aware slog"
```

---

## Phase 3：测试基础设施

### Task 3.1：testcontainers PG helper

**Files:**
- Create: `server/internal/testutil/containers/postgres.go`
- Create: `server/internal/testutil/containers/postgres_test.go`

- [ ] **Step 1：实现**

```go
// server/internal/testutil/containers/postgres.go
package containers

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// StartPostgres 启动一个跑了 migrations 的 PG 容器，返回 DSN。
// 测试用 t.Cleanup 自动销毁。
func StartPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()

	_, file, _, _ := runtime.Caller(0)
	migrationsDir := filepath.Join(filepath.Dir(file), "..", "..", "..", "migrations")

	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("im_test"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		postgres.WithInitScripts(filepath.Join(migrationsDir, "001_init.up.sql")),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	require.NoError(t, err)

	t.Cleanup(func() { _ = pg.Terminate(ctx) })

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}
```

- [ ] **Step 2：写冒烟测试**

```go
// server/internal/testutil/containers/postgres_test.go
package containers

import (
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

func TestStartPostgres_Smoke(t *testing.T) {
	if testing.Short() { t.Skip("requires docker") }
	dsn := StartPostgres(t)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	require.NoError(t, db.Ping())

	var n int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM users").Scan(&n))
	require.Equal(t, 0, n)
}
```

- [ ] **Step 3：跑测试**

```bash
cd server && go test -v -timeout 120s ./internal/testutil/containers/...
# Expected: PASS（约 5–15 秒，启动容器）
```

- [ ] **Step 4：Commit**

```bash
git add server/internal/testutil/containers/postgres*.go
git commit -m "test: add testcontainers postgres helper"
```

### Task 3.2：testcontainers Redis helper

**Files:**
- Create: `server/internal/testutil/containers/redis.go`
- Create: `server/internal/testutil/containers/redis_test.go`

- [ ] **Step 1：实现**

```go
// server/internal/testutil/containers/redis.go
package containers

import (
	"context"
	"testing"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

func StartRedis(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r, err := tcredis.Run(ctx, "redis:7-alpine")
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Terminate(ctx) })
	uri, err := r.ConnectionString(ctx)
	require.NoError(t, err)
	return uri
}
```

- [ ] **Step 2：冒烟测试**

```go
// server/internal/testutil/containers/redis_test.go
package containers

import (
	"context"
	"testing"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestStartRedis_Smoke(t *testing.T) {
	if testing.Short() { t.Skip("requires docker") }
	uri := StartRedis(t)
	opts, err := redis.ParseURL(uri)
	require.NoError(t, err)
	c := redis.NewClient(opts)
	defer c.Close()
	require.NoError(t, c.Ping(context.Background()).Err())
}
```

- [ ] **Step 3：跑测试 + Commit**

```bash
cd server && go test -v -timeout 60s ./internal/testutil/containers/...
git add server/internal/testutil/containers/redis*.go
git commit -m "test: add testcontainers redis helper"
```

### Task 3.3：testcontainers Pulsar helper

**Files:**
- Create: `server/internal/testutil/containers/pulsar.go`
- Create: `server/internal/testutil/containers/pulsar_test.go`

- [ ] **Step 1：实现**

```go
// server/internal/testutil/containers/pulsar.go
package containers

import (
	"context"
	"fmt"
	"testing"
	"time"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func StartPulsar(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "apachepulsar/pulsar:3.3.2",
		ExposedPorts: []string{"6650/tcp", "8080/tcp"},
		Cmd:          []string{"bin/pulsar", "standalone", "--no-functions-worker", "--no-stream-storage"},
		WaitingFor:   wait.ForLog("Created namespace public/default").WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req, Started: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "6650")
	return fmt.Sprintf("pulsar://%s:%s", host, port.Port())
}
```

- [ ] **Step 2：冒烟测试**

```go
// server/internal/testutil/containers/pulsar_test.go
package containers

import (
	"testing"
	"github.com/apache/pulsar-client-go/pulsar"
	"github.com/stretchr/testify/require"
)

func TestStartPulsar_Smoke(t *testing.T) {
	if testing.Short() { t.Skip("requires docker") }
	url := StartPulsar(t)
	cli, err := pulsar.NewClient(pulsar.ClientOptions{URL: url})
	require.NoError(t, err)
	defer cli.Close()
}
```

- [ ] **Step 3：跑测试 + Commit**

```bash
cd server && go test -v -timeout 240s ./internal/testutil/containers/...
git add server/internal/testutil/containers/pulsar*.go
git commit -m "test: add testcontainers pulsar helper"
```

### Task 3.4：httpexpect helper

**Files:**
- Create: `server/internal/testutil/httpexpect.go`

- [ ] **Step 1：实现**

```go
// server/internal/testutil/httpexpect.go
package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gavv/httpexpect/v2"
)

// NewExpect 用任意 http.Handler 起一个 httptest server 并返回 httpexpect 客户端。
func NewExpect(t *testing.T, h http.Handler) *httpexpect.Expect {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return httpexpect.Default(t, srv.URL)
}
```

- [ ] **Step 2：Commit**

```bash
git add server/internal/testutil/httpexpect.go
git commit -m "test: add httpexpect helper"
```

### Task 3.5：build tag `integration` 与 Makefile

**Files:**
- Modify: `server/Makefile`

- [ ] **Step 1：加 integration target**

```makefile
.PHONY: test-integration
test-integration:
	gotestsum --format pkgname -- -race -tags=integration -timeout 5m ./...
```

- [ ] **Step 2：调整 `check`**

```makefile
check: lint vuln test-unit
check-full: lint vuln test-unit test-integration
```

- [ ] **Step 3：Commit**

```bash
git add server/Makefile
git commit -m "test: split unit/integration test targets"
```

---

## Phase 4：Gin 共存外壳

> 关键：让 Gin 引擎同时承载 Gin 原生路由 + 现有 `net/http.ServeMux`，**不破坏一个旧端点**。

### Task 4.1：Gin router 包

**Files:**
- Create: `server/internal/http/router.go`
- Create: `server/internal/http/router_test.go`

- [ ] **Step 1：写测试**

```go
// server/internal/http/router_test.go
package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"github.com/stretchr/testify/assert"
)

func TestNew_HealthEndpoint(t *testing.T) {
	r := New(Config{ServiceName: "test"})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "ok", w.Body.String())
}

func TestNew_LegacyMuxFallthrough(t *testing.T) {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("GET /legacy", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, _ = w.Write([]byte("legacy"))
	})
	r := New(Config{ServiceName: "test", Legacy: mux})

	req := httptest.NewRequest("GET", "/legacy", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, 200, w.Code)
	assert.Equal(t, "legacy", w.Body.String())
}
```

- [ ] **Step 2：跑 — FAIL**

```bash
cd server && go test ./internal/http/... -v
# Expected: FAIL — New/Config undefined
```

- [ ] **Step 3：实现**

```go
// server/internal/http/router.go
package http

import (
	stdhttp "net/http"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

type Config struct {
	ServiceName string
	Legacy      stdhttp.Handler // 现有 net/http.ServeMux
	Mode        string          // gin.ReleaseMode / DebugMode / TestMode
}

func New(cfg Config) *gin.Engine {
	if cfg.Mode == "" { cfg.Mode = gin.ReleaseMode }
	gin.SetMode(cfg.Mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware(cfg.ServiceName))

	r.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/readyz", func(c *gin.Context) { c.String(200, "ok") })

	// 兜底：所有未匹配路由透传给旧 mux
	if cfg.Legacy != nil {
		r.NoRoute(gin.WrapH(cfg.Legacy))
	}
	return r
}
```

- [ ] **Step 4：PASS + Commit**

```bash
cd server && go test ./internal/http/... -v
git add server/internal/http/
git commit -m "feat(http): Gin router with legacy mux fallthrough"
```

### Task 4.2：cmd/gateway 切到 Gin engine

**Files:**
- Modify: `server/cmd/gateway/main.go`

- [ ] **Step 1：替换启动逻辑**

把当前 `http.ListenAndServe(":8080", mux)` 替换为：

```go
import (
    imhttp "im-server/internal/http"
    stdhttp "net/http"
    "github.com/gin-gonic/gin"
)

// ... mux 已经按原样注册了所有旧 handler ...

engine := imhttp.New(imhttp.Config{
    ServiceName: "im-gateway",
    Legacy:      mux,
    Mode:        gin.ReleaseMode,
})

srv := &stdhttp.Server{Addr: ":8080", Handler: engine}
go func() {
    if err := srv.ListenAndServe(); err != nil && err != stdhttp.ErrServerClosed {
        slog.Error("listen", "err", err)
        os.Exit(1)
    }
}()

<-ctx.Done()
shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
_ = srv.Shutdown(shutCtx)
```

- [ ] **Step 2：跑现有 e2e 与 healthz**

```bash
cd server && go run ./cmd/gateway &
sleep 2
bash ../tests/sync_test.sh         # 旧端点必须仍通过
curl -f http://localhost:8080/healthz   # 应返回 ok
kill %1
```

- [ ] **Step 3：在 Jaeger UI 验证 trace**

打开 http://localhost:16686，Service 选 `im-gateway`，能看到每个 HTTP 请求一条 trace（span 名形如 `HTTP GET /healthz`）。

- [ ] **Step 4：Commit**

```bash
git add server/cmd/gateway/main.go
git commit -m "feat(gateway): serve via Gin engine wrapping legacy mux"
```

### Task 4.3：Gin 版 JWT middleware

**Files:**
- Create: `server/internal/middleware/jwt_gin.go`
- Create: `server/internal/middleware/jwt_gin_test.go`

- [ ] **Step 1：写测试**

```go
// server/internal/middleware/jwt_gin_test.go
package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"im-server/internal/auth"
)

func TestJWTGin_NoToken_401(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(JWTGin(auth.NewVerifier("secret")))
	r.GET("/x", func(c *gin.Context) { c.Status(200) })

	req := httptest.NewRequest("GET", "/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestJWTGin_ValidToken_SetsUserID(t *testing.T) {
	gin.SetMode(gin.TestMode)
	v := auth.NewVerifier("secret")
	tok, _ := auth.NewSigner("secret").Sign(42)

	r := gin.New()
	r.Use(JWTGin(v))
	r.GET("/x", func(c *gin.Context) {
		uid, _ := c.Get(UserIDKey)
		c.JSON(200, gin.H{"uid": uid})
	})

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"uid":42`)
}
```

> 注：如现有 `auth/` 包接口与 `NewSigner/NewVerifier` 名字不同，直接用现有 API；签名/校验语义保持不变即可。

- [ ] **Step 2：实现**

```go
// server/internal/middleware/jwt_gin.go
package middleware

import (
	"strings"
	"github.com/gin-gonic/gin"
	"im-server/internal/auth"
)

const UserIDKey = "user_id"

func JWTGin(v *auth.Verifier) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		token := strings.TrimPrefix(h, "Bearer ")
		if token == "" || token == h {
			c.AbortWithStatusJSON(401, gin.H{"error": "missing token"})
			return
		}
		uid, err := v.Verify(token)
		if err != nil {
			c.AbortWithStatusJSON(401, gin.H{"error": "invalid token"})
			return
		}
		c.Set(UserIDKey, uid)
		c.Next()
	}
}
```

- [ ] **Step 3：PASS + Commit**

```bash
cd server && go test ./internal/middleware/... -v
git add server/internal/middleware/jwt_gin*
git commit -m "feat(middleware): Gin JWT middleware"
```

---

## Phase 5：pgx → GORM 全量切换（一次性，无共存）

> **核心约束**：本 phase 结束后，`store/` 包与 `pgx` 依赖**完全移除**。GORM 与 pgx **不允许共存**。
>
> 执行顺序：先把所有 repo 用 GORM 实现并通过集成测试 → 再把现有 net/http handler 一次性切换到 repo → 最后删除 store/ 与 pgx。每个 repo 单独 commit，便于回滚。

### Task 5.1：GORM Open

**Files:**
- Create: `server/internal/repo/db.go`
- Create: `server/internal/repo/db_test.go`

- [ ] **Step 1：写测试**

```go
// server/internal/repo/db_test.go
//go:build integration

package repo

import (
	"testing"
	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func TestOpen_Smoke(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	require.NotNil(t, db)
	sqlDB, _ := db.DB()
	require.NoError(t, sqlDB.Ping())
}
```

- [ ] **Step 2：实现**

```go
// server/internal/repo/db.go
package repo

import (
	"fmt"
	"time"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/opentelemetry/tracing"
)

type Config struct {
	DSN             string
	MaxOpen         int
	MaxIdle         int
	ConnMaxLifetime time.Duration
	LogLevel        logger.LogLevel
}

func Open(cfg Config) (*gorm.DB, error) {
	if cfg.MaxOpen == 0 { cfg.MaxOpen = 25 }
	if cfg.MaxIdle == 0 { cfg.MaxIdle = 5 }
	if cfg.ConnMaxLifetime == 0 { cfg.ConnMaxLifetime = 30 * time.Minute }
	if cfg.LogLevel == 0 { cfg.LogLevel = logger.Warn }

	db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
		Logger:                                   logger.Default.LogMode(cfg.LogLevel),
		PrepareStmt:                              true,
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, fmt.Errorf("gorm open: %w", err)
	}
	if err := db.Use(tracing.NewPlugin(tracing.WithoutMetrics())); err != nil {
		return nil, fmt.Errorf("otel plugin: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil { return nil, err }
	sqlDB.SetMaxOpenConns(cfg.MaxOpen)
	sqlDB.SetMaxIdleConns(cfg.MaxIdle)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	return db, nil
}
```

- [ ] **Step 3：PASS + Commit**

```bash
cd server && go test -tags=integration -v ./internal/repo/... -run TestOpen
git add server/internal/repo/db*
git commit -m "feat(repo): GORM Open with OTel tracing plugin"
```

### Task 5.2：GORM models 与 schema 对齐

**Files:**
- Create: `server/internal/repo/models.go`
- Create: `server/internal/repo/models_test.go`

- [ ] **Step 1：先看实际 schema 字段（一次性参考）**

```bash
grep -E '^\s*(CREATE TABLE|--)' server/migrations/001_init.up.sql
```

- [ ] **Step 2：实现 models（按 `001_init.up.sql` 字段精确对齐；类型有出入按实际 SQL 修正）**

```go
// server/internal/repo/models.go
package repo

import "time"

type User struct {
	ID           int64     `gorm:"primaryKey"`
	Username     string    `gorm:"uniqueIndex;size:64;not null"`
	Email        string    `gorm:"uniqueIndex;size:128"`
	PasswordHash string    `gorm:"column:password_hash;not null"`
	DisplayName  string    `gorm:"column:display_name;size:128"`
	AvatarURL    string    `gorm:"column:avatar_url"`
	Status       int       `gorm:"not null;default:1"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}
func (User) TableName() string { return "users" }

type Channel struct {
	ID        int64     `gorm:"primaryKey"`
	Type      int       `gorm:"not null"` // 1=DM, 2=Group
	Name      string    `gorm:"size:128"`
	AvatarURL string    `gorm:"column:avatar_url"`
	Seq       int64     `gorm:"not null;default:0"`
	CreatorID int64     `gorm:"column:creator_id"`
	CreatedAt time.Time
	UpdatedAt time.Time
}
func (Channel) TableName() string { return "channels" }

type ChannelMember struct {
	UserID       int64     `gorm:"primaryKey;column:user_id"`
	ChannelID    int64     `gorm:"primaryKey;column:channel_id"`
	Role         int
	LastReadSeq  int64     `gorm:"column:last_read_seq"`
	PhantomCount int       `gorm:"column:phantom_count"`
	JoinedAt     time.Time `gorm:"column:joined_at"`
}
func (ChannelMember) TableName() string { return "channel_members" }

type Message struct {
	ID            int64     `gorm:"primaryKey"`
	ChannelID     int64     `gorm:"column:channel_id;index"`
	Seq           int64     `gorm:"not null"`
	ClientMsgID   string    `gorm:"column:client_msg_id;uniqueIndex:uq_msg_client"`
	SenderID      int64     `gorm:"column:sender_id"`
	MsgType       int       `gorm:"column:msg_type"`
	Content       string
	VisibleTo     []int64   `gorm:"column:visible_to;type:bigint[]"`
	ReplyTo       *int64    `gorm:"column:reply_to"`
	ForwardedFrom *int64    `gorm:"column:forwarded_from"`
	CreatedAt     time.Time
}
func (Message) TableName() string { return "messages" }

type Friendship struct {
	RequesterID int64 `gorm:"primaryKey;column:requester_id"`
	AddresseeID int64 `gorm:"primaryKey;column:addressee_id"`
	Status      int   // 1=pending,2=accepted,3=blocked
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
func (Friendship) TableName() string { return "friendships" }

type File struct {
	ID            int64  `gorm:"primaryKey"`
	UploaderID    int64  `gorm:"column:uploader_id"`
	FileName      string `gorm:"column:file_name"`
	FileSize      int64  `gorm:"column:file_size"`
	MimeType      string `gorm:"column:mime_type"`
	StoragePath   string `gorm:"column:storage_path"`
	ThumbnailPath string `gorm:"column:thumbnail_path"`
	CreatedAt     time.Time
}
func (File) TableName() string { return "files" }

type MessageAttachment struct {
	MessageID int64 `gorm:"primaryKey;column:message_id"`
	FileID    int64 `gorm:"primaryKey;column:file_id"`
}
func (MessageAttachment) TableName() string { return "message_attachments" }

type MessageFavorite struct {
	UserID    int64     `gorm:"primaryKey;column:user_id"`
	MessageID int64     `gorm:"primaryKey;column:message_id"`
	CreatedAt time.Time
}
func (MessageFavorite) TableName() string { return "message_favorites" }

type UserSettings struct {
	UserID              int64 `gorm:"primaryKey;column:user_id"`
	NotificationEnabled bool  `gorm:"column:notification_enabled"`
	Theme               string
	Language            string
	SettingsJSON        string `gorm:"column:settings_json"`
	UpdatedAt           time.Time
}
func (UserSettings) TableName() string { return "user_settings" }
```

- [ ] **Step 3：写测试 — 模型字段映射验证**

```go
// server/internal/repo/models_test.go
//go:build integration

package repo

import (
	"testing"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"im-server/internal/testutil/containers"
)

func TestModels_NoMigrationDrift(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)

	// 对每个模型 First 一次（空表必返回 ErrRecordNotFound，
	// 但若字段映射错会返回 column does not exist 等错误）。
	cases := []any{&User{}, &Channel{}, &ChannelMember{}, &Message{},
		&Friendship{}, &File{}, &MessageAttachment{}, &MessageFavorite{}, &UserSettings{}}
	for _, m := range cases {
		err := db.First(m).Error
		require.True(t, err == nil || err == gorm.ErrRecordNotFound,
			"model %T schema mismatch: %v", m, err)
	}
}
```

- [ ] **Step 4：跑测试**

```bash
cd server && go test -tags=integration -v ./internal/repo/... -run TestModels
```

如有字段映射错误，按错误信息修正 `models.go`，再跑直到通过。

- [ ] **Step 5：Commit**

```bash
git add server/internal/repo/models*
git commit -m "feat(repo): GORM models matching existing schema"
```

### Task 5.3：实现 UserRepo（GORM）

**Files:**
- Create: `server/internal/repo/user.go`
- Create: `server/internal/repo/user_test.go`

> **本 task 是后续 5.4–5.10 所有 repo 的模板**，每个 repo 严格按"接口 → 集成测试 → 实现 → PASS → commit"五步走。

- [ ] **Step 1：定义错误与接口**

```go
// server/internal/repo/errors.go
package repo

import "errors"

var ErrNotFound = errors.New("not found")
```

- [ ] **Step 2：写集成测试（模板）**

```go
// server/internal/repo/user_test.go
//go:build integration

package repo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"im-server/internal/testutil/containers"
)

func newUserRepo(t *testing.T) UserRepo {
	dsn := containers.StartPostgres(t)
	db, err := Open(Config{DSN: dsn})
	require.NoError(t, err)
	return NewUserRepo(db)
}

func TestUserRepo_CreateAndGetByUsername(t *testing.T) {
	r := newUserRepo(t)
	ctx := context.Background()

	u := &User{Username: "alice", Email: "a@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, u))
	require.NotZero(t, u.ID)

	got, err := r.GetByUsername(ctx, "alice")
	require.NoError(t, err)
	require.Equal(t, u.ID, got.ID)
	require.Equal(t, "h", got.PasswordHash)
}

func TestUserRepo_GetByUsername_NotFound(t *testing.T) {
	r := newUserRepo(t)
	_, err := r.GetByUsername(context.Background(), "nope")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestUserRepo_UpdateProfile(t *testing.T) {
	r := newUserRepo(t)
	ctx := context.Background()
	u := &User{Username: "u", Email: "u@x.com", PasswordHash: "h", Status: 1}
	require.NoError(t, r.Create(ctx, u))

	require.NoError(t, r.UpdateProfile(ctx, u.ID, "New Name", "https://x.com/a.png"))
	got, _ := r.GetByID(ctx, u.ID)
	require.Equal(t, "New Name", got.DisplayName)
}
```

- [ ] **Step 3：实现**

```go
// server/internal/repo/user.go
package repo

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

type UserRepo interface {
	Create(ctx context.Context, u *User) error
	GetByID(ctx context.Context, id int64) (*User, error)
	GetByUsername(ctx context.Context, name string) (*User, error)
	UpdateProfile(ctx context.Context, id int64, displayName, avatarURL string) error
	ListByIDs(ctx context.Context, ids []int64) ([]User, error)
}

type gormUserRepo struct{ db *gorm.DB }

func NewUserRepo(db *gorm.DB) UserRepo { return &gormUserRepo{db: db} }

func (r *gormUserRepo) Create(ctx context.Context, u *User) error {
	return r.db.WithContext(ctx).Create(u).Error
}

func (r *gormUserRepo) GetByID(ctx context.Context, id int64) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).First(&u, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return nil, ErrNotFound }
		return nil, err
	}
	return &u, nil
}

func (r *gormUserRepo) GetByUsername(ctx context.Context, name string) (*User, error) {
	var u User
	if err := r.db.WithContext(ctx).Where("username = ?", name).First(&u).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) { return nil, ErrNotFound }
		return nil, err
	}
	return &u, nil
}

func (r *gormUserRepo) UpdateProfile(ctx context.Context, id int64, displayName, avatarURL string) error {
	return r.db.WithContext(ctx).Model(&User{}).Where("id = ?", id).
		Updates(map[string]any{"display_name": displayName, "avatar_url": avatarURL}).Error
}

func (r *gormUserRepo) ListByIDs(ctx context.Context, ids []int64) ([]User, error) {
	if len(ids) == 0 { return nil, nil }
	var us []User
	err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&us).Error
	return us, err
}
```

- [ ] **Step 4：跑通 + Commit**

```bash
cd server && go test -tags=integration -v ./internal/repo/... -run TestUserRepo
git add server/internal/repo/errors.go server/internal/repo/user*
git commit -m "feat(repo): UserRepo with GORM"
```

### Task 5.4：实现 UserSettingsRepo

**Files:**
- Create: `server/internal/repo/user_settings.go`、`server/internal/repo/user_settings_test.go`

接口：
```go
type UserSettingsRepo interface {
    Get(ctx context.Context, uid int64) (*UserSettings, error)
    Upsert(ctx context.Context, s *UserSettings) error
}
```

集成测试覆盖：首次 Get → ErrNotFound；Upsert 后 Get 命中；二次 Upsert 字段覆盖。

实现要点：用 `db.Save(s)` 或 `OnConflict{UpdateAll: true}` 实现 upsert。

```bash
git add server/internal/repo/user_settings*
git commit -m "feat(repo): UserSettingsRepo with GORM"
```

### Task 5.5：实现 ChannelRepo（含成员）

**Files:**
- Create: `server/internal/repo/channel.go`、`server/internal/repo/channel_test.go`

接口：
```go
type ChannelRepo interface {
    Create(ctx context.Context, ch *Channel) error
    GetByID(ctx context.Context, id int64) (*Channel, error)
    UpdateMeta(ctx context.Context, id int64, name, avatarURL string) error
    Delete(ctx context.Context, id int64) error
    AddMembers(ctx context.Context, channelID int64, userIDs []int64, role int) error
    RemoveMember(ctx context.Context, channelID, userID int64) error
    UpdateRole(ctx context.Context, channelID, userID int64, role int) error
    ListByUser(ctx context.Context, uid int64) ([]Channel, error)
    ListMembers(ctx context.Context, channelID int64) ([]ChannelMember, error)
    UpdateLastReadSeq(ctx context.Context, channelID, userID, seq int64) error
    EnsureDM(ctx context.Context, a, b int64) (*Channel, error) // 事务: 查 DM 已存在则返回，否则创建 + 加 2 个 member
    NextSeq(ctx context.Context, channelID int64) (int64, error) // 事务内 SELECT FOR UPDATE + UPDATE
}
```

集成测试要点：
- `EnsureDM(a, b)` 与 `EnsureDM(b, a)` 返回同一 channel
- `AddMembers` 同 user 重复插入用 `OnConflict{DoNothing: true}` 幂等
- `NextSeq` 在 100 个并发 goroutine 调用后返回 1..100 严格递增无重复

```bash
git add server/internal/repo/channel*
git commit -m "feat(repo): ChannelRepo with GORM (incl. seq + DM ensure)"
```

### Task 5.6：实现 MessageRepo（最复杂）

**Files:**
- Create: `server/internal/repo/message.go`、`server/internal/repo/message_test.go`

接口：
```go
type MessageRepo interface {
    // Send 在事务内：调 ChannelRepo.NextSeq 取 seq, 设到 m.Seq, 插入,
    // visible_to 非空时给被排除的成员 phantom_count++
    Send(ctx context.Context, m *Message) error
    GetByClientMsgID(ctx context.Context, channelID int64, clientID string) (*Message, error)
    Get(ctx context.Context, id int64) (*Message, error)
    ListByChannel(ctx context.Context, channelID, fromSeq int64, limit int) ([]Message, error)
    DetectHoles(ctx context.Context, channelID, fromSeq, toSeq int64) ([]int64, error)
    DeleteByID(ctx context.Context, id, userID int64) error
}
```

集成测试要点（用 testcontainers PG）：
- 100 goroutine 并发 Send → 最终 seq 1..100 严格连续，无重复
- 同一 `client_msg_id` 重复 Send 通过 unique index + 调用方先调 GetByClientMsgID 兜底（service 层处理）
- `visible_to=[bob]` 时，alice 的 ListByChannel 不返回此消息
- `DetectHoles(channel, 1, 10)` 在 7,8 缺失时返回 `[7,8]`

实现要点：
- `Send` 必须用 `db.Transaction(func(tx *gorm.DB) error { ... })`
- `NextSeq` 在事务内调用：`tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&channel, channelID)`，然后 `channel.Seq++; tx.Save(&channel)`
- `visible_to` 用 `pq.Int64Array` 或 GORM 内置 PostgreSQL 数组支持

```bash
git add server/internal/repo/message*
git commit -m "feat(repo): MessageRepo with GORM (txn + seq + phantom)"
```

### Task 5.7：实现 FriendshipRepo

**Files:**
- Create: `server/internal/repo/friendship.go`、`server/internal/repo/friendship_test.go`

接口：
```go
type FriendshipRepo interface {
    Request(ctx context.Context, requesterID, addresseeID int64) error
    UpdateStatus(ctx context.Context, requesterID, addresseeID int64, status int) error
    GetPair(ctx context.Context, a, b int64) (*Friendship, error)
    ListByUser(ctx context.Context, uid int64, status int) ([]Friendship, error)
    AreFriends(ctx context.Context, a, b int64) (bool, error)
    IsBlocked(ctx context.Context, by, target int64) (bool, error)
}
```

集成测试要点：双向查询、状态机转换、blocked 后 IsBlocked=true。

```bash
git add server/internal/repo/friendship*
git commit -m "feat(repo): FriendshipRepo with GORM"
```

### Task 5.8：实现 SearchRepo（PG tsvector）

**Files:**
- Create: `server/internal/repo/search.go`、`server/internal/repo/search_test.go`

接口：
```go
type SearchRepo interface {
    Messages(ctx context.Context, uid int64, q string, limit int) ([]Message, error)
    Users(ctx context.Context, q string, limit int) ([]User, error)
    Channels(ctx context.Context, uid int64, q string, limit int) ([]Channel, error)
}
```

实现：用 GORM `Raw(...).Scan(...)` 执行 PG `tsquery`，绑定参数防注入。例如：
```go
err := db.WithContext(ctx).Raw(`
    SELECT m.* FROM messages m
    JOIN channel_members cm ON cm.channel_id = m.channel_id AND cm.user_id = ?
    WHERE to_tsvector('simple', m.content) @@ plainto_tsquery('simple', ?)
    ORDER BY m.created_at DESC LIMIT ?
`, uid, q, limit).Scan(&out).Error
```

集成测试要点：注入字符（`'` `;` `--`）安全；用户不在某 channel 时搜不到。

```bash
git add server/internal/repo/search*
git commit -m "feat(repo): SearchRepo with GORM Raw SQL"
```

### Task 5.9：实现 FileRepo + FavoriteRepo

**Files:**
- Create: `server/internal/repo/file.go`、`server/internal/repo/file_test.go`
- Create: `server/internal/repo/favorite.go`、`server/internal/repo/favorite_test.go`

接口：
```go
type FileRepo interface {
    Create(ctx context.Context, f *File) error
    GetByID(ctx context.Context, id int64) (*File, error)
    ListByOwner(ctx context.Context, uid int64) ([]File, error)
    Delete(ctx context.Context, id, uid int64) error // 限定 owner
    LinkToMessage(ctx context.Context, msgID, fileID int64) error
}

type FavoriteRepo interface {
    Add(ctx context.Context, uid, mid int64) error    // 幂等：OnConflict DoNothing
    Remove(ctx context.Context, uid, mid int64) error
    ListByUser(ctx context.Context, uid int64, limit, offset int) ([]Message, error)
}
```

```bash
git add server/internal/repo/{file,favorite}*
git commit -m "feat(repo): FileRepo and FavoriteRepo with GORM"
```

### Task 5.10：mockery 注册所有 repo 接口

**Files:**
- Modify: `server/.mockery.yaml`

- [ ] **Step 1：写完整接口列表**

```yaml
packages:
  im-server/internal/repo:
    interfaces:
      UserRepo:
      UserSettingsRepo:
      ChannelRepo:
      MessageRepo:
      FriendshipRepo:
      SearchRepo:
      FileRepo:
      FavoriteRepo:
```

- [ ] **Step 2：生成 + 编译**

```bash
cd server && make mocks
go build ./...
```

- [ ] **Step 3：Commit**

```bash
git add server/.mockery.yaml server/internal/repo/mocks/
git commit -m "test: generate mocks for all repo interfaces"
```

### Task 5.11：cmd/gateway/message 切换到 GORM

**Files:**
- Modify: `server/cmd/gateway/main.go`
- Modify: `server/cmd/message/main.go`

- [ ] **Step 1：在 gateway main 用 repo.Open 替换原 store/pgxpool 初始化**

```go
import "im-server/internal/repo"

gormDB, err := repo.Open(repo.Config{DSN: cfg.Postgres.DSN})
if err != nil {
    slog.Error("gorm open", "err", err); os.Exit(1)
}
defer func() {
    if sqlDB, e := gormDB.DB(); e == nil { _ = sqlDB.Close() }
}()

// 注入到现有 handler（这一步在 5.12 才把 handler 内部改完）
userRepo     := repo.NewUserRepo(gormDB)
channelRepo  := repo.NewChannelRepo(gormDB)
messageRepo  := repo.NewMessageRepo(gormDB)
friendRepo   := repo.NewFriendshipRepo(gormDB)
searchRepo   := repo.NewSearchRepo(gormDB)
fileRepo     := repo.NewFileRepo(gormDB)
favoriteRepo := repo.NewFavoriteRepo(gormDB)
settingsRepo := repo.NewUserSettingsRepo(gormDB)
// 删除原 pgxpool / store.New(...) 初始化代码
```

- [ ] **Step 2：cmd/message 同样替换**（`message-service` 主要用 messageRepo + channelRepo）

- [ ] **Step 3：先编译**

```bash
cd server && go build ./...
```

预期：handler 包仍引用 `store.*` 类型，编译报错 — 5.12 修复。

- [ ] **Step 4：暂存（不 commit，等 5.12 一起 commit 完整可编译版本）**

### Task 5.12：把现有 net/http handler 全量切到 repo

**Files:**
- Modify: 所有 `server/internal/handler/*.go`（不删，只改实现/构造函数签名）
- Modify: `server/cmd/gateway/main.go`（注入新 repo）

> **目标**：所有 `net/http` handler 内部不再调用 `store.*`，改为调用 `repo.*` 接口。**响应格式、状态码、错误码完全保留**。

- [ ] **Step 1：每个 handler 文件改两处**

例：`server/internal/handler/auth.go`
```go
// before
type AuthHandler struct { users *store.UserStore; ... }
func NewAuthHandler(users *store.UserStore, ...) *AuthHandler { ... }
// 内部：users.Insert(ctx, u)、users.FindByName(ctx, name)

// after
type AuthHandler struct { users repo.UserRepo; ... }
func NewAuthHandler(users repo.UserRepo, ...) *AuthHandler { ... }
// 内部：users.Create(ctx, u)、users.GetByUsername(ctx, name)
```

按下表逐文件替换：

| handler 文件 | 旧依赖 | 新依赖 |
|---|---|---|
| `handler/auth.go` | `*store.UserStore` | `repo.UserRepo` |
| `handler/profile.go` | `*store.UserStore` | `repo.UserRepo` |
| `handler/settings.go` | `*store.SettingsStore` | `repo.UserSettingsRepo` |
| `handler/channel.go` | `*store.ChannelStore` | `repo.ChannelRepo` |
| `handler/message.go` | `*store.MessageStore` | `repo.MessageRepo`, `repo.ChannelRepo` |
| `handler/friend.go` | `*store.FriendshipStore` | `repo.FriendshipRepo` |
| `handler/sync.go` | 多 store | `repo.MessageRepo`, `repo.ChannelRepo` |
| `handler/search.go` | `*store.SearchStore` | `repo.SearchRepo` |
| `handler/file.go` | `*store.FileStore` | `repo.FileRepo` |
| `handler/favorite.go` | `*store.FavoriteStore` | `repo.FavoriteRepo` |

> 现有 store 方法名与 repo 方法名可能不同，按 5.3–5.9 定义的 repo 方法签名调用；error 类型 `store.ErrXxx` → `repo.ErrNotFound` 等。

- [ ] **Step 2：在 gateway main 用新 repo 实例化所有 handler**

```go
mux.Handle("POST /api/auth/register", handler.NewAuthHandler(userRepo, cfg.JWTSecret).Register())
// ...所有 handler 同样替换构造参数
```

- [ ] **Step 3：编译**

```bash
cd server && go build ./...
```

- [ ] **Step 4：跑现有 handler 单元测试**

```bash
cd server && make test-unit
```

预期：可能多个测试因 store mock 失效而 FAIL — 这些是 store 层旧测试。逐个删除（store 层马上要删），handler 层测试若 mock 了 store，临时改为 mock repo 接口。

> 注：原 `handler/*_test.go` 的测试价值在 Phase 6+ 切片中由 Gin 版 httpexpect 测试取代，此处只需让"编译通过 + 不被 break 的测试通过"，不需要全绿。

- [ ] **Step 5：跑 sync_test.sh 端到端验证**

```bash
docker compose up -d postgres redis pulsar otel-collector jaeger
cd server && make migrate-up
go run ./cmd/gateway &
sleep 2
bash ../tests/sync_test.sh   # 必须通过
kill %1
```

预期：所有现有端点行为不变。

- [ ] **Step 6：Commit**（与 5.11 合并 commit）

```bash
git add server/cmd/ server/internal/handler/
git commit -m "refactor: switch all handlers from store/pgx to repo/GORM"
```

### Task 5.13：删除 store/ 包与 pgx 依赖

**Files:**
- Delete: `server/internal/store/` 整个目录（含 `pg.go` `redis.go` `*.go`）
- Modify: `server/go.mod`（移除 `pgx` 直接依赖）

> **注**：若 `store/redis.go` 提供 Redis 客户端初始化（与 PG 无关），把 Redis 初始化代码搬到 `server/internal/repo/redis.go`（或直接放 `cmd/gateway/main.go`），不要因此保留整个 store/ 目录。

- [ ] **Step 1：把 Redis 客户端初始化迁移到新位置**

```go
// server/internal/repo/redis.go (新建)
package repo

import (
    "github.com/redis/go-redis/v9"
    "github.com/redis/go-redis/extra/redisotel/v9"
)

func OpenRedis(addr, password string, db int) (*redis.Client, error) {
    c := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db})
    if err := redisotel.InstrumentTracing(c); err != nil { return nil, err }
    if err := redisotel.InstrumentMetrics(c); err != nil { return nil, err }
    return c, nil
}
```

- [ ] **Step 2：cmd/gateway 与 cmd/message 改用 repo.OpenRedis**

```go
rdb, err := repo.OpenRedis(cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
```

- [ ] **Step 3：删除 store/ 全部文件**

```bash
git rm -r server/internal/store
```

- [ ] **Step 4：移除 pgx 直接依赖**

```bash
cd server
go mod tidy
# 检查 go.mod / go.sum 中 github.com/jackc/pgx 应只在 indirect 区出现（被 GORM postgres driver 间接依赖），
# 不应出现在直接 require 中。若仍在直接 require：
go mod edit -droprequire=github.com/jackc/pgx/v5
go mod tidy
```

- [ ] **Step 5：编译 + 全套测试**

```bash
cd server && make check-full
bash ../tests/sync_test.sh
```

- [ ] **Step 6：Commit**

```bash
git add -A
git commit -m "refactor: remove store package and pgx direct dependency"
```

### Task 5.14：Jaeger 验证 GORM trace

- [ ] **Step 1：跑现有 e2e**

```bash
docker compose up -d
cd server && make migrate-up
go run ./cmd/gateway &
sleep 2
bash ../tests/sync_test.sh
kill %1
```

- [ ] **Step 2：Jaeger UI（http://localhost:16686）按服务过滤**

预期：每个 HTTP 请求 trace 内含 `gorm.Query` / `gorm.Create` 子 span，包含 `db.statement`、`db.system=postgresql` 属性。

- [ ] **Step 3：Phase 5 完成检查清单**
  - [ ] `go.mod` 直接依赖中无 `github.com/jackc/pgx`
  - [ ] `server/internal/store/` 目录不存在
  - [ ] `make check-full` 全绿
  - [ ] `tests/sync_test.sh` 通过
  - [ ] Jaeger 可见 GORM 子 span
  - [ ] 所有现有 net/http handler 仍由原路由提供（Phase 6 才切 Gin）

---

## Phase 6：Auth 切片 — 完整 Gin TDD 模板

> **本节是后续所有切片的模板，必须熟读。** 数据层（repo + mock）已在 Phase 5 完成；本 phase 只负责 service + Gin handler + 切换。
>
> 后续切片严格按 6.1 → 6.5 顺序执行：
> 1. AuthService 单测（用 Phase 5 生成的 repo mock）+ 实现
> 2. Gin handler + httpexpect 测试 + 实现
> 3. 切换 gateway 路由 + 删除旧 net/http handler
> 4. 集成测试（真 PG + 真 HTTP）
> 5. 检查清单

### Task 6.1：AuthService 单测 + 实现

**Files:**
- Create: `server/internal/service/auth.go`
- Create: `server/internal/service/auth_test.go`

- [ ] **Step 1：写单测**

```go
// server/internal/service/auth_test.go
package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
)

func TestAuthService_Register_Success(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, repo.ErrNotFound)
	m.EXPECT().Create(mock.Anything, mock.MatchedBy(func(u *repo.User) bool {
		return u.Username == "alice" && u.PasswordHash != "plain"
	})).Run(func(_ context.Context, u *repo.User) { u.ID = 1 }).Return(nil)

	svc := NewAuthService(m, "secret")
	uid, token, err := svc.Register(context.Background(), "alice", "a@x.com", "plain")
	require.NoError(t, err)
	require.Equal(t, int64(1), uid)
	require.NotEmpty(t, token)
}

func TestAuthService_Register_Duplicate(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(&repo.User{ID: 1}, nil)

	svc := NewAuthService(m, "secret")
	_, _, err := svc.Register(context.Background(), "alice", "a@x.com", "x")
	require.ErrorIs(t, err, ErrUserExists)
}

func TestAuthService_Login_OK(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pwd"), bcrypt.DefaultCost)
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, PasswordHash: string(hash)}, nil)

	svc := NewAuthService(m, "secret")
	tok, err := svc.Login(context.Background(), "alice", "pwd")
	require.NoError(t, err)
	require.NotEmpty(t, tok)
}

func TestAuthService_Login_BadPassword(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("pwd"), bcrypt.DefaultCost)
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, PasswordHash: string(hash)}, nil)

	svc := NewAuthService(m, "secret")
	_, err := svc.Login(context.Background(), "alice", "wrong")
	require.ErrorIs(t, err, ErrBadCreds)
}

func TestAuthService_Login_NoUser(t *testing.T) {
	m := mocks.NewUserRepoMock(t)
	m.EXPECT().GetByUsername(mock.Anything, "ghost").Return(nil, repo.ErrNotFound)

	svc := NewAuthService(m, "secret")
	_, err := svc.Login(context.Background(), "ghost", "x")
	require.ErrorIs(t, err, ErrBadCreds)
}
```

- [ ] **Step 2：跑 — FAIL**

```bash
cd server && go test ./internal/service/... -v
```

- [ ] **Step 3：实现**

```go
// server/internal/service/auth.go
package service

import (
	"context"
	"errors"

	"golang.org/x/crypto/bcrypt"
	"im-server/internal/auth"
	"im-server/internal/repo"
)

var (
	ErrUserExists = errors.New("user already exists")
	ErrBadCreds   = errors.New("invalid credentials")
)

type AuthService struct {
	users  repo.UserRepo
	signer *auth.Signer
}

func NewAuthService(users repo.UserRepo, secret string) *AuthService {
	return &AuthService{users: users, signer: auth.NewSigner(secret)}
}

func (s *AuthService) Register(ctx context.Context, username, email, password string) (int64, string, error) {
	if _, err := s.users.GetByUsername(ctx, username); err == nil {
		return 0, "", ErrUserExists
	} else if !errors.Is(err, repo.ErrNotFound) {
		return 0, "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil { return 0, "", err }

	u := &repo.User{Username: username, Email: email, PasswordHash: string(hash), Status: 1}
	if err := s.users.Create(ctx, u); err != nil { return 0, "", err }

	tok, err := s.signer.Sign(u.ID)
	return u.ID, tok, err
}

func (s *AuthService) Login(ctx context.Context, username, password string) (string, error) {
	u, err := s.users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) { return "", ErrBadCreds }
		return "", err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return "", ErrBadCreds
	}
	return s.signer.Sign(u.ID)
}
```

> 若现有 `auth.NewSigner/Sign` 接口不同，按现有签名调整本服务，保证 token 生成行为与旧 handler 一致。

- [ ] **Step 4：PASS + Commit**

```bash
cd server && go test -short -v ./internal/service/...
git add server/internal/service/auth*
git commit -m "feat(service): AuthService with TDD coverage"
```

### Task 6.2：Gin Auth handler + httpexpect 测试

**Files:**
- Create: `server/internal/http/auth.go`
- Create: `server/internal/http/auth_test.go`

- [ ] **Step 1：写 handler 测试**

```go
// server/internal/http/auth_test.go
package http

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
	"im-server/internal/repo"
	"im-server/internal/repo/mocks"
	"im-server/internal/service"
	"im-server/internal/testutil"
)

func setupAuth(t *testing.T) (*gin.Engine, *mocks.UserRepoMock) {
	gin.SetMode(gin.TestMode)
	m := mocks.NewUserRepoMock(t)
	svc := service.NewAuthService(m, "secret")
	r := gin.New()
	RegisterAuthRoutes(r, svc)
	return r, m
}

func TestAuthHandler_Register_201(t *testing.T) {
	r, m := setupAuth(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(nil, repo.ErrNotFound)
	m.EXPECT().Create(mock.Anything, mock.Anything).
		Run(func(_ context.Context, u *repo.User) { u.ID = 1 }).Return(nil)

	e := testutil.NewExpect(t, r)
	e.POST("/api/auth/register").
		WithJSON(map[string]string{"username": "alice", "email": "a@x.com", "password": "pwd123"}).
		Expect().Status(201).JSON().Object().
		Value("token").String().NotEmpty()
}

func TestAuthHandler_Register_409(t *testing.T) {
	r, m := setupAuth(t)
	m.EXPECT().GetByUsername(mock.Anything, "alice").Return(&repo.User{ID: 1}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{"username": "alice", "email": "a@x.com", "password": "pwd123"}).
		Expect().Status(409)
}

func TestAuthHandler_Register_400_ValidationFails(t *testing.T) {
	r, _ := setupAuth(t)
	testutil.NewExpect(t, r).POST("/api/auth/register").
		WithJSON(map[string]string{"username": "a", "email": "bad", "password": "x"}).
		Expect().Status(400)
}

func TestAuthHandler_Login_OK(t *testing.T) {
	r, m := setupAuth(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pwd123"), bcrypt.DefaultCost)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, PasswordHash: string(hash)}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/login").
		WithJSON(map[string]string{"username": "alice", "password": "pwd123"}).
		Expect().Status(200).JSON().Object().Value("token").String().NotEmpty()
}

func TestAuthHandler_Login_401(t *testing.T) {
	r, m := setupAuth(t)
	hash, _ := bcrypt.GenerateFromPassword([]byte("pwd123"), bcrypt.DefaultCost)
	m.EXPECT().GetByUsername(mock.Anything, "alice").
		Return(&repo.User{ID: 7, PasswordHash: string(hash)}, nil)

	testutil.NewExpect(t, r).POST("/api/auth/login").
		WithJSON(map[string]string{"username": "alice", "password": "wrong"}).
		Expect().Status(401)
}
```

- [ ] **Step 2：实现**

```go
// server/internal/http/auth.go
package http

import (
	"errors"
	"github.com/gin-gonic/gin"
	"im-server/internal/service"
)

type registerReq struct {
	Username string `json:"username" binding:"required,min=3,max=64"`
	Email    string `json:"email"    binding:"required,email"`
	Password string `json:"password" binding:"required,min=6"`
}

type loginReq struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

func RegisterAuthRoutes(r *gin.Engine, svc *service.AuthService) {
	g := r.Group("/api/auth")

	g.POST("/register", func(c *gin.Context) {
		var in registerReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		uid, tok, err := svc.Register(c.Request.Context(), in.Username, in.Email, in.Password)
		switch {
		case errors.Is(err, service.ErrUserExists):
			c.JSON(409, gin.H{"error": "username taken"})
		case err != nil:
			c.JSON(500, gin.H{"error": err.Error()})
		default:
			c.JSON(201, gin.H{"user_id": uid, "token": tok})
		}
	})

	g.POST("/login", func(c *gin.Context) {
		var in loginReq
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}
		tok, err := svc.Login(c.Request.Context(), in.Username, in.Password)
		switch {
		case errors.Is(err, service.ErrBadCreds):
			c.JSON(401, gin.H{"error": "invalid credentials"})
		case err != nil:
			c.JSON(500, gin.H{"error": err.Error()})
		default:
			c.JSON(200, gin.H{"token": tok})
		}
	})
}
```

> 重要：响应字段（`user_id` / `token` / `error`）必须与旧 handler 完全一致，否则客户端断开。如旧 handler 返回多字段（如包含 `user` 对象），同步保留。

- [ ] **Step 3：PASS + Commit**

```bash
cd server && go test -short -v ./internal/http/... -run TestAuthHandler
git add server/internal/http/auth*
git commit -m "feat(http): Gin auth handlers with httpexpect tests"
```

### Task 6.3：切换 gateway 路由 + 删除旧 auth handler

**Files:**
- Modify: `server/cmd/gateway/main.go`
- Delete: `server/internal/handler/auth.go`、`server/internal/handler/auth_test.go`

- [ ] **Step 1：在 gateway main.go 注册新路由（在 `imhttp.New(...)` 之后）**

```go
import (
    "im-server/internal/repo"
    "im-server/internal/service"
    imhttp "im-server/internal/http"
)

authSvc := service.NewAuthService(repo.NewUserRepo(gormDB), cfg.JWTSecret)
imhttp.RegisterAuthRoutes(engine, authSvc)
```

- [ ] **Step 2：从旧 mux 删除 auth 路由注册**

打开 `server/cmd/gateway/main.go`，找到形如：

```go
mux.HandleFunc("POST /api/auth/register", handler.Register(...))
mux.HandleFunc("POST /api/auth/login", handler.Login(...))
```

直接删除（不是注释）。

- [ ] **Step 3：真请求验证**

```bash
docker compose up -d postgres redis pulsar otel-collector jaeger
cd server && make migrate-up
go run ./cmd/gateway &
sleep 2

curl -X POST http://localhost:8080/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"u1","email":"u1@x.com","password":"pwd123"}'
# 期望 201 + token

curl -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"u1","password":"pwd123"}'
# 期望 200 + token

curl -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"u1","password":"wrong"}'
# 期望 401

kill %1
```

- [ ] **Step 4：Jaeger UI 验证 trace**

打开 http://localhost:16686，Service 选 `im-gateway`，过滤 `POST /api/auth/login`，应看到一条 trace 包含子 span：
- `POST /api/auth/login`（otelgin）
- `gorm.Query SELECT ... FROM "users" WHERE username = $1`

- [ ] **Step 5：删除旧 auth handler 文件**

```bash
git rm server/internal/handler/auth.go server/internal/handler/auth_test.go
```

- [ ] **Step 6：Commit**

```bash
git add -A
git commit -m "feat(auth): cut over to Gin+GORM auth, remove legacy handler"
```

### Task 6.4：端到端集成测试

**Files:**
- Create: `server/tests/integration/auth_test.go`

- [ ] **Step 1：写端到端测试**

```go
// server/tests/integration/auth_test.go
//go:build integration

package integration

import (
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	imhttp "im-server/internal/http"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

func TestAuth_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	imhttp.RegisterAuthRoutes(r, service.NewAuthService(repo.NewUserRepo(db), "test-secret"))

	e := testutil.NewExpect(t, r)
	e.POST("/api/auth/register").
		WithJSON(map[string]string{"username": "bob", "email": "b@x.com", "password": "pwd123"}).
		Expect().Status(201)

	e.POST("/api/auth/register").
		WithJSON(map[string]string{"username": "bob", "email": "b@x.com", "password": "pwd123"}).
		Expect().Status(409)

	e.POST("/api/auth/login").
		WithJSON(map[string]string{"username": "bob", "password": "pwd123"}).
		Expect().Status(200).JSON().Object().Value("token").String().NotEmpty()

	e.POST("/api/auth/login").
		WithJSON(map[string]string{"username": "bob", "password": "wrong"}).
		Expect().Status(401)
}
```

- [ ] **Step 2：跑通**

```bash
cd server && make test-integration
```

- [ ] **Step 3：Commit**

```bash
git add server/tests/integration/auth_test.go
git commit -m "test(integration): auth end-to-end with real PG"
```

### Task 6.5：Auth 切片完成检查清单

- [ ] `server/internal/handler/auth*.go` 已删除
- [ ] `make lint` 通过
- [ ] `make test-unit` 通过（含 service/handler mock 测试）
- [ ] `make test-integration` 通过（含真 PG）
- [ ] Jaeger 中可看到 `register/login` 请求的完整 trace（含 `gorm.*` 子 span）
- [ ] 旧客户端调用 `/api/auth/login` 行为不变（响应字段、状态码完全相同）

---

## Phase 7：剩余切片路线图

> **每个切片独立成一份子计划文件** `docs/superpowers/plans/2026-04-23-slice-<name>.md`，严格按 Phase 6 的 Task 6.1 → 6.5 五步法执行（**只做 service + Gin handler + 切换 + 集成测试**，repo 与 mock 已在 Phase 5 全部完成）。
> 推荐顺序（依赖与风险递增）：profile/settings → friends → channels → messages → sync → search → files → favorites。

每个切片下面给出：**已就绪的 repo 接口**（Phase 5 产出，引用即可）、**Service 职责**、**Gin 路由**、**关键集成测试场景**、**待删除旧文件**。

---

### Slice 7.1：profile + settings（最简单，先做）

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
// 复用并扩展 repo.UserRepo
type UserRepo interface {
    // ... 已有 Create/GetByID/GetByUsername
    UpdateProfile(ctx context.Context, uid int64, displayName, avatarURL string) error
}

type UserSettingsRepo interface {
    Get(ctx context.Context, uid int64) (*UserSettings, error)
    Upsert(ctx context.Context, s *UserSettings) error
}
```

**Service：**
- `service.ProfileService` — `UpdateProfile(ctx, uid, displayName, avatarURL) error`
- `service.SettingsService` — `Get(ctx, uid)` / `Update(ctx, uid, theme, lang, notif, settingsJSON)`

**Gin 路由（受 JWTGin 保护）：**
- `PUT  /api/me/profile`
- `GET  /api/me/settings`
- `PUT  /api/me/settings`

**关键集成测试：**
- Profile 头像 URL 长度边界、空字段保留旧值
- Settings Upsert 首次插入 + 二次更新只改变化字段

**待删除：** `internal/handler/profile.go`、`internal/handler/settings.go`（store/ 已在 Phase 5 整体删除）

---

### Slice 7.2：friends

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type FriendshipRepo interface {
    Request(ctx context.Context, requesterID, addresseeID int64) error
    UpdateStatus(ctx context.Context, requesterID, addresseeID int64, status int) error
    GetPair(ctx context.Context, a, b int64) (*Friendship, error)
    ListByUser(ctx context.Context, uid int64, status int) ([]Friendship, error)
    AreFriends(ctx context.Context, a, b int64) (bool, error)
    IsBlocked(ctx context.Context, by, target int64) (bool, error)
}
```

**Service：** `service.FriendService`，封装状态机：`pending→accepted | rejected | blocked`，并对自己加好友、重复请求做拦截。

**Gin 路由：**
- `POST   /api/friends/request`        body `{addressee_id}`
- `POST   /api/friends/accept`         body `{requester_id}`
- `POST   /api/friends/reject`         body `{requester_id}`
- `POST   /api/friends/block`          body `{user_id}`
- `GET    /api/friends?status=accepted|pending|blocked`
- `DELETE /api/friends/:id`

**关键集成测试：**
- A→B request, B accept, 双向可见
- 重复 request 幂等
- B block A 后 A 不能再发 request
- 状态机非法转换（如 rejected → accepted）拒绝

**待删除：** `internal/handler/friend.go`

---

### Slice 7.3：channels

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type ChannelRepo interface {
    Create(ctx context.Context, ch *Channel) error
    GetByID(ctx context.Context, id int64) (*Channel, error)
    UpdateMeta(ctx context.Context, id int64, name, avatarURL string) error
    Delete(ctx context.Context, id int64) error
    AddMembers(ctx context.Context, channelID int64, userIDs []int64, role int) error
    RemoveMember(ctx context.Context, channelID, userID int64) error
    UpdateRole(ctx context.Context, channelID, userID int64, role int) error
    ListByUser(ctx context.Context, uid int64) ([]Channel, error)
    ListMembers(ctx context.Context, channelID int64) ([]ChannelMember, error)
    UpdateLastReadSeq(ctx context.Context, channelID, userID, seq int64) error
    EnsureDM(ctx context.Context, a, b int64) (*Channel, error) // 两人无 DM 则创建
}
```

**Service：** `service.ChannelService`，含权限判断（owner/admin/member）。

**Gin 路由：**
- `POST   /api/channels`                     # body 含 type, name, member_ids
- `GET    /api/channels`                     # 当前用户参与的
- `GET    /api/channels/:id`
- `PUT    /api/channels/:id`                 # 改 name/avatar
- `DELETE /api/channels/:id`
- `POST   /api/channels/:id/members`         # body { user_ids: [] }
- `DELETE /api/channels/:id/members/:uid`
- `PUT    /api/channels/:id/members/:uid/role`

**关键集成测试：**
- DM 类型自动 ensure（两个用户重复 createDM 返回同一 channel）
- 群组成员去重
- 非 owner 不能解散群
- 退出群组后 ListByUser 不再包含

**待删除：** `internal/handler/channel.go`

---

### Slice 7.4：messages（最复杂 — seq 原子性 + visible_to + Pulsar）

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type MessageRepo interface {
    // Send 在事务内：FOR UPDATE 锁 channel, 取 seq+1, 插入 message,
    // 更新 channel.seq, 处理 phantom_count（visible_to 非空时被排除用户 +1）
    Send(ctx context.Context, m *Message) (seq int64, err error)
    ListByChannel(ctx context.Context, ch int64, fromSeq int64, limit int) ([]Message, error)
    Get(ctx context.Context, id int64) (*Message, error)
    MarkRead(ctx context.Context, ch, uid, seq int64) error
    DetectHoles(ctx context.Context, ch int64, fromSeq, toSeq int64) ([]int64, error)
    DeleteByID(ctx context.Context, id, userID int64) error
}
```

**Service：** `service.MessageService` — `Send` 在 repo.Send 成功后投递 Pulsar；`client_msg_id` 重复直接返回旧消息（幂等）。

**Gin 路由：**
- `POST   /api/channels/:id/messages`                # 含 client_msg_id, content, visible_to, reply_to
- `GET    /api/channels/:id/messages?from_seq=&limit=`
- `POST   /api/channels/:id/messages/read`           # body { seq }
- `DELETE /api/channels/:id/messages/:mid`

**关键集成测试**（同时拉 PG + Pulsar 容器）：
- 100 个 goroutine 并发 Send，最终 seq 严格递增 1..100 无空洞
- 同一 `client_msg_id` 重发返回原 message（幂等）
- `visible_to=[bob]` 时，alice 的 ListByChannel 不返回此消息但 phantom_count++
- read seq 必须单调递增（旧 seq 提交不回退）
- DetectHoles 在区间内缺失 seq 时返回正确空洞列表

**待删除：** `internal/handler/message.go`

---

### Slice 7.5：sync（批量同步端点）

**Service：** `service.SyncService.SyncChannels(ctx, uid, []ChannelCursor) (BatchResult, error)`，并发拉取每个频道增量。

**Gin 路由：**
- `POST /api/sync` body: `{ cursors: [{channel_id, last_seq}] }`

**关键集成测试：**
- 多 channel 并发拉取，结果按 channel_id 分组
- 大频道 limit 截断 + has_more 字段正确
- 跨 reconnect 场景：心跳 Pong 返回 seq diff 后客户端调 sync 一致

**待删除：** `internal/handler/sync.go`

---

### Slice 7.6：search

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type SearchRepo interface {
    Messages(ctx context.Context, uid int64, q string, limit int) ([]Message, error)
    Users(ctx context.Context, q string, limit int) ([]User, error)
    Channels(ctx context.Context, uid int64, q string, limit int) ([]Channel, error)
}
```
GORM 用 raw SQL `Raw(...).Scan(...)` 执行 PG `tsquery`。

**Gin 路由：**
- `GET /api/search?q=&type=messages|users|channels&limit=`

**关键集成测试：**
- 中文 token（`simple` 配置）能命中
- `q` 注入字符（`'` `;`）安全
- 用户只能搜到自己参与的 channel 内消息

**待删除：** `internal/handler/search.go`

---

### Slice 7.7：files

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type FileRepo interface {
    Create(ctx context.Context, f *File) error
    GetByID(ctx context.Context, id int64) (*File, error)
    ListByOwner(ctx context.Context, uid int64) ([]File, error)
    Delete(ctx context.Context, id, uid int64) error
}
```

**Service：** `service.FileService`，封装存储后端（本地/对象存储），保留现有 `storage_path` 策略。

**Gin 路由：**
- `POST   /api/files`            multipart upload
- `GET    /api/files/:id`        下载
- `DELETE /api/files/:id`
- `GET    /api/files/:id/meta`

**关键集成测试：**
- 上传 5MB 文件 + 下载 sha256 校验一致
- 非 owner 不能 delete
- mime/size 限制返回 413/415

**待删除：** `internal/handler/file.go`

---

### Slice 7.8：favorites

**已就绪的 Repo 接口**（Phase 5 已实现，签名供参考）：
```go
type FavoriteRepo interface {
    Add(ctx context.Context, uid, mid int64) error
    Remove(ctx context.Context, uid, mid int64) error
    ListByUser(ctx context.Context, uid int64, limit, offset int) ([]Message, error)
}
```

**Gin 路由：**
- `POST   /api/favorites`        body `{message_id}`
- `DELETE /api/favorites/:mid`
- `GET    /api/favorites?limit=&offset=`

**关键集成测试：**
- Add 同一 mid 幂等（unique index）
- 关联 message 软删除后 favorites 列表是否过滤（按业务决定）

**待删除：** `internal/handler/favorite.go`

---

## Phase 8：WebSocket + Pulsar OTel 跨服务 trace

### Task 8.1：ws_handler 加 OTel span

**Files:**
- Modify: `server/internal/gateway/ws_handler.go`

- [ ] **Step 1：在帧分发处开 span**

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/trace"
)

var wsTracer = otel.Tracer("im-gateway/ws")

func (c *Conn) handleFrame(ctx context.Context, f Frame) {
    if f.Type == FrameTypePing || f.Type == FrameTypePong {
        // 心跳不开 span，避免污染
    } else {
        var span trace.Span
        ctx, span = wsTracer.Start(ctx, "ws."+string(f.Type),
            trace.WithSpanKind(trace.SpanKindServer),
            trace.WithAttributes(
                attribute.Int64("user_id", c.userID),
                attribute.String("device_id", c.deviceID),
            ))
        defer span.End()
    }
    // ... 原有 dispatch 逻辑，使用更新后的 ctx
}
```

> 帧类型常量名按现有 `internal/gateway/types.go` 实际值替换。

- [ ] **Step 2：跑现有 gateway 测试**

```bash
cd server && go test ./internal/gateway/... -v
```

- [ ] **Step 3：Commit**

```bash
git add server/internal/gateway/ws_handler.go
git commit -m "feat(gateway): OTel spans on WS frame dispatch"
```

### Task 8.2：Pulsar producer 注入 trace context

**Files:**
- Modify: `server/internal/pulsar/producer.go`（按现有 producer.go 实际包/函数名调整）

- [ ] **Step 1：在 Send 处注入 + 开 span**

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/attribute"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/trace"
    "github.com/apache/pulsar-client-go/pulsar"
)

var pulsarTracer = otel.Tracer("im-pulsar")

func (p *Producer) Send(ctx context.Context, payload []byte, props map[string]string) error {
    if props == nil { props = map[string]string{} }
    otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(props))

    ctx, span := pulsarTracer.Start(ctx, "pulsar.produce."+p.topic,
        trace.WithSpanKind(trace.SpanKindProducer),
        trace.WithAttributes(
            attribute.String("messaging.system", "pulsar"),
            attribute.String("messaging.destination.name", p.topic),
        ))
    defer span.End()

    _, err := p.inner.Send(ctx, &pulsar.ProducerMessage{Payload: payload, Properties: props})
    if err != nil { span.RecordError(err) }
    return err
}
```

- [ ] **Step 2：编译 + Commit**

```bash
cd server && go build ./...
git add server/internal/pulsar/producer.go
git commit -m "feat(pulsar): inject OTel trace context into producer messages"
```

### Task 8.3：Pulsar consumer 提取 trace context

**Files:**
- Modify: `server/internal/gateway/push_consumer.go`
- Modify: `server/cmd/message/main.go`（消息持久化消费循环）

- [ ] **Step 1：在 Receive 后 Extract**

```go
import (
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/propagation"
    "go.opentelemetry.io/otel/trace"
)

var consumerTracer = otel.Tracer("im-pulsar/consumer")

for {
    msg, err := consumer.Receive(ctx)
    if err != nil { return }

    msgCtx := otel.GetTextMapPropagator().Extract(ctx,
        propagation.MapCarrier(msg.Properties()))
    msgCtx, span := consumerTracer.Start(msgCtx, "pulsar.consume."+topic,
        trace.WithSpanKind(trace.SpanKindConsumer))

    // ... 业务处理 (use msgCtx)

    span.End()
    consumer.Ack(msg)
}
```

- [ ] **Step 2：端到端验证 — 一条消息一条 trace**

```bash
docker compose up -d
cd server && make migrate-up
go run ./cmd/gateway &
go run ./cmd/message &
sleep 3

# 用客户端或 curl WS 发消息（跳过 — 用 e2e 脚本）
# 然后在 Jaeger UI（http://localhost:16686）按 trace_id 应看到完整链路：
#   ws.send → pulsar.produce.outbound → pulsar.consume.outbound → gorm.Insert
```

- [ ] **Step 3：Commit**

```bash
kill %1 %2
git add server/internal/gateway/push_consumer.go server/cmd/message/main.go
git commit -m "feat: extract OTel trace context in pulsar consumers"
```

### Task 8.4：WebSocket 在线连接数 metric

**Files:**
- Modify: `server/internal/gateway/hub.go`

- [ ] **Step 1：注册 ObservableGauge**

```go
import (
    "context"
    "go.opentelemetry.io/otel"
    "go.opentelemetry.io/otel/metric"
)

func (h *Hub) registerMetrics() error {
    meter := otel.Meter("im-gateway")
    _, err := meter.Int64ObservableGauge("im.ws.active_connections",
        metric.WithDescription("Active WebSocket connections"),
        metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
            o.Observe(int64(h.connCount()))
            return nil
        }))
    return err
}
```

在 `NewHub` 末尾调用 `_ = h.registerMetrics()`。

- [ ] **Step 2：验证 metric 端点**

```bash
docker compose up -d
go run ./cmd/gateway &
sleep 20  # 等 collector 抓取一轮
curl -s http://localhost:8889/metrics | grep im_ws_active_connections
kill %1
```

- [ ] **Step 3：Commit**

```bash
git add server/internal/gateway/hub.go
git commit -m "feat(gateway): expose active_connections gauge via OTel"
```

### Task 8.5：自定义业务 metric — 消息发送计数

**Files:**
- Modify: `server/internal/service/message.go`（在 7.4 切片完成后）

- [ ] **Step 1：在 service 注册 counter**

```go
type MessageService struct {
    // ...
    sentCounter metric.Int64Counter
}

func NewMessageService(...) *MessageService {
    meter := otel.Meter("im-service/message")
    sent, _ := meter.Int64Counter("im.messages.sent",
        metric.WithDescription("Messages sent successfully"))
    return &MessageService{sentCounter: sent, ...}
}

// 在 Send 成功路径：
s.sentCounter.Add(ctx, 1, metric.WithAttributes(
    attribute.Int64("channel_id", m.ChannelID),
    attribute.Int("msg_type", m.MsgType),
))
```

- [ ] **Step 2：Commit**

```bash
git add server/internal/service/message.go
git commit -m "feat(service): im.messages.sent counter"
```

---

## Phase 9：清理与文档

### Task 9.1：确认 handler/ 已空并删除

**Files:**
- Delete: `server/internal/handler/`（store/ 与 pgx 已在 Phase 5.13 移除）
- Delete: `tests/sync_test.sh`

- [ ] **Step 1：确认 handler 目录已无文件**

```bash
ls server/internal/handler/ 2>/dev/null
```

预期：所有切片完成后目录为空。

- [ ] **Step 2：删除**

```bash
git rm -r server/internal/handler
git rm tests/sync_test.sh   # 已被 Go 集成测试覆盖
```

- [ ] **Step 3：Commit**

```bash
git commit -m "chore: remove deprecated handler package and shell tests"
```

### Task 9.2：更新 README

**Files:**
- Modify: `README.md`

- [ ] **Step 1：替换技术栈段落**（用本计划顶部 Tech Stack 内容）

- [ ] **Step 2：加运行/测试章节**

```markdown
## Development
- `make build-all` — 构建所有二进制
- `make test-unit` — 快速单元测试
- `make test-integration` — 真容器集成测试（需要 Docker）
- `make lint` / `make vuln` / `make check`
- `make mocks` — 重新生成 mockery mocks

## Observability
- 启动栈：`docker compose up -d`
- Jaeger UI：http://localhost:16686
- Prometheus 端点：http://localhost:8889/metrics
- 关闭可观测：`OTEL_DISABLED=true`
```

- [ ] **Step 3：Commit**

```bash
git add README.md
git commit -m "docs: update README for new stack"
```

### Task 9.3：codecov 阈值

**Files:**
- Create: `codecov.yml`

```yaml
coverage:
  status:
    project:
      default: { target: 75%, threshold: 1% }
    patch:
      default: { target: 80% }
ignore:
  - "**/mocks/**"
  - "**/*_mock.go"
```

- [ ] **Commit:**

```bash
git add codecov.yml
git commit -m "chore: enforce coverage thresholds"
```

### Task 9.4：归档本计划

- [ ] 在本计划文件顶部添加 `**Status: Completed YYYY-MM-DD**`
- [ ] 在 `docs/superpowers/briefings/` 写一份 1 页 wrap-up（迁移收益、踩过的坑、性能 before/after）

---

## Self-Review Checklist（执行前再过一遍）

- [ ] **Spec coverage:**
  - 质量门：✅ Phase 0
  - Gin 替换 net/http：✅ Phase 4（共存）+ Phase 6+ 各切片（逐个切换）
  - **GORM 全量替换 pgx（不共存）**：✅ Phase 5 一次性完成（5.3–5.13）
  - testify + mockery + testcontainers + httpexpect：✅ Phase 3 + Phase 5.10 + Phase 6 模板
  - OTel trace + metrics：✅ Phase 2 + Task 5.14 + Task 6.3 + Phase 8
  - 现有中间件不动：✅ Pulsar/Redis/PG/迁移工具/JWT/WS 库均保留
  - 稳步迁移：✅ 数据层一次性切（业务行为不变）+ HTTP 层切片渐进切换
- [ ] **Placeholder scan:** 无 TBD/TODO；所有 step 含可执行命令或可粘贴代码
- [ ] **Type consistency:** `Config` `Open` `User` `UserRepo` `ChannelRepo` `MessageRepo` `AuthService` `RegisterAuthRoutes` `JWTGin` `UserIDKey` 全文一致
- [ ] **GORM/pgx 不共存**：Phase 5.13 后 `go.mod` 直接依赖无 pgx；`server/internal/store/` 不存在
- [ ] **每切片删除旧 handler 文件**：6.3/7.x 均明确列出 `Delete` 项
- [ ] **每切片至少 1 单测 + 1 集成测试**：Phase 6 完整示范

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-04-23-cloud-native-migration.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, two-stage review per task, fast iteration. 适合此计划：任务多、可并行（如 Phase 0/1/3 完全独立）

**2. Inline Execution** — 当前会话顺序执行，按 phase 设 checkpoint

**建议执行节奏：**
- 第 1 周：Phase 0–3（CI、依赖、SDK、测试基础设施 — 全部不动业务，可快速合并）
- 第 2 周：Phase 4（Gin 共存外壳）+ Phase 5.1–5.10（GORM Open / models / 8 个 repo / mockery）
- 第 3 周：Phase 5.11–5.14（handler 切到 repo + 删 store/pgx + Jaeger 验证）⚠️ **关键周，全部 e2e 必须通过**
- 第 4 周：Phase 6（Auth Gin 切片，建立模板）+ Slice 7.1（profile/settings）
- 第 5–7 周：Phase 7 剩余切片，每周 2 片
- 第 8 周：Phase 8 + 9（OTel 跨服务 + 清理）

**哪种执行方式？**
