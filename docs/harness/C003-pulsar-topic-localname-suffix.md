# C003 — Pulsar push topic 必须经 `gateway.PushTopicFor(gatewayID, env)`，本地 dev 必须带 dev-suffix

```yaml
---
id: C003
title: Pulsar push topic 命名必须经 PushTopicFor 函数；本地 / 共享 Pulsar 必须有 USER/HOSTNAME 后缀
status: active
created: 2026-05-07
last_recurred: 2026-04-19
recurrence_count: 3
source_logs:
  - logs/2026-04-19.json#L88
  - logs/2026-04-22.json#L31
  - logs/2026-04-26.json#L9
applies_to:
  - server/internal/gateway/topic.go
  - server/internal/gateway/cross_pod_push.go
  - server/internal/gateway/push_consumer.go
  - server/cmd/message/**/*.go
  - server/cmd/gateway/**/*.go
inline_target: server/docs/BACKEND.md#§3.2
---
```

## 1. 触发场景（Trigger）

任何会构造 / 订阅 Pulsar push topic 字符串的代码：

- `server/internal/gateway/cross_pod_push.go` 投递 envelope 时算 topic
- `server/internal/gateway/push_consumer.go` 启动订阅时算 topic
- `server/cmd/message/main.go` worker 自定义 producer 时
- 任何 `*.go` 文件出现 `persistent://im/push` 字符串字面量
- 关键词 grep：`persistent://im/push` / `msg.push.` / `pulsar.NewProducer.*Topic`

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：硬编码 prod topic 没有 env 分支
topic := "persistent://im/push/msg.push." + gatewayID
producer, _ := pulsarClient.CreateProducer(pulsar.ProducerOptions{Topic: topic})

// ❌ 错误 #2：本地 / 多人共享 Pulsar 不加 dev-suffix
topic := "persistent://im/push-local/msg.push." + gatewayID  // 漏 USER 后缀

// ❌ 错误 #3：sender 和 consumer 一个用全名一个用短名
// sender:
producer.Send(ctx, ...)  // topic = "persistent://im/push-pre/msg.push.gw-1"
// consumer 启动时只订阅了：
consumer.Subscribe(... Topic: "msg.push.gw-1")  // 短名，落到 public/default/msg.push.gw-1

