# CLAUDE.md — internal/deps 模块级指令

> 本文件仅约束 `server/internal/deps/` 目录。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。

---

## 0. 模块定位

**当前是什么**：build-tag `deps_pin` 下的**依赖钉子**文件 —— 用 `_ "import"` 把 go.mod 里**未来 Phase 才会真用**的库锚住，防止 `go mod tidy` 把它们清掉。**零运行时开销**（`-tags=deps_pin` 才会编译）。

**演进方向**（**正在发生**）：本目录将逐步成为 im 后端的 **DI（Dependency Injection）container 集中地**：

- ✅ 所有基础设施 client（PG / Redis / Pulsar / HTTP / OTel）的**单例构造**集中此处
- ✅ `cmd/gateway/main.go` + `cmd/message/main.go` 启动期**只调用一次** `deps.Build(ctx, cfg)`
- ✅ 注入到 `service` / `gateway` / `repo` 各层 —— 业务层不感知构造细节
- ✅ 单一 `Shutdown` 出口，按依赖反向顺序关停

**不负责**：

- ❌ 业务逻辑（service / handler 改动不进本包）
- ❌ 协议 / wire schema（在 `internal/types/`）
- ❌ 自己声明 config 字段（读 `internal/config.Config`，不重复定义）
- ❌ 任何全局可变 var（单例构造产物注入参数，不暴露 package-level var）

---

## 1. 影响范围

**上游依赖**：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `internal/config.Config` | `internal/config/` | 单例构造的输入 |
| `internal/observability` | `internal/observability/` | OTel Init 在 cmd 早于 deps.Build |
| 各基础设施 SDK（go-redis / pulsar-client-go / pgx / gorm） | go.mod | 单例化目标 |

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| 新增基础设施单例（如 ES / Kafka） | `cmd/gateway/main.go` + `cmd/message/main.go` 装配点 + 注入到下游 service / repo |
| 删 / 改 `deps_pin` 锁定列表 | `go.mod` `tidy` 后可能丢库；CI build-tag 化测试会红 |
| `Shutdown` 顺序变更 | graceful exit 行为；订单顺序错 → in-flight message drop / Pulsar producer 残留 |

> 改本包前必看：`server/CLAUDE.md §6.2 cmd 入口专属约束`、项目根 §1.5 资源复用单例。

---

## 2. 功能模块清单

**当前**：

| 文件 | 角色 | 备注 |
|---|---|---|
| `deps.go` | `//go:build deps_pin` 锁定 12 个未来 Phase 用到的库（httpexpect / testcontainers / OTel 全套 / gorm 等） | 29 行；零运行时开销 |

**演进后（目标态）**：

| 文件 | 角色 |
|---|---|
| `deps.go` | `Container` struct + `Build(ctx, *config.Config) (*Container, error)` + `Shutdown(ctx)` |
| `pg.go` | PG pool 单例（pgxpool 或 gorm.DB）+ sync.Once 兜底 |
| `redis.go` | Redis client / cluster client（按 `cfg.Redis.Cluster` 分支）|
| `pulsar.go` | Pulsar client + producer pool（按 topic 复用 producer，topic 经 `PushTopicFor`）|
| `http.go` | 共享 `*http.Client` + `*http.Transport`（显式超时、连接池）|
| `deps_test.go` | 单测：每个单例幂等 + Shutdown 顺序 + 失败回滚 |

**关键导出符号（目标态）**：

- `deps.Container{PG, Redis, Pulsar, HTTP, GatewayID, ...}`
- `deps.Build(ctx, *config.Config) (*Container, error)` —— 必须幂等可重入（实际 cmd 只调一次，但单测可多次）
- `deps.Container.Shutdown(ctx) error` —— 倒序关停

---

## 3. SOP — 写代码 → 验证 → commit

**接入新基础设施的标准流程**：

```
0. 开局：读 docs/harness/C010 + C015 + 项目根 §1.5 资源复用 + 本文件
1. Skill(skill="go-concurrency-patterns")  ← 任何 .go 改动强制
2. 先在 internal/config 加对应 Config 字段（走配置模块 SOP）+ example yaml + applyDefaults
3. TDD RED：deps_test.go 加用例 ——
   - 构造成功路径
   - Config 字段非法 → error wrap
   - Build 失败时已构造的依赖必须 Shutdown 释放
   - Shutdown 倒序释放
4. 在 deps/<name>.go 实现单例构造（sync.Once 模式或函数返回）
5. 改 Container struct 加字段；Build 内串联
6. 改 Shutdown 加倒序释放
7. go test -race ./internal/deps/...
8. grep gate 自查（见 §4.2）
9. cmd/gateway/main.go + cmd/message/main.go 改装配：拿 cfg → deps.Build → 注入到 service / handler
10. make verify-all
11. commit（见 §5）
```

**注入风格约束**：

