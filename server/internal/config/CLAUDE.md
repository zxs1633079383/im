# CLAUDE.md — internal/config 模块级指令

> 本文件仅约束 `server/internal/config/` 目录（viper-style YAML + Consul KV + env override）。
> 优先级：用户全局 `~/.claude/CLAUDE.md` > 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md` > `server/CLAUDE.md` > **本文件** > 默认行为。
> 加载顺序：会话开始先扫 `docs/harness/`（**C010 + C015 必读**），再扫本文件。

---

## 0. 模块定位

**是什么**：im Go 后端**配置加载的唯一入口**。三件套：

- ✅ `Load(path)` —— 读本地 YAML + 应用 env override
- ✅ `LoadFromConsulOrFile(path)` —— Consul KV 优先；缺 `IM_ENV` / `IM_CONSUL_URL` 则降级读 YAML
- ✅ `Config` struct —— 后端所有可配置项的**契约**（PG / Redis / Pulsar / Gateway / Observability）

**关键属性**：

- `config.example.yaml` 是**对外契约文件**：新增字段必须先改它（reviewer 用它读 schema）
- `applyEnvOverrides` 是 K8s secret 注入路径（pre / prod 走 env，不走 Consul KV）
- `applyDefaults` 与 `Load` 内置默认值保持等价（不允许两边漂移）

**不负责**：

- ❌ 实际构造 client / pool —— 那是 `cmd/*/main.go` + `internal/gateway` / `internal/cache` / `internal/repo` 的事
- ❌ 配置热加载 —— **本项目不支持**热改 config；任何字段变更走重启路径
- ❌ Consul KV 写入 —— 只读；写由运维侧 ansible / consul-template 处理

---

## 1. 影响范围

**上游依赖**：

| 依赖 | 来源 | 备注 |
|---|---|---|
| `gopkg.in/yaml.v3` | go.mod | YAML 解析 |
| `github.com/hashicorp/consul/api` | go.mod | KV 拉取 |
| `github.com/google/uuid` | go.mod | `ResolveGatewayID` 兜底 |

**下游影响**（改这里波及谁）：

| 改动 | 波及面 |
|---|---|
| 新增 `Config` 字段 | `cmd/gateway/main.go` + `cmd/message/main.go` 两入口装配 + `config.example.yaml` + Consul KV `im-go/{env}/config.yaml` + 部署 chart |
| 改默认值 / `applyDefaults` 行为 | 所有未显式配 yaml / Consul 的进程；旧环境可能跑出意外行为 |
| 改 env var 名 | `scripts/run-all-dev.sh` + `Makefile` `run-*-dev` target + K8s deployment + 本地开发者 `.envrc` |
| 改 `LoadFromConsulOrFile` 优先级逻辑 | dev/pre/prod 全部加载源会变；必须分环境测 |

> 改本包前必看：`server/docs/BACKEND.md` 配置章节、`deploy/k8s/20-deployment.yaml`（env 注入清单）。

---

## 2. 功能模块清单

| 文件 | 角色 | 备注 |
|---|---|---|
| `config.go` | `Config` struct + `Load(path)` + `applyEnvOverrides` + `ResolveGatewayID` | 173 行；YAML schema 契约入口 |
| `consul.go` | `LoadFromConsulOrFile` + `loadFromConsul` + `applyDefaults` + 4 env var 常量 | 165 行；Consul 拉取 + 默认值同步 |
| `config_test.go` | 4 testcase：file load / env override / Redis Addrs / IM_REDIS_CLUSTER | 100% 行覆盖 |
| `../../config.example.yaml` | **对外契约 YAML** | 新增字段必先改它 |

**关键导出符号**：

- `config.Config{PG, Redis, Pulsar, Gateway, Observability}`
- `config.Load(path string) (*Config, error)`
- `config.LoadFromConsulOrFile(path string) (*Config, string, error)`
- `config.ResolveGatewayID(*Config) string`
- env var 常量 4 个：`EnvVarEnv` / `EnvVarConsulURL` / `EnvVarConsulKey` / `EnvVarConsulToken`
- 4 个环境枚举：`EnvDev` / `EnvPre` / `EnvProd`
- `ErrConsulKeyMissing` —— 调用方 detect 后可降级

---

## 3. SOP — 写代码 → 验证 → commit

新增 / 修改字段的标准流程（**顺序不可乱**）：

```
0. 开局：读 docs/harness/C010 + C015 + 本文件
1. Skill(skill="go-concurrency-patterns")  ← 任何 .go 改动强制
2. 改 config.example.yaml —— 加新字段 + 写注释说明默认值 + 注释里给 pre/prod 示例
3. 改 config.go —— Config struct 加字段 + tag `yaml:"xxx"` + Load 内默认值
4. 改 consul.go applyDefaults —— 同步等价默认值（**禁止漂移**）
5. （可选）改 applyEnvOverrides —— 加 IM_XXX env override
6. TDD：在 config_test.go 加用例（file load / env override / default）
7. go test -race ./internal/config/...
8. grep gate 自查（见 §4.2）
9. 改 cmd/*/main.go 消费新字段（构造 client / pool 时读 cfg.<New>）
10. 改 scripts/run-all-dev.sh + Makefile run-*-dev（如新字段需本地 dev override）
11. 改 deploy/k8s/20-deployment.yaml env 块（如新字段走 K8s secret）
12. 更新 Consul KV im-go/{dev,pre,prod}/config.yaml —— **运维侧 PR**，本仓只标注
13. make verify-all
14. commit（见 §5）
```

**特殊路径**：

- 只改默认值 → 必须同时改 `Load`（YAML 缺省）+ `applyDefaults`（Consul 缺省）两处，否则 Consul 来源拿不到新默认值
- 只改 env override 行为 → `config_test.go` 必须新增对应 testcase（参照 `TestLoadRedisClusterEnvOverride`）
- 删除字段 → **禁止直接删**；先在 yaml + struct 标 deprecated 注释，下个版本再清

---

## 4. Pre-commit 自检

### 4.1 必跑命令

```bash
cd /Users/mac28/workspace/golangProject/im/server
go test -race ./internal/config/...
go vet ./internal/config/...
make verify-build                              # cmd 装配编译不挂
```

新增字段（必要时）：

```bash
make verify-integration                        # 加载 + 装配 + 启动烟测
```

### 4.2 Grep gate（结果必须 0 条 / 或 ≥ 4 条 — 见每条说明）

| Gate | 命令 | 期望 |
|---|---|---|
| 新字段在 example 中 | `grep -n '<field_name>' server/config.example.yaml` | **≥ 1 条** — 必须先改 example |
| 新字段在 struct 中 | `grep -n '<field_name>' server/internal/config/config.go` | **≥ 1 条** |
| 新字段默认值双写 | `grep -nE '<field_name>' server/internal/config/{config,consul}.go` | **≥ 2 条**（Load + applyDefaults）|
| 业务包绕过 config 直接读 env | `grep -rn 'os.Getenv(' server/ --include='*.go' \| grep -v 'internal/config/\|cmd/'` | **= 0** — 业务只能拿 `*Config` |
| `IM_REDIS_CLUSTER=true` 出现次数（C010 §4.6）| `grep -nE 'IM_REDIS_CLUSTER\s*=\s*true\|"IM_REDIS_CLUSTER=true"' server/Makefile server/scripts/run-all-dev.sh deploy/k8s/20-deployment.yaml` | **≥ 4 条** — 本地 dev / Makefile run target / k8s deployment 都必须 override |
| Pulsar topic 写死（C003）| `grep -rn '"push-' server/ --include='*.go' \| grep -v 'PushTopicFor\|_test.go'` | **= 0** — 必经 `PushTopicFor`，本地必带 USER/HOSTNAME 后缀 |

### 4.3 失败处置

- 单测红 → **tdd-guide** agent
- `make verify-build` 红（cmd 装配编译挂）→ 同 PR 修；不许只改 struct 不改 cmd
- env override 行为变更 → 加 `TestLoadXXXEnvOverride` testcase；禁止靠 manual smoke

---

## 5. Commit 规范

沿用 Conventional Commits + 中文 body：

```
feat(config): xxx

