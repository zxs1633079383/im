# CLAUDE.md — internal/observability 模块级指令

> 本文件仅约束 `server/internal/observability/` 目录（OTel SDK + slog TraceHandler）。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 加载顺序：会话开始先扫 `docs/harness/`，再扫本文件。

---

## 0. 模块定位

**是什么**：im 后端 OpenTelemetry SDK + 结构化日志的**唯一装配点**。

- ✅ OTLP/gRPC trace exporter + Prometheus pull metric reader 双管线
- ✅ Resource semantic conventions（`service.name` / `service.version`）
- ✅ slog `TraceHandler` —— 把当前 span 的 `trace_id` / `span_id` 注入每条 log record
- ✅ runtime metrics（Go GC / goroutine / mem）自动开启
- ✅ `Init` 返回 `ShutdownFunc`，由 cmd 入口 `defer` 调用

**不负责**：
- ❌ 业务 metric / span 命名（业务自己 `otel.Tracer("xxx").Start(...)`）
- ❌ HTTP middleware 接线（在 `internal/middleware/` 用 `otelgin.Middleware`）
- ❌ Grafana / Jaeger 服务端拓扑（运维侧）
- ❌ 直接被 service 调用（业务通过全局 `otel.GetTracerProvider()` 拿，本包不暴露 raw exporter）

---

## 1. 影响范围

**上游依赖**：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `internal/config.ObservabilityConfig` | `internal/config/config.go` | Endpoint / Disabled / SampleRatio |
| `go.opentelemetry.io/otel/*` | go.mod | SDK + OTLP exporter + Prometheus exporter |
| `prometheus/client_golang/prometheus/promhttp` | go.mod | `/metrics` pull handler |

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| `Init` 签名 / `Config` 字段 | `cmd/gateway/main.go:53` + `cmd/message/main.go:286` 两处装配 |
| `PrometheusHandler` 暴露形态 | `cmd/gateway/main.go:188`（`/metrics` 路由）+ 运维 Grafana dashboard |
| `TraceHandler` 行为 | 全 server 进程 `slog.Default()` 输出（所有日志 trace_id 字段）|
| OTLP endpoint 默认值 | pre / prod K8s deployment env + Consul KV |

> 改本包前必看：`server/docs/BACKEND.md §十一 OTel`、`server/cmd/gateway/main.go:32-58`。

---

## 2. 功能模块清单

| 文件 | 角色 | 备注 |
|---|---|---|
| `otel.go` | `Init(ctx, Config) (ShutdownFunc, error)` + `PrometheusHandler` 全局变量 | 122 行；trace + metric + prom 三件套 |
| `slog_handler.go` | `TraceHandler` 包装任意 `slog.Handler`，注入 `trace_id` / `span_id` | 44 行；`Handle` / `WithAttrs` / `WithGroup` 三接口实现 |
| `otel_test.go` | `TestInit_DisabledNoop` + `TestInit_WithEndpoint` 两条基线 | 必须先红再绿 |
| `slog_handler_test.go` | 测 `TraceHandler` 注入逻辑 + Group / Attrs 递归 | 100% 行覆盖 |

**关键导出符号**（业务侧请用全局 `otel.*` API，**不要**反向 import 本包）：

- `observability.Init(ctx, Config) (ShutdownFunc, error)`
- `observability.Config{ServiceName, ServiceVersion, SampleRatio, Disabled, Endpoint}`
- `observability.ShutdownFunc`（`func(ctx) error`）
- `observability.PrometheusHandler`（`http.Handler`，Init 后非 nil）
- `observability.NewTraceHandler(slog.Handler) slog.Handler`

---

## 3. SOP — 写代码 → 验证 → commit

```
0. 开局：读 server/docs/BACKEND.md §十一 OTel + 本文件 + 项目根 §1.5（资源复用单例）
1. Skill(skill="go-concurrency-patterns")  ← 任何 .go 改动强制
2. TDD RED：在 otel_test.go / slog_handler_test.go 加用例 → go test ./internal/observability/... 看红
3. 写实现，必须保持 `Init` 幂等 — 重复调用应该 panic 或返回已构造的 provider；当前实现走 cmd 单次装配，**禁止改成多次 Init**
4. 验证：go test -race ./internal/observability/...
5. 验证：go vet ./internal/observability/...
6. 跨包影响 → make verify-build；改了 metric/span 命名 → make verify-integration
7. commit（见 §5）
8. 更新 SESSION.md + 必要时同步 server/docs/BACKEND.md §十一
```

**特殊路径**：

