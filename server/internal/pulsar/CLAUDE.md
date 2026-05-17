# CLAUDE.md — `server/internal/pulsar/` 模块指令

> 模块级 Claude 指令，优先级低于项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md`、高于默认行为。
> 触达本目录任意 `*.go` 必须先读完本文件 + harness [C002](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) + [C003](../../../docs/harness/C003-pulsar-topic-localname-suffix.md)。

---

## 0. 模块定位

本目录是 **im 后端访问 Pulsar 的唯一接入层**：

- **职责一**：Pulsar `*pulsarclient.Client` 的连接所有者（`Client.inner`）；进程内 **`sync.Once` 单例**（由 `cmd/gateway/main.go` / `cmd/message/main.go` 各自 `pulsar.New(...)` 一次，注入下游不再重复 `New`）。
- **职责二**：暴露**最小可用**的 `Producer` / `Consumer` 抽象 —— JSON 序列化 + OTel propagator inject/extract + Pulsar broker→consumer lag histogram。
- **职责三**：暴露 `pulsarMetrics`（OTel histogram `im.pulsar.consume.lag.duration`）供运维 / Grafana 观察。

**本模块不做**的事（**死规矩**）：

- ❌ **不直接发业务消息**：跨 pod push 由 `gateway.CrossPodPush` / `CrossPodBroadcast` 编排，本层只是基础设施（详见 §6 + harness C002）。
- ❌ **不缓存 producer**：`ProducerCache`（LRU 256）落在 `server/internal/gateway/producer_cache.go`，本模块只 `Client.NewProducer(topic)` 出 raw `*Producer`，cache 复用是 gateway 层职责。
- ❌ **不拼 topic 字符串**：topic 命名由 `gateway.PushTopicFor(gatewayID, env)` 决定（harness C003），本模块对 topic 字面量**完全不关心**，只接收 caller 传入的 string。
- ❌ **不拼 envelope**：`PulsarPushEnvelope` 结构 + `TargetUIDs` 列表在 `gateway/cross_pod_push.go`，本模块只把 `payload any` JSON 化后投递，对 schema 无知。

一句话：**本模块 = "Pulsar 客户端 SDK 的 im 化薄封装"**，业务语义 0。

---

## 1. 影响范围

| 方向 | 调用方 / 被调方 | 关键文件 |
|---|---|---|
| **上游（调用本模块）** | `server/cmd/gateway/main.go` —— 启动时 `pulsar.New()` + 建 ProducerCache + 起 PushConsumer | `cmd/gateway/main.go:139-212` |
| 上游 | `server/cmd/message/main.go` —— worker 启动时 `pulsar.New()` + 共享 ProducerCache | `cmd/message/main.go:68-339` |
| 上游 | `server/internal/gateway/cross_pod_push.go` —— 通过 ProducerCache 拿 `*Producer` 后调 `Send(ctx, key, payload)` | C002 §3 |
| 上游 | `server/internal/gateway/push_consumer.go` —— 通过 `Client.NewConsumer(topic, sub, handler)` 订阅 push topic | C003 §3 |
| **下游（本模块依赖）** | `github.com/apache/pulsar-client-go/pulsar` —— 官方 SDK，禁止 fork / 内嵌补丁 | `go.mod` |
| 下游 | OTel：`go.opentelemetry.io/otel` —— tracer (`im-pulsar`) + meter (`im-pulsar`) | client.go:19 / metrics.go:31 |
| **运行依赖** | Pulsar broker：本地走 `docker-compose` (`docker/pulsar-init.sh`，tenant=`im`、namespace=`im/push-local`)；pre/prod 走 k8s | docker/pulsar-init.sh:16-20 |

**爆炸半径**：本模块改坏 → gateway / message worker 启动崩 → 所有跨 pod 推送链路断 → "异 pod 用户消息全丢"。改动前必跑 `gitnexus_impact({target: "Client.NewProducer", direction: "upstream"})`。

---

## 2. 功能模块清单

| 文件 | 行数 | 职责 | 关键 export |
|---|---|---|---|
| `client.go` | 193 | Pulsar 连接所有者 + Producer / Consumer 抽象 + OTel propagator inject/extract | `New(url, log)` / `Client.NewProducer(topic)` / `Client.NewConsumer(topic, sub, handler)` / `Producer.Send(ctx, key, payload)` / `Consumer.Consume(ctx)` / `HandlerFunc` |
| `metrics.go` | 41 | OTel metrics 单例（`sync.Once` 模式样本） | `metrics()` —— 内部 getter，返回 `*pulsarMetrics{ConsumeLag}` |

**两个文件加起来 < 250 行**。任何 PR 把本目录撑到 > 400 行（参见 `~/.claude/rules/golang/coding-style.md` 单文件硬限）→ 拆文件，不放任。

**不允许新增**的文件类型（除非 Spec 拍板）：
- `topic.go` —— topic 命名归 `gateway/topic.go`（C003），本目录禁止重复实现。
- `envelope.go` —— envelope schema 归 `gateway/`，本目录禁止定义业务结构体。
- `producer_cache.go` —— LRU 缓存归 `gateway/producer_cache.go`，本目录只出 raw producer。

---

## 3. SOP — 在本模块做改动 / 加东西

### 3.1 增加一个新的 **Pulsar topic family**（如新增 `msg.retry.*` 类）

> 注意：**topic family 定义不在本模块**。本模块只是"水管"。

正确流程：

1. 在 `server/internal/gateway/topic.go` 加常量 + 加一个新的 `XxxTopicFor(id, env)` 函数（对照 `PushTopicFor`）。
2. caller 拿到 topic 字符串后，**直接** `client.NewProducer(topic)` / `client.NewConsumer(topic, sub, handler)` —— 本模块代码**不需要改**。
3. 如果新 topic 需要 `im/<new-ns>` namespace，去 `docker/pulsar-init.sh` 加 `pulsar-admin namespaces create im/<new-ns>` 一行；pre/prod 同步动 k8s ConfigMap + Terraform。
4. 集成测试 `server/tests/integration/m4_pulsar_*_test.go` 加 case 覆盖新 topic。
5. 走 harness 复核：C003 §1 触发场景已经覆盖"任何会构造 topic 的代码"，新 family 自动入网。

### 3.2 改 Producer / Consumer 行为（如加 batch / 加 dead-letter）

1. 先在 SESSION.md §3 写"待用户拍板的分叉"：列出现状 vs 提案 vs 影响面。
2. 用户拍板后，改 `client.go` 对应方法，**保持 export 签名不变**（避免 cascade 改所有 caller）。
3. 跑 `go test -race ./internal/pulsar/...`（本目录单测）+ `go test -race ./internal/gateway/...`（上游 cross_pod_push / push_consumer）。
4. 跑 `make verify-all`：包含 harness C002 / C003 的 grep gate 检查。
5. 改了 OTel 字段？同步改 `server/docs/BACKEND.md §十一 OTel` + Grafana dashboard `deploy/grafana/im-fanout.json`。

### 3.3 改 metrics

1. 新 histogram / counter 加到 `metrics.go` 的 `pulsarMetrics` struct 字段。
2. **必须**用 `sync.Once` 单例 pattern（已经是的，照搬）；禁止"每个 Producer / Consumer 创建一次 meter"。
3. 在 `client.go` 对应位置 `metrics().XxxCounter.Add(ctx, 1, otelmetric.WithAttributes(...))` 上报。
4. 更新 Grafana dashboard + alerting rule。
5. 单测覆盖 metrics 上报路径（用 `sdkmetric` test reader 验 `Aggregations`）。

---

## 4. Pre-commit 自检清单

任何对 `server/internal/pulsar/*.go` 的改动，commit 前必须**逐条**过：

```bash
# ① 单测全绿 + race clean
go test -race ./server/internal/pulsar/...

# ② `pulsar.New(` / `pulsarclient.NewClient(` 全仓只在本模块出现（singleton 约束）
grep -rEn 'pulsarclient\.NewClient\(' /Users/mac28/workspace/golangProject/im/server --include='*.go' \
  | grep -v 'server/internal/pulsar/client.go'
# 期望输出：空（0 行）

# ③ business 路径不允许直接 New Producer（必须经 gateway.ProducerCache）
grep -rEn '\.NewProducer\(' /Users/mac28/workspace/golangProject/im/server/internal/service/ \
  /Users/mac28/workspace/golangProject/im/server/internal/http/ --include='*.go' | grep -v '_test.go'
# 期望输出：空（0 行）

# ④ business 路径不允许直接拼 pulsar.ProducerMessage（harness C002 §4.1）
grep -rEn 'pulsarclient?\.ProducerMessage\{' /Users/mac28/workspace/golangProject/im/server/internal/service/ \
  /Users/mac28/workspace/golangProject/im/server/internal/http/ /Users/mac28/workspace/golangProject/im/server/cmd/ \
  --include='*.go' | grep -v '_test.go' | grep -v 'gateway/cross_pod_push.go'
# 期望输出：空（0 行）

# ⑤ 本地 dev 启动 smoke：topic 名包含 USER / HOSTNAME 后缀（harness C003）
USER=zlc IM_ENV=local go run ./server/cmd/gateway 2>&1 | head -20 | grep 'push-local/msg.push.*\.zlc'
# 期望：能 grep 到 dev-suffix；漏 USER 后缀 → 退回 C003 §3 检查

# ⑥ golangci-lint 零告警 + gofmt 干净
gofmt -l server/internal/pulsar/ ; golangci-lint run ./server/internal/pulsar/...
```

**任意一条非空 / 非零 exit code → 拒绝 commit**。

补充软检：

- [ ] 改 `client.go` 任意 export 签名？跑 `gitnexus_impact({target: "<symbol>", direction: "upstream"})`，HIGH / CRITICAL 必告警用户。
- [ ] 加新 metric？验 Grafana dashboard 已经能查到新 series（用 `curl /metrics | grep im_pulsar_`）。
- [ ] 改 OTel span 名？同步改 `server/docs/BACKEND.md §十一`。

---

## 5. Commit 规范

遵循项目根 `CLAUDE.md §3` + `~/.claude/rules/common/git-workflow.md`：

```
feat(pulsar): <一句中文描述，≤ 50 字>

<可选 body：解释 why，不解释 what；每行 ≤ 72 字符>
```

`type` 取值（本模块常用）：

- `feat(pulsar):` —— 加新方法 / 新 metric / 新功能
- `fix(pulsar):` —— 修 race / 修 OTel span 链断 / 修 producer 泄漏
- `refactor(pulsar):` —— 重构（不改 export 签名）
- `perf(pulsar):` —— 性能优化（必须带 benchmark before/after）
- `test(pulsar):` —— 只加测试
- `chore(pulsar):` —— 升 pulsar-client-go 版本 / 调 go.mod
- `docs(pulsar):` —— 改本 CLAUDE.md / godoc

**示例**：

```
feat(pulsar): Producer.Send 注入 OTel context 到 msg.Properties

把 producer span 的 SpanContext 通过 TextMapPropagator inject 到
Properties，consumer 侧 Extract 后形成同一条 trace。
联动 BACKEND.md §十一 OTel 设计与 Grafana service map。
```

```
fix(pulsar): Consumer.Consume 在 ctx 取消时返回 nil 不报错

否则 cmd/gateway 在 SIGTERM 时 push_consumer.Consume 会上报
"receive: context canceled" 误告警，干扰 SLO。
```

**禁止**：
- ❌ `update client.go` / `modify metrics` 这类无信息量 message
- ❌ 跨 type 混改（如同一 commit 既加 feat 又 fix bug → 拆两个 commit）
- ❌ commit message 里写"AI 生成 / Co-authored-by Claude" 之类 attribution
- ❌ 跳过 `go test -race` 直接 commit

---

## 6. 约束规范（铁律）

### 6.1 Pulsar Client = `sync.Once` 单例（根 CLAUDE.md §1.5）

- `*pulsar.Client` **每进程一个**。`pulsar.New(url, log)` 只在 `cmd/gateway/main.go` / `cmd/message/main.go` 各调一次，结果通过依赖注入传给下游。
- 本模块代码本身不必持有全局变量（已经是这样：`Client` 是 value type，所有权在 cmd 层）。但 **caller 不准** 在 service / handler / repo 里 `pulsar.New(...)`。
- 验证：见 §4 第 ② 条 grep。`pulsarclient.NewClient(` 全仓只出现在 `client.go:32` 一处。

### 6.2 Topic 命名必经 `gateway.PushTopicFor`（harness C003）

- 本模块 `Client.NewProducer(topic)` / `Client.NewConsumer(topic, sub, handler)` 接收一个 string，**不校验**它的格式 —— 校验是 C003 grep gate 的事。
- 但作为本模块 owner，**你**有义务在 PR review 时拦下任何 caller 传 `"persistent://im/push/..." + xxx` 硬编码字符串的提案 —— 必须改走 `PushTopicFor`。
- **本地 dev 必带 USER / HOSTNAME 后缀**：多人共享 Pulsar 时，缺 dev-suffix 会窜台（C003 §5 复现 #3）。`PushTopicFor` 已经强制 `USER > HOSTNAME > "anon"` 三级 fallback —— 不要在本模块绕过这层。

### 6.3 跨 pod 推送只走 `gateway.CrossPodPush` / `CrossPodBroadcast`（harness C002）

- **本模块不发业务消息**。任何"x service 想推一条 push_msg 给用户 Y" 的代码路径，**绝不**直接拿 `*pulsar.Producer.Send(...)`。
- 正确链路：`service` → `gateway.Hub.CrossPodPush(ctx, args)` → `ProducerCache.GetOrCreate(topic)` → `*pulsar.Producer.Send(...)` → 本模块 OTel-instrumented Send → broker。
- 本模块是这条链路的"水管末端"，不感知业务 envelope / TargetUIDs / push_id ack 队列等概念。
- 验证：见 §4 第 ③ ④ 条 grep。`service/` `http/` 不允许出现 `.NewProducer(` 或 `pulsarclient.ProducerMessage{`。

### 6.4 Producer / Consumer 必带 `ctx` + 超时（根 CLAUDE.md §1.4）

- `Producer.Send(ctx, key, payload)` 首参 `ctx`，caller **必须**传 `WithTimeout` 后的 ctx —— 否则 Pulsar broker 卡死 → goroutine 永久阻塞。
- `Consumer.Consume(ctx)` 用 ctx 做退出信号 —— 已经实现 `if ctx.Err() != nil { return nil }`，禁止改成 `panic` 或忽略。
- **业务层禁止 `context.Background()`**：本模块 export 函数都接受 caller 的 ctx，自己不 `Background()` 兜底。

### 6.5 Producer / Consumer 必显式 Close

- `Producer.Close()` / `Consumer.Close()` / `Client.Close()` 在进程退出时**必须**被调（`cmd/gateway/main.go:140` 已 `defer producerCache.Close()`）。
- 加新资源持有方？同步加 defer Close。漏 close → Pulsar broker 侧连接泄漏 + 本进程 fd 泄漏。

### 6.6 不引入新依赖

- 本模块只允许依赖：`pulsar-client-go` / `go.opentelemetry.io/otel*` / 标准库。
- 想加 retry / circuit breaker / dead-letter queue？**先在 SESSION.md §3 提案**，用户拍板，再走 RFC 加依赖。直接 PR 引入新 library → 拒绝合并。

---

## 7. 对应 Harness 映射

| Harness | 触发场景（grep / 文件 glob） | 本模块的位置 | 验证手段 |
|---|---|---|---|
| **[C002](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md)** 跨 pod 推送走 `gateway.CrossPodPush` | `service/**/*.go` / `http/**/*.go` / `cmd/**/*.go` 出现 `pulsarclient.ProducerMessage{` / `.PushToUser(` | 本模块是 C002 的"水管末端"——禁止业务路径绕过 gateway 直发 | §4 第 ③ ④ 条 grep + `tests/integration/m4_ws_cross_pod_test.go` |
| **[C003](../../../docs/harness/C003-pulsar-topic-localname-suffix.md)** Pulsar topic 必经 `PushTopicFor` + 本地 USER 后缀 | `*.go` 出现 `persistent://im/push` 字符串字面量 / `pulsar.ConsumerOptions{` / `pulsar.ProducerOptions{` | 本模块 `NewProducer(topic)` / `NewConsumer(topic, ...)` 是 sink，不校验，依赖 C003 grep gate 在 caller 侧拦 | `topic_test.go`（位置在 `gateway/`，不在本模块）+ §4 第 ⑤ 条 dev-suffix smoke |

**新增 harness 触发场景**：

- 如果本模块再被复现 ≥ 3 次"业务直接 New Producer"事故 → 升级新 harness `C{NNN}-pulsar-no-direct-newproducer.md`（目前 C002 已覆盖，无需新建）。
- 如果 OTel span 链断 ≥ 3 次 → 升级 harness `C{NNN}-pulsar-otel-propagator-required.md`。

---

## 8. Update / Insert 规则（新增 topic family 完整路径）

新增一个 Pulsar topic family（例如 `msg.retry.*` 用于消息投递失败重试）的**完整 7 步**：

1. **`server/internal/gateway/topic.go`**：加常量 `MsgRetryTopicPrefix = "persistent://im/retry/msg.retry."`；加函数 `MsgRetryTopicFor(gatewayID, env) string`（对照现有 `PushTopicFor`，env 分支 + dev-suffix 三件套）。
2. **`server/internal/gateway/topic_test.go`**：加 5 个对应单测（prod / pre / local-USER / local-HOSTNAME / local-anon）。
3. **`docker/pulsar-init.sh`**：第 20 行后追加 `bin/pulsar-admin namespaces create im/retry 2>/dev/null || echo "..."`。
4. **caller** 在 `cmd/message/` 或 `cmd/gateway/` 拿 topic → `client.NewProducer(topic)` / `client.NewConsumer(topic, sub, handler)` —— **本模块（`server/internal/pulsar/`）代码不变**。
5. **`server/tests/integration/m4_pulsar_<family>_test.go`**：起 docker-compose Pulsar → 投 → 收 → 验 OTel trace 链 → 验 dev-suffix 隔离。
6. **`server/docs/BACKEND.md §5`** + **`server/docs/BACKEND.md §3.2`** 同步加 topic family 说明 + env 矩阵。
7. **harness C003 §4.1 grep**：本来就覆盖"任何 `persistent://im/push` 字面量必经 `PushTopicFor`" —— 新 family `persistent://im/retry` 也走相同 pattern，**确认 grep 正则需要扩展**（如改为 `persistent://im/`）→ 同步改 `verify-all` Makefile target。

**Insert（新 export 函数）**：

- 加在 `client.go` 末尾，保持文件分区顺序（Client → Producer → Consumer → Helper）。
- 加 godoc 注释（`~/.claude/rules/golang/coding-style.md` checklist 强制）。
- 加单测；race 必过。

**Update（改现有 export 行为）**：

- 改前先 `gitnexus_impact` 跑爆炸半径。
- 改后 `gitnexus_detect_changes` 复核。
- 不准默默改语义 —— 改完更新本 CLAUDE.md §2 表格 + godoc。

---

## 9. 文档关联

| 文档 | 用途 | 关联本模块的小节 |
|---|---|---|
| [`server/docs/BACKEND.md`](../../docs/BACKEND.md) | M1-M6 后端契约 | §3.2 Pulsar topic 命名规则、§5 跨 pod 推送链路、§十一 OTel 设计 |
| [`docker-compose.yml`](../../../docker-compose.yml) | 本地 Pulsar 服务定义 | service `pulsar`（standalone 模式） |
| [`docker/pulsar-init.sh`](../../../docker/pulsar-init.sh) | Pulsar standalone 启动 + 建 tenant/namespace | tenant `im`、namespace `im/push-local`（新加 family 走第 §8.3 步同步） |
| [`docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md`](../../../docs/harness/C002-cross-pod-push-must-go-gateway-crosspodpush.md) | 跨 pod 推送唯一入口 | §3 Required、§4.1 grep、§4.3 单测 |
| [`docs/harness/C003-pulsar-topic-localname-suffix.md`](../../../docs/harness/C003-pulsar-topic-localname-suffix.md) | Topic 命名 + 本地 dev-suffix | §3 PushTopicFor 实现、§4 grep gate、§5 三次复现 |
| [`~/.claude/skills/go-concurrency-patterns/SKILL.md`](~/.claude/skills/go-concurrency-patterns/SKILL.md) | Go 并发唯一标准 | §1.5 资源复用 sync.Once、§1.4 Context 贯穿、§1.7 错误处理 |
| [`~/.claude/rules/golang/coding-style.md`](~/.claude/rules/golang/coding-style.md) | Go 风格硬限 | 函数 ≤ 60 行 / 文件 ≤ 400 行 / 禁裸 panic / 禁吞错 |
| [`server/Makefile`](../../Makefile) | `verify-all` / `verify-unit` / `verify-integration` | 必含 C002 / C003 grep gate |
| 项目根 [`CLAUDE.md §1.6`](../../../CLAUDE.md) | 项目特有约束 | 「Pulsar topic 命名 `PushTopicFor` + 本地后缀」摘要，本目录是详细版 |

---

## 10. 一句话总结

> **本模块是"Pulsar 客户端 SDK 的 im 化薄封装"，对外只暴露 `Client` / `Producer` / `Consumer` 三个抽象 + 1 个 lag histogram；不感知 topic 命名、不感知 envelope schema、不感知 push 业务语义；任何想在这里塞业务逻辑的提案 → 直接打回 `gateway/` 层。**
