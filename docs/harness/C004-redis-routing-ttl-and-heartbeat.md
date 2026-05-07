# C004 — Redis routing TTL = 45s + 心跳 15s × 3 容错；改一边必须同时改另一边

```yaml
---
id: C004
title: routing TTL 与 heartbeat interval 是耦合的；同步改 + 不允许单边改
status: active
created: 2026-05-07
last_recurred: 2026-04-25
recurrence_count: 1
source_logs:
  - logs/2026-04-25.json#L11
applies_to:
  - server/internal/repo/routing.go
  - server/internal/gateway/heartbeat.go
  - server/internal/gateway/ws_handler.go
inline_target: ~/.claude/skills/go-concurrency-patterns/SKILL.md  # heartbeat liveness pattern
---
```

## 1. 触发场景（Trigger）

任何会动以下常量 / 流程的 PR：

- `server/internal/repo/routing.go` 的 `RoutingTTL` 常量（默认 45s）
- `server/internal/gateway/heartbeat.go` 的 `heartbeatInterval` / `readDeadline` 常量（默认 15s / 45s）
- `MarkOnline` / `MarkOffline` / `Heartbeat` Lua 脚本里的 `EXPIRE` 参数
- 客户端心跳 `PingInterval`（前端 ws-client.ts / Tauri ws_handler.rs）
- 关键词 grep：`RoutingTTL` / `heartbeatInterval` / `readDeadline` / `connKeyTTL`

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：只动 routing TTL 不动心跳
const RoutingTTL = 30 * time.Second  // 改短了
// heartbeat 仍然 15s × 3 = 45s 容错 → routing 在 30s 时已过期，第 2 次心跳还没到 → 用户被误标离线

// ❌ 错误 #2：只动心跳间隔不动 routing TTL
const heartbeatInterval = 30 * time.Second  // 改长了
// RoutingTTL 仍然 45s → 30s + 5s gateway 处理 latency = 35s ≤ 45s 勉强够，但只能容忍 0 次丢包
// → 一次网络抖动就掉线

// ❌ 错误 #3：客户端 ping 改成 60s 不告知后端
// 前端：setInterval(ws.ping, 60_000)
// 后端 readDeadline = 45s → 60s ping 永远来不及，client 看似在线但 server 60s 后认为离线
```

**后果**：
1. **僵尸 routing**：用户实际下线但 routing key 没过期 → 异 pod sender 仍向其投 envelope → consumer ack timeout 累积
2. **误判离线**：用户实际在线但 routing 提前过期 → 跨 pod 投递认为 user offline 跳过 → 真消息丢
3. **心跳风暴**：心跳改太频（如 5s）→ Redis QPS × 3 + 网络流量 × 3，pre 集群 Redis CPU 打满
4. **客户端 / 服务端不同步**：前后端各自改一边 → 用户连接每分钟掉一次，root cause 极难定位

事故链路：
- 2026-04-25 想"压测期间提升 routing TTL 到 90s 减少 Redis 压力"，只动了 `RoutingTTL` 常量没动 `readDeadline` → 用户掉线后 90s 内仍接受 envelope，consumer 全部 ack timeout 红字（`v0.4.1-m3-markoffline-cleanup` 修，加 `sendFailureTracker` 3 次失败摘除）

## 3. 正确做法（Required）

**首选 A — 锁定四元组耦合关系**：

| 参数 | 当前值 | 来源 | 角色 |
|---|---|---|---|
| `heartbeatInterval` | 15s | `gateway/heartbeat.go:11` | 客户端 ping 间隔（服务端 pong 频率） |
| `readDeadline` | 45s | `gateway/heartbeat.go:12` | WS read timeout，3 个 ping 周期 |
| `RoutingTTL` | 45s | `repo/routing.go:20` | Redis routing key TTL，等同 readDeadline |
| `connKeyTTL` | 2h | `repo/routing.go:15` | conn-level Hash 整体过期时间，远大于 RoutingTTL |

**铁律**：

```
heartbeatInterval × 3 == readDeadline == RoutingTTL
```

- 3 倍冗余：允许丢 2 次心跳不掉线
- TTL 等于 readDeadline：用户掉线后 routing 自动过期，无需主动 markOffline
- `connKeyTTL` 是 2h（远大于 RoutingTTL）：当 user 重连进新 pod，老 deviceID 字段被新 EXPIRE 续命

**首选 B — 改动流程**：

任何调整这四个常量的 PR 必须：

1. **同步改动 4 处**：
   - `server/internal/gateway/heartbeat.go` 的 `heartbeatInterval` 与 `readDeadline`
   - `server/internal/repo/routing.go` 的 `RoutingTTL`
   - 客户端：`client/src/app/core/services/ws-client.service.ts` 的 `PING_INTERVAL_MS`
   - Rust 端：`src-tauri/src/websocket/im_handlers.rs` 的 `HEARTBEAT_INTERVAL`
2. **保持 3 倍关系**：`readDeadline = heartbeatInterval × 3 = RoutingTTL`
3. **PR 描述必填**：业务驱动（为什么改）+ 压测对比数据（Redis QPS / 用户掉线率）
4. **加 harness § 5 复现日志一行**

**绝对禁止 C**：
- ❌ 单边改：只改一个常量
- ❌ 把比例改成 4× / 5×（无业务理由）
- ❌ 不同模块用不同常量（`service` 层硬编码 30s 假设 routing TTL）
- ❌ 客户端 ping 间隔与服务端 heartbeatInterval 不一致

**实施约束**：
- 这四个常量必须**全部**用 `const` 集中声明，不允许散落或读 env（运维不应在线调）
- service 层引用 routing TTL 时必须 `import repo "im-server/internal/repo"; _ = repo.RoutingTTL`，不允许复制数值
- 客户端 / Rust 端的常量值必须在 PR 描述中显式列出，与后端对齐

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① service / handler 层硬编码 routing 时间常量
grep -rEn '45.*time\.Second|15.*time\.Second|RoutingTTL = ' \
  server/internal/service/ server/internal/http/ --include='*.go' | grep -v '_test.go'

# ② 任何 .Expire(ctx, key, ...time...) 不引用 RoutingTTL 常量
grep -rEn '\.Expire\([^)]*time\.[A-Z][a-z]+' server/ --include='*.go' \
  | grep -v 'repo/routing.go' | grep -v '_test.go'

# ③ 客户端 ping interval 不是 15000
grep -rEn 'setInterval.*ping|ping.*setInterval' client/src/ 2>/dev/null \
  | grep -v '15000\|15_000\|HEARTBEAT_INTERVAL'
```