// ❌ 错误 #4：按 channelID / userID 命名 topic
topic := "persistent://im/push/msg.push." + channelID  // 千万 channel = 千万 topic 元数据爆 ZK
```

**后果**：
1. **生产 / pre / 本地之间窜台**：开发机投到 prod topic / pre worker 收到 dev 包 → 真用户被打扰 / 调试包打到生产消费者
2. **多人共享同 Pulsar 的 dev 互相打架**：A 用户投的 envelope 被 B 用户的 consumer 抢消费 → 调试断链 + 看不出 bug
3. **sender / consumer topic 不一致**：消息全部沉积在错的 topic（`public/default/*` 默认 namespace），ack timeout 打满，pod 重启不停
4. **topic 数爆炸**：按 user/channel 命名导致 ZK / Pulsar metadata 性能塌方（Pulsar topic 数上限远低于 channel 数量级）

事故链路：
- 2026-04-19 push_consumer 订阅 topic 用了 `gatewayID` 短名（漏 `persistent://im/push-pre/msg.push.` 前缀），落到 `public/default/{gatewayID}` → 跨 pod 推送全丢（commit `e0ecb32`，tag `v0.4.0` 修）
- 2026-04-22 pre 灰度时同事在本地跑 `make run-dev` 默认拿 `pre` env 投到了 `push-pre` topic → 干扰 pre 集群联调
- 2026-04-26 多人共享 staging Pulsar 没加 USER 后缀 → 张三的 send_ack 被李四的 consumer 抢消费

## 3. 正确做法（Required）

**首选 A — 单一入口 `PushTopicFor`**：

```go
// ✅ 正确：sender 和 consumer 都调同一个函数
import "im-server/internal/gateway"

// sender:
topic := gateway.PushTopicFor(gatewayID, cfg.Env)
producer := producerCache.Get(topic)
producer.Send(ctx, &pulsar.ProducerMessage{Payload: payload})

// consumer:
func (pc *PushConsumer) topic() string {
    return gateway.PushTopicFor(pc.gatewayID, pc.env)  // 同源
}
```

`PushTopicFor` 实现（`server/internal/gateway/topic.go:17`）：

```go
func PushTopicFor(gatewayID string, env string) string {
    switch env {
    case "prod":
        return "persistent://im/push/msg.push." + gatewayID
    case "pre":
        return "persistent://im/push-pre/msg.push." + gatewayID
    default:
        return "persistent://im/push-local/msg.push." + gatewayID + "." + devSuffix()
    }
}

func devSuffix() string {
    if u := os.Getenv("USER"); u != "" { return u }
    if h := os.Getenv("HOSTNAME"); h != "" { return h }
    return "anon"
}
```

**首选 B — env 推导规则**：

| 环境 | env 取值 | 来源 |
|---|---|---|
| 生产 | `prod` | k8s ConfigMap (`deploy/k8s/30-config.yaml`) |
| pre | `pre` | k8s ConfigMap (pre namespace) |
| 本地 / dev | 任意非 prod / pre 值（含空串） | 默认 `make run-dev` 不设 → fallback dev-suffix |

**绝对禁止 C**：
- ❌ 在 service / handler / 任何业务路径里手写 `persistent://im/push...` 字符串
- ❌ 硬编码 prod topic 跳过 env 分支
- ❌ 按 user/channel/companyID 等业务字段命名 topic
- ❌ 同时维护两套命名规则（如 sender 用 `PushTopicFor`，consumer 自己拼）

**实施约束**：
- 唯一入口：`gateway.PushTopicFor(gatewayID, env)`
- gatewayID 来源：`cmd/gateway/main.go::resolveGatewayID()`，每个 pod 一个稳定 ID（k8s pod name 或 `IM_GATEWAY_ID` env）
- env 来源：`config.Gateway.Env`（cfg 从 Consul KV / `IM_ENV` 推导）
- dev-suffix：`USER` > `HOSTNAME` > `"anon"` 三级 fallback

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① 业务路径硬编码 push topic 字符串（除 topic.go 自身）
grep -rEn 'persistent://im/push' server/ --include='*.go' \
  | grep -v 'gateway/topic.go' | grep -v '_test.go'

# ② 业务路径手写 msg.push. 短名
grep -rEn '"msg\.push\.' server/internal/service/ server/internal/http/ server/cmd/ \
  --include='*.go' | grep -v '_test.go'

# ③ 任何 pulsar Subscribe / CreateProducer 调用没经 PushTopicFor
grep -rEn 'pulsar\.(ProducerOptions|ConsumerOptions)\{' server/ --include='*.go' \
  | grep -v 'gateway/' | grep -v '_test.go'
```

### 4.2 CI Gate

- `verify-all` 加上 §4.1 三条 grep；非 0 行 → exit 1
- pre 部署前 smoke：`scripts/topic-name-smoke.sh` 验 `PushTopicFor("gw-1", "pre")` 返回 `persistent://im/push-pre/msg.push.gw-1`

### 4.3 单测（白盒）

- 路径：`server/internal/gateway/topic_test.go`
- 必备用例：
  - `TestPushTopicFor_Prod` — env=prod 返回 `persistent://im/push/msg.push.{gw}`
  - `TestPushTopicFor_Pre` — env=pre 返回 `persistent://im/push-pre/msg.push.{gw}`
  - `TestPushTopicFor_LocalWithUser` — env="" + USER=zlc → `...push-local/msg.push.gw-1.zlc`
  - `TestPushTopicFor_LocalWithHostname` — USER 未设 + HOSTNAME=mac28 → `...gw-1.mac28`
  - `TestPushTopicFor_LocalAnon` — 都没有 → `...gw-1.anon`
  - `TestSenderConsumerSameTopic` — sender 路径与 consumer 路径返回完全一致字符串

### 4.4 集成测试

- 路径：`server/tests/integration/m4_pulsar_topic_test.go`（待补 Batch-D）
- 验证：本地起两个 USER 不同的 worker → 互不消费对方 envelope
- 验证：env=pre + env=local 两个 producer 投到不同 topic，consumer 互不串台

### 4.5 手工 smoke

```bash
# 跨用户隔离 smoke（共享 Pulsar 时）
USER=alice IM_ENV=local go run cmd/gateway/main.go &
USER=bob   IM_ENV=local go run cmd/gateway/main.go &
# alice 发的 envelope 不应被 bob consumer 收到
kubectl -n pulsar-cses exec pulsar-cses-toolset-0 -- bin/pulsar-admin topics list im/push-local
# 应看到两条 topic：msg.push.gw-1.alice 和 msg.push.gw-1.bob
```

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                                       | 引用日志                  | 处置                                                                  |
|---|------------|------------------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-04-19 | M3 push_consumer 订阅短名 `{gatewayID}` 没经 PushTopicFor，跨 pod 推送全部落到 public/default | logs/2026-04-19.json#L88 | sender 与 consumer 改用同一函数（commit `e0ecb32`）                     |
| 2 | 2026-04-22 | 同事本地 `make run-dev` 默认 env=pre，dev 包投到 pre topic → 灰度联调被干扰                    | logs/2026-04-22.json#L31 | 加 `make run-dev` 默认 env=local 兜底（`server/Makefile`）             |
| 3 | 2026-04-26 | 多人共享 staging Pulsar 同一 USER 后缀（都是默认 anon），张三 ack 被李四消费                   | logs/2026-04-26.json#L9  | `devSuffix()` 强制 USER → HOSTNAME → anon 三级 fallback                 |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **migrations / seed**：本地一次性 SQL 脚本不涉及 Pulsar topic，不受约束
- ✅ **测试 fixture**：`tests/integration/*.go` 允许构造 mock topic（不投真 Pulsar）
- ✅ **k8s pre/prod 配置文件**：`deploy/k8s/30-config.yaml` 里硬编码 `IM_ENV=pre/prod` 是 ConfigMap 注入，不算"业务路径硬编码 topic"
- ❌ **不要**扩展到非 push 类 topic（如 dead-letter / retry-letter） —— 那些有自己的命名规则
- ❌ **不要**为了"测试方便"在单测里 mock 掉 `PushTopicFor` —— 命名规则就是契约，必须用真函数

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.1 grep gate + topic 单测 100% 覆盖
- inline 进 `server/docs/BACKEND.md §3.2 Pulsar topic 命名`（已部分 inline）
- inline 进项目根 `CLAUDE.md §1.6` 已有"Pulsar topic 命名 PushTopicFor + 本地后缀"摘要，本 harness 是详细版

**弃用 → deprecated**：
- Pulsar 被替换（如换 NATS / Redis Streams） → 新建 C{NNN}-replacement
- gateway 改单 pod / sticky session（不再有跨 pod push topic 概念）→ 同 C002 退役
- topic 命名彻底重构（如改用 schema registry 统一注册）→ 本条 deprecated