<why 用中文，每行 ≤ 72 字符>
```

### 5.1 常用 scope 模板

| 场景 | 模板 |
|---|---|
| 新增字段 | `feat(config): 新增 gateway.upload_dir 字段（默认 /data/uploads）` |
| 改默认值 | `refactor(config): PG.MaxConns 默认 20 → 300 对齐 Java HikariCP` |
| 新 env override | `feat(config): 加 IM_REDIS_CLUSTER env 强制 cluster 模式（C010）` |
| Consul 路径变更 | `refactor(config): Consul KV im/dev/config.yaml → im-go/dev/config.yaml` |

**禁止**：
- ❌ `feat(config): tweak`（信息量 0）
- ❌ 同 commit 混 config + 业务装配
- ❌ 改 `config.example.yaml` 不改 struct（或反之）

---

## 6. 约束规范（本层强约束）

### 6.1 本地 dev 必须 `IM_REDIS_CLUSTER=true`（C010 §4.6）

Consul KV `im-go/dev/config.yaml` 配 `cluster: false`，但 pre Redis 实际是 Cluster 部署 ——
本地 / 集群启动入口必须 env override 成 `true`，否则 go-redis 跑 single client → 拿不到
`UserData:<id>` → 鉴权全 401。

- ✅ `Makefile` 三个 run-*-dev target 内联 `IM_REDIS_CLUSTER=true`
- ✅ `scripts/run-all-dev.sh` COMMON_ENV 块内含 `IM_REDIS_CLUSTER=true`
- ✅ `deploy/k8s/20-deployment.yaml` env 块声明 `IM_REDIS_CLUSTER`
- CI grep gate（C010 §4.6）期望 ≥ 4 条命中

### 6.2 Pulsar topic 本地必带 USER/HOSTNAME 后缀（C003）

不在本包代码内，但配置文件 `pulsar.url` + topic 命名约束**联动**：

- 本地 dev `PUSH_TOPIC_SUFFIX` env 必须设为当前 USER 或 HOSTNAME（dev 环境窜台防护）
- 改 `PulsarConfig` 新增字段时考虑 topic 命名解析路径是否需要联动 env

### 6.3 新增字段三同步铁律

任何 `Config` 字段新增 **必须同 PR** 改完三处，缺一不可：

1. `server/config.example.yaml` —— 加字段 + 注释默认值 + pre/prod 示例
2. `server/internal/config/config.go` —— `Config` struct 加字段 + `Load` 内默认值
3. `server/internal/config/consul.go` —— `applyDefaults` 同步默认值

**为什么三处都要**：
- example 是 reviewer 读 schema 的入口
- `Load` 默认值兜底**本地 YAML** 缺省
- `applyDefaults` 兜底 **Consul KV** 缺省（KV 来源不走 `Load`，独立路径）

### 6.4 env override 规则

- ✅ 命名空间 `IM_*`（PG / Redis / Pulsar / Gateway）
- ✅ 第三方惯例 env 直接尊重（`OTEL_EXPORTER_OTLP_ENDPOINT` / `HOSTNAME`）
- ✅ truthy 值锁定 `"true"` / `"1"`，**其他全视为 false**（参照 `TestLoadRedisClusterEnvOverrideFalseValues`）
- ❌ 禁止给业务字段（如 `messages.max_seq`）开 env override —— 业务配置走 Consul KV

### 6.5 Consul 加载约束

- ✅ `IM_ENV` 必须是 `dev` / `pre` / `prod` 三值之一
- ✅ `IM_CONSUL_URL` 设但 `IM_ENV` 未设 → fallback 到 `EnvDev`
- ✅ Consul fetch 失败必须返回 wrapped error（`fmt.Errorf("consul kv get %q: %w", ...)`）
- ❌ 禁止 Consul fetch 失败时自动 fallback 到 YAML —— **fail fast**（误导排查）

---

## 7. 对应 Harness 映射

| Harness | 触发场景 | 验证手段 |
|---|---|---|
| [C010](../../../docs/harness/C010-userdata-resolve.md) §4.6 | 改启动 env 注入 / 改 `RedisConfig.Cluster` 默认值 | grep `IM_REDIS_CLUSTER=true` 命中 ≥ 4 条；本地 dev 鉴权 smoke 必经此 env |
| [C015](../../../docs/harness/C015-testcontainers-redis-port-race.md) | 改集成测试用的 Redis 配置 | testcontainers 起 redis 必走 `MappedPortWithRetry`；本包不直接管 testcontainers，但 `Config` 字段名 / 类型变更会影响 testutil 装配 |
| [C003](../../../docs/harness/C003-pulsar-topic-localname-suffix.md) | 改 `PulsarConfig` / 本地 dev Pulsar topic 命名 | 本地启动必带 `USER` / `HOSTNAME` 后缀；CI grep 写死 `"push-"` = 0 |
| 项目根 §1.6 项目特有约束 | 全局 | Redis routing TTL = 45s × 心跳 15s × 3 耦合（虽不在本包，但默认值修改时连带审视 cache 层）|

> 任何 `Config` 字段变更触发 ≥ 3 次"忘了改 K8s deployment / Consul KV / config.example.yaml"的复现 → 立刻起草 drafting harness。

---

## 8. Update / Insert 规则

### 8.1 新增字段（标准流程，见 §3 SOP）

参考 commit `feat(config): 新增 observability.endpoint 字段` 模式：

1. `config.example.yaml` 加：
   ```yaml
   observability:
     endpoint: ""  # OTLP/gRPC host:port no scheme; empty → OTEL_EXPORTER_OTLP_ENDPOINT
   ```
2. `config.go` struct + 默认值
3. `consul.go applyDefaults` 同步
4. （如需）`applyEnvOverrides` 加 `OTEL_EXPORTER_OTLP_ENDPOINT`
5. cmd 装配 + 单测 + 联调

### 8.2 删除字段（罕见，**禁止直接删**）

1. 字段在 struct + yaml 标 deprecated 注释
2. 业务侧消费点改读 fallback / 砍掉调用
3. 等 ≥ 1 个 release 周期 + Consul KV 已清理对应 key
4. 才能真删（commit scope = `refactor(config): 清理 deprecated.xxx 字段`）

### 8.3 新增 Consul 环境（如 `staging`）

1. `consul.go` 加 `EnvStaging ConsulEnv = "staging"` 常量
2. `defaultConsulURLByEnv` / `defaultConsulKeyByEnv` 加映射
3. 单测覆盖（仿照现有 dev/pre/prod 测试）
4. 运维侧同步部署 `consul-staging` 集群

---

## 9. 文档关联

| 文档 | 在哪 | 用途 |
|---|---|---|
| 项目根 CLAUDE.md | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | §1.6 项目特有约束 + §3 commit 规范 |
| server/ 入口 CLAUDE.md | `../../CLAUDE.md` | §6.2 cmd 入口禁直接 os.Getenv（必经本包）|
| 架构总览 | `../../../docs/ARCHITECTURE.md` | 配置加载链路图 + Consul KV 命名空间 |
| 配置 example | `../../config.example.yaml` | **对外契约 YAML**（必先改它） |
| K8s deployment | `/Users/mac28/workspace/golangProject/im/deploy/k8s/20-deployment.yaml` | env 注入清单（同步审）|
| Harness C010 | `../../../docs/harness/C010-userdata-resolve.md` | §4.6 IM_REDIS_CLUSTER override 强约束 |
| Harness C015 | `../../../docs/harness/C015-testcontainers-redis-port-race.md` | testcontainers 端口 race（间接关联）|
| Redis 知识 skill | `~/.claude/skills/redis-knowledge/` | Cluster vs Single 模式行为差异 |

---

> 维护：本模块每次新增字段都要按 §3 三同步铁律走；漏掉任一处必定在 pre 联调暴露。