### 4.2 CI Gate

- `verify-all` 加上 §4.1 grep
- 单测 `TestRoutingTTLMatchesReadDeadline` 必须 PASS：

```go
func TestRoutingTTLMatchesReadDeadline(t *testing.T) {
    if repo.RoutingTTL != gateway.ReadDeadline {
        t.Fatalf("RoutingTTL=%v != readDeadline=%v — 必须同时改", repo.RoutingTTL, gateway.ReadDeadline)
    }
    if gateway.HeartbeatInterval*3 != gateway.ReadDeadline {
        t.Fatalf("heartbeatInterval×3=%v != readDeadline=%v", gateway.HeartbeatInterval*3, gateway.ReadDeadline)
    }
}
```

### 4.3 单测（白盒）

- 路径：`server/internal/repo/routing_test.go`
- 必备用例：
  - `TestHeartbeatExpireExtendsTTL` — Heartbeat 调用后 TTL 重置为 45s
  - `TestMarkOffline_RemovesAllDevicesOnSameGateway` — Lua 脚本只删指定 gatewayID 的 device
  - `TestMarkOffline_PreservesOtherGatewayDevices` — 多设备多 pod 场景

### 4.4 集成测试

- 路径：`server/tests/integration/m4_ws_heartbeat_test.go`（Batch-D）
- 用例：
  - 客户端连上 → 不发 ping → 45s 后 read timeout，server 主动关 conn
  - 客户端连上 → 每 15s ping → 持续 60s 不掉线，routing TTL 持续刷新
  - 模拟丢 1 次 ping（30s 间隔）→ 不掉线
  - 模拟丢 2 次 ping（45s 间隔）→ readDeadline 触发掉线

### 4.5 运维监控

- Grafana panel：`im.gateway.heartbeat.miss_rate`（连续 2 次 ping 间隔 > 30s 的比例）必须 < 1%
- Grafana panel：`im.routing.expire_duration_p95`（routing key 实际存活时长 P95）应在 15s ~ 45s 之间
- 告警：`im.gateway.markoffline.due_to_failure_count` 突增 → 说明 routing TTL 与心跳关系破坏

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                              | 引用日志                  | 处置                                                                  |
|---|------------|---------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-04-25 | 压测想减 Redis 压力把 RoutingTTL 改 90s 没动 readDeadline，用户掉线后仍收 envelope    | logs/2026-04-25.json#L11 | 回滚 + 加 `sendFailureTracker` 3 次失败摘除（`v0.4.1-m3-markoffline-cleanup`，commit `1f9b78c`） |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **测试场景缩短**：`tests/integration/*.go` 用 `WithShortTTL(5*time.Second)` option override 是允许的，前提是测试结束 `t.Cleanup` 还原
- ✅ **`connKeyTTL`（2h）独立维护**：connKey 的整体 hash TTL 与 routing per-field TTL 不耦合，可以单独调（前提是远大于 RoutingTTL）
- ✅ **接受其他 3× 倍数**（如 30s × 3 = 90s）：业务有合理理由（如电池优化客户端少发 ping），但必须**四个一起动 + 压测数据 + PR 描述**
- ❌ **不要**把约束扩展到非 routing 类 Redis key（如 `cookie_cache` LRU TTL = 30s，不受本约束）
- ❌ **不要**因为单测想"快"就个别 override RoutingTTL 而不还原 —— 用 dependency injection，不能改全局常量

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.2 单测 `TestRoutingTTLMatchesReadDeadline` 在 CI 上接管
- inline 进 `~/.claude/skills/go-concurrency-patterns/SKILL.md` 的「heartbeat liveness pattern」节（多项目共性）
- inline 进项目根 `CLAUDE.md §1.6`（已有"Redis routing TTL = 45s，心跳 15s × 3 容错"摘要，本 harness 是详细版）

**弃用 → deprecated**：
- 心跳协议改 server-push（如改用 SSE / 长轮询）→ 不再有 client ping 概念
- routing 改持久化到 PG（不依赖 Redis TTL）→ TTL 概念消失
- WS 改 sticky session + LB level liveness → routing 表退役