- 只改 endpoint 默认值 / Disabled 行为 → `go test ./internal/observability/... && go vet ./...` 即可
- 改 `TraceHandler` 接口 → 必须 sweep 所有 `slog.New(...)` 使用点（`grep -rn 'slog.New(' server/cmd/` 找到 2 处装配）
- 新增 metric / span → 在业务包注册（`otel.Meter("xxx").Int64Counter(...)`），本包不动；只有改 SDK 装配参数时才碰 `otel.go`

---

## 4. Pre-commit 自检

### 4.1 必跑命令

```bash
cd /Users/mac28/workspace/golangProject/im/server
go test -race ./internal/observability/...      # 单元（含 TraceHandler）
go vet ./internal/observability/...
make verify-build                                # 跨包 build 不挂
```

改了 `Init` / `Config` / `PrometheusHandler` 签名：

```bash
make verify-integration                          # cmd 装配 + /metrics endpoint 烟测
```

### 4.2 Grep gate（结果必须 0 条）

| Gate | 命令 | 含义 |
|---|---|---|
| `SetTracerProvider` 单点 | `grep -rn 'otel.SetTracerProvider(' server/ --include='*.go' \| grep -v internal/observability/otel.go` | 全 server 只有 `otel.go:82` 一处可调；业务包不许自己 SetTracerProvider |
| `SetMeterProvider` 单点 | `grep -rn 'otel.SetMeterProvider(' server/ --include='*.go' \| grep -v internal/observability/otel.go` | 同上 |
| `runtime.Start` 单点 | `grep -rn 'runtime.Start(' server/ --include='*.go' \| grep -v internal/observability/otel.go` | runtime metrics 只在 Init 启动一次 |
| 业务包直拿 raw exporter | `grep -rn 'otlptracegrpc\.New\|otlpmetricgrpc\.New' server/ --include='*.go' \| grep -v internal/observability/` | 业务不许自己 new exporter；强制走 Init |
| ServiceName 写死 | `grep -rn 'semconv.ServiceName(' server/ --include='*.go' \| grep -v internal/observability/otel.go` | resource 装配只此一处 |

### 4.3 失败处置

- 单测红 → **tdd-guide** agent；禁止"改测试让它过"
- `Init` 改后两个 cmd 入口编译红 → 同 PR 一起修；不允许只改一方
- Prometheus `/metrics` 拉不到数据 → 看 `PrometheusHandler != nil` 分支是否绕过；OTEL_DISABLED=true 会令其为 nil

---

## 5. Commit 规范

沿用 Conventional Commits + 中文 body：

```
feat(observability): xxx

<why 用中文，每行 ≤ 72 字符>
```

### 5.1 常用 scope 模板

| 场景 | 模板 |
|---|---|
| 新增 metric / span SDK 装配 | `feat(observability): 接入 runtime histogram + 暴露 /metrics` |
| 修 exporter race / shutdown 顺序 | `fix(observability): otel shutdown 加 5s 超时避免阻塞 graceful exit` |
| 升 OTel SDK 版本 | `chore(observability): 升 go.opentelemetry.io/otel v1.x.y → v1.x.z` |
| 改 slog handler 行为 | `refactor(observability): TraceHandler WithGroup 递归保留 trace_id` |

**禁止**：
- ❌ `feat(observability): add trace`（信息量 0）
- ❌ 同 commit 混 SDK 升级 + 业务 metric 注册

---

## 6. 约束规范（本层强约束）

### 6.1 单例化（对齐项目根 §1.5）

- ✅ `Init` 必须由 cmd 入口**单次调用**，单进程内禁止重复 Init
- ✅ `tp` / `mp` 通过 `otel.SetTracerProvider` / `otel.SetMeterProvider` 注册到全局
- ✅ `PrometheusHandler` 全局变量在 Init 内赋值一次，无 Mutex（cmd 启动是串行）
- ❌ **禁止**在业务代码里 `sdktrace.NewTracerProvider(...)` —— 全局只此一处
- ❌ **禁止**暴露 raw `otlptracegrpc.Exporter` / `sdktrace.TracerProvider` —— 仅暴露 `ShutdownFunc`

### 6.2 Context 透传

- ✅ `Init(ctx, ...)` 首参是 ctx，exporter dial-out 受 ctx 超时控制
- ✅ `ShutdownFunc(ctx)` 内部 `context.WithTimeout(ctx, 5*time.Second)` 兜底 —— 防 graceful exit 卡死
- ❌ 禁止 `context.Background()` 在 Init / Shutdown 内部裸用（cmd 入口可以 new root，业务层不可）

### 6.3 TraceHandler 行为锁定