- ✅ 构造器注入：`service.NewMessageSender(deps.PG, deps.Pulsar)`
- ❌ **禁止**全局 `var Pulsar *pulsar.Client` 让 service 直接拿
- ❌ **禁止**`service.GetPulsar()` 这种隐式拿（service 不该知道有 deps 这个东西）

---

## 4. Pre-commit 自检

### 4.1 必跑命令

```bash
cd /Users/mac28/workspace/golangProject/im/server
go test -race ./internal/deps/...
go vet ./internal/deps/...
go build -tags=deps_pin ./internal/deps/...    # build-tag 锁定路径必须能编译
make verify-build                              # 普通 build 不挂
```

接入新基础设施：

```bash
make verify-integration                        # 单例构造 + 真实 client dial 烟测
```

### 4.2 Grep gate（结果必须 0 条 / 见每条说明）

| Gate | 命令 | 期望 |
|---|---|---|
| `new<Client>(` 单例构造点 | `grep -rEn 'pulsar\.NewClient\|redis\.NewClient\|redis\.NewClusterClient\|pgxpool\.New\|gorm\.Open\(' server/ --include='*.go' \| grep -v 'internal/deps/\|_test.go'` | **= 0** — client 只在 deps 包内构造 |
| service / handler 自己 `os.Getenv` | `grep -rn 'os.Getenv(' server/internal/{service,handler,repo,gateway,middleware}/ --include='*.go'` | **= 0** — 走 config 包 |
| 全局可变 var 暴露 client | `grep -rEn '^var [A-Z][A-Za-z0-9]*\s+\*?(pulsar\|redis\|http)\.Client' server/internal/deps/ --include='*.go'` | **= 0** — 通过 `Container` 字段而非全局 var |
| `IM_REDIS_CLUSTER=true` 出现次数（C010 §4.6） | `grep -nE 'IM_REDIS_CLUSTER\s*=\s*true\|"IM_REDIS_CLUSTER=true"' server/Makefile server/scripts/run-all-dev.sh deploy/k8s/20-deployment.yaml` | **≥ 4 条** |
| 集成测试用 testcontainers 不 retry（C015） | `grep -rn 'redisContainer.MappedPort\|\.MappedPort(' server/ --include='*_test.go' \| grep -v 'WithRetry\|MappedPortWithRetry'` | **= 0** — 必须经 retry helper |

### 4.3 失败处置

- `deps_test.go` 红 → **tdd-guide** agent
- `make verify-integration` 红 + race fail → C015 retry helper 兜底
- cmd 装配编译挂 → 同 PR 修，禁止只改 Container 不改 cmd

---

## 5. Commit 规范

沿用 Conventional Commits + 中文 body：

```
feat(deps): xxx

<why 用中文，每行 ≤ 72 字符>
```

### 5.1 常用 scope 模板

| 场景 | 模板 |
|---|---|
| 接入新基础设施 | `feat(deps): 接入 ES client 单例 + Shutdown 倒序释放` |
| 修单例 race | `fix(deps): pulsar producer pool 加 sync.Once 防多 cmd 并发 init` |
| 调整 Shutdown 顺序 | `refactor(deps): Shutdown 顺序改为 service→gateway→pulsar→DB→redis` |
| 锁库版本 | `chore(deps): deps_pin 锁定 go-redis v9 转 cluster API` |

**禁止**：
- ❌ `feat(deps): add stuff`
- ❌ 同 commit 混 deps 装配 + 业务消费（拆两 commit）

---

## 6. 约束规范（本层强约束）

### 6.1 单例铁律（项目根 §1.5 + 本层加严）

- ✅ 所有 client 单例化：`pulsar.Client` / `redis.Client` / `redis.ClusterClient` / `pgxpool.Pool` / `*http.Client`
- ✅ 单例构造走 `sync.Once` 或在 `Build` 内串行构造一次（cmd 入口只调一次 Build，无并发 race）
- ✅ 失败回滚：`Build` 中途某依赖 init 红 → 必须 Shutdown 已构造的依赖再返回 error
- ❌ **禁止**业务 service / handler / repo 内 `pulsar.NewClient(...)` —— 必须从注入参数拿
- ❌ **禁止**新增 package-level `var ImClient *xxx.Client` 全局 var —— 通过 `Container` 字段传递

### 6.2 没有 deps 字段不许直接全局 var

任何"我想搞个全局 X 让多处用"的冲动 → 先问：

1. 这玩意是 client / pool 这类有 lifecycle 的？→ 进 `Container` 字段
2. 这玩意是纯常量？→ 进 `internal/config` 或对应业务包 const
3. 这玩意是缓存？→ 进 `internal/cache` 单独构造，仍由 deps 管 lifecycle

**绝对禁止**：`var defaultClient = http.Client{}` 这种隐式全局。

### 6.3 Shutdown 顺序（**锁死**）

按"消费侧 → 生产侧 → 持久化侧"反向释放：