- ✅ 仅当 `span.SpanContext().IsValid()` 时才注入 `trace_id` / `span_id`（无 span 不污染日志）
- ✅ `WithAttrs` / `WithGroup` 必须返回**新的** `TraceHandler` 包装，**不可** mutate self（immutability 见 `~/.claude/rules/common/coding-style.md`）
- ❌ 禁止在 `Handle` 里读写共享 state（多 goroutine 并发 log）

### 6.4 endpoint 默认值

- ✅ `Endpoint == ""` 时退回 `OTEL_EXPORTER_OTLP_ENDPOINT` env（SDK 内置回退）
- ✅ `Disabled == true` 走 noop shutdown，**不** dial-out exporter（CI / 离线开发友好）
- ❌ 禁止把 endpoint 写死在 const 里（pre / prod / dev 走 Consul / env）

---

## 7. 对应 Harness 映射

本模块**无直接 harness**，但与下列 harness 共享原则：

| 关联 | 来源 | 关系 |
|---|---|---|
| 项目根 `CLAUDE.md §1.5` 资源复用单例 | 全局 | `Init` = sync.Once 模型；exporter / tracer 全局共享 |
| 项目根 `CLAUDE.md §1.4` Context 透传 | 全局 | `Init(ctx, ...)` + `ShutdownFunc(ctx)` 首参约束 |
| [C006](../../docs/harness/C006-httpexpect-query-encoding.md) | 间接 | 集成测试拉 `/metrics` 时用 `.WithQuery`，不拼 `?` |
| [C015](../../docs/harness/C015-testcontainers-redis-port-race.md) | 间接 | OTel 也跑 testcontainers 端口（pre 联调时），同样要走 retry helper |

> 若未来跨进程 trace 链路出现 ≥ 3 次断链问题，立刻按 `docs/harness/TEMPLATE.md` 起草 drafting 条目并指回本文件。

---

## 8. Update / Insert 规则

### 8.1 新增 metric / span（**绝大多数**情况）

业务侧操作，**不动本包**：

```go
// 业务包内 e.g. internal/service/message/sender.go
var sendCounter = otel.Meter("im/message").
    Int64Counter("im_message_send_total")
sendCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", "text")))
```

完成后：
- 在 `server/docs/BACKEND.md §十一 OTel` 表格补一行（metric 名 / 类型 / label / 来源文件）
- 同步 Grafana dashboard JSON（运维侧 PR）

### 8.2 改 SDK 装配参数（罕见）

如：换 sampler / 加 BatchSpanProcessor 调优 / 接入 OTLP/HTTP 备线 ——

1. 先在 `otel_test.go` 加用例（覆盖新参数路径）
2. 改 `otel.go` `Init` 实现
3. `go test -race ./internal/observability/...`
4. 改 `ObservabilityConfig`（在 `internal/config/config.go`）+ `config.example.yaml` 同步
5. cmd 入口 `cmd/gateway/main.go` + `cmd/message/main.go` 装配点同步
6. 更新 `server/docs/BACKEND.md §十一 OTel`
7. commit scope = `observability`

### 8.3 新增 slog Handler 装饰器（罕见）

如：加 sampling / sanitize PII / 限速 ——

1. 新建 `internal/observability/<name>_handler.go` + `<name>_handler_test.go`
2. 必须实现完整 `slog.Handler` 四接口（`Enabled` / `Handle` / `WithAttrs` / `WithGroup`）
3. `WithAttrs` / `WithGroup` 必须返回新实例（immutability）
4. cmd 入口装配：`slog.New(<name>.New(observability.NewTraceHandler(baseHandler)))`
5. 100% 行覆盖单测

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | §1.5 资源复用 / §1.6 项目特有约束 |
| server/ 入口 CLAUDE.md | `../../CLAUDE.md` | §6.1 cmd 装配铁律 |
| 后端契约 §十一 OTel | `../../docs/BACKEND.md` | metric / span 命名表 + 上线检查 |
| Grafana dashboard | 运维仓 `ops/grafana/im-*.json` | metric 落地后同步 panel |
| Harness 索引 | `/Users/mac28/workspace/golangProject/im/docs/harness/README.md` | 关联 C006 / C015 |
| OTel 知识 skill | `~/.claude/skills/opentelemetry-knowledge/` | SDK / Collector / 语义约定深度参考 |
| Prometheus 知识 skill | `~/.claude/skills/prometheus-knowledge/` | metric 类型 / PromQL / scrape 配置 |
| Jaeger 知识 skill | `~/.claude/skills/jaeger-knowledge/` | trace 后端 / 采样策略 |

---

> 维护：本模块改动频率低（SDK 版本升 + 偶尔调 sampler）。每次升级 `go.opentelemetry.io/otel` 主版本时同步审本文件 §1 / §2 / §8 三节。