```
1. cancel rootCtx（让所有 goroutine 收到 Done 信号）
2. service 层 Drain（in-flight HTTP 请求 / WS handler 跑完）
3. gateway 层 Close（关 WS hub / drain Pulsar consumer）
4. pulsar producer.Close + pulsar client.Close
5. DB pool.Close（pgxpool / gorm.DB）
6. redis client.Close
7. observability.Shutdown（最后 flush trace / metric）
```

**为什么这个顺序**：上游先停（无新流量进来），中游 drain（处理完 in-flight），下游最后关（保证 in-flight 能落库 / 发 Pulsar）。

- ❌ 禁止把 redis.Close 放到 pulsar.Close 之前 —— pulsar producer drain 时可能要读 redis routing key
- ❌ 禁止把 observability.Shutdown 放最前 —— 之后所有 Close 行为的 trace 会丢

### 6.4 测试纪律

- 100% 行覆盖（go test 覆盖 `Build` / 每个单例构造 / `Shutdown`）
- `go test -race` 必绿
- 集成测试用 testcontainers → 必经 C015 retry helper

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| 项目根 `CLAUDE.md §1.5` 资源复用 | 任何新基础设施接入 | `sync.Once` 单例 + grep gate（§4.2） |
| 项目根 `CLAUDE.md §1.6` AllocSeqAndInsert / CrossPodPush | PG / Pulsar / Redis client 都从 deps 拿 | 业务包不许直接构造 client |
| [C010](../../../docs/harness/C010-userdata-resolve.md) §4.6 | Redis client cluster vs single 分支 | `cfg.Redis.Cluster` 控制；本地 dev 必经 `IM_REDIS_CLUSTER=true` |
| [C015](../../../docs/harness/C015-testcontainers-redis-port-race.md) | 集成测试用 testcontainers 起 redis / pg / pulsar | `MappedPortWithRetry` 兜底 |
| [C003](../../../docs/harness/C003-pulsar-topic-localname-suffix.md) | Pulsar producer 构造时 topic 命名 | 经 `PushTopicFor(gatewayID, env)`；本地必带 USER/HOSTNAME 后缀 |

> 当未来 deps 真正落地为 DI container 后（不再只是 deps_pin 文件），从 §1 影响范围 + §6 单例铁律高频踩坑可起草 drafting harness。

---

## 8. Update / Insert 规则

### 8.1 接入新基础设施（如 ES / Kafka / S3）

1. **先在 `internal/config`** 加对应字段（走配置模块 SOP）
2. 在 `deps/` 新建 `<name>.go`（如 `es.go`）—— 单例构造 + 错误 wrap
3. `Container` struct 加字段（`ES *elasticsearch.Client`）
4. `Build` 内串联构造 + 失败回滚
5. `Shutdown` 按 §6.3 顺序加 close 调用
6. `deps_test.go` 加 4 类用例：成功 / Config 非法 / 中途失败回滚 / Shutdown 倒序
7. cmd/*/main.go 装配点拿到 `deps.Container.ES` → 注入到 service / repo
8. 更新 docs/ARCHITECTURE.md 加新依赖图节点

### 8.2 删除基础设施（罕见）

1. 业务侧消费点先全部移除
2. `Container` 字段标 deprecated 注释
3. `Build` / `Shutdown` 加分支：旧字段 nil 时跳过
4. 等 ≥ 1 个 release，CI 跑过 + 部署稳定 → 真删

### 8.3 调整 Shutdown 顺序

罕见。**必须用户 / RFC 拍板**才能动 §6.3 锁定顺序。流程：

1. RFC 描述新顺序 + 理由
2. 改 `Shutdown` + 加单测覆盖新顺序
3. 集成测试跑一次 kill -TERM smoke，确认 in-flight 不 drop
4. 起一条 harness 条目（drafting）说明为何改

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | §1.5 资源复用 + §1.6 项目特有约束 |
| server/ 入口 CLAUDE.md | `../../CLAUDE.md` | §6.2 cmd 装配铁律（不许在 main 写业务）|
| config 模块 CLAUDE.md | `../config/CLAUDE.md` | 新增字段流程（必先走 config 再走 deps）|
| observability 模块 CLAUDE.md | `../observability/CLAUDE.md` | OTel Init 在 cmd 早于 deps.Build |
| 架构总览 | `../../../docs/ARCHITECTURE.md` | 依赖关系图 / 启动时序图 |
| Harness 索引 | `../../../docs/harness/README.md` | C003 / C010 / C015 关联 |
| Go skill | `~/.claude/skills/go-concurrency-patterns/SKILL.md` | sync.Once / context / 单例 pattern |

---

> 维护：本模块尚未完全 DI 化（当前仅 `deps_pin` 文件），落地时按 §3 SOP 演进；每接入一个新基础设施都按 §6.1 单例铁律落地，否则项目根 §1.5 警报。
