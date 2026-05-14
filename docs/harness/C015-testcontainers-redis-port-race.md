---
id: C015
title: testcontainers-go v0.35.0 redis module port-mapping race 必须 retry 兜底
status: active
created: 2026-05-14
last_recurred: 2026-05-14
recurrence_count: 1
source_logs:
  - FX1 worktree fix/integration-tests-2026-05-14 全量复盘：197 RUN 41 FAIL 中 25+ 误诊为"docker-compose vs testcontainers 双源"
  - 真实根因：testcontainers-go v0.35.0 `MappedPort()` race（容器 Ready 但 port 表偶发延迟）
applies_to:
  - server/internal/testutil/containers/redis.go
  - server/internal/testutil/containers/postgres.go
  - server/internal/testutil/containers/pulsar.go
  - server/tests/integration/m4_harness_test.go
inline_target: server/docs/M4_SPEC.md §测试基础设施
related:
  - C014  # 测试覆盖 + CI gate（FX1 是 C014 集成测试 gate 的派生修复）
---

# C015 — testcontainers-go v0.35.0 redis module port-mapping race 必须 retry 兜底

> **沉淀时机**：2026-05-14，FX1 worktree `fix/integration-tests-2026-05-14` merge `70a27b0`。
> **代价**：误诊 5+ 小时（agent 多轮报告 "testcontainers vs docker-compose 双源 race"，实际 docker-compose 起的 redis 集成测试根本不用）。

## 1. 触发场景（Trigger）

**适用**：
- 任何使用 `internal/testutil/containers/redis.go::StartRedis` 的集成测试
- 集成测试报错 pattern：`port "6379/tcp" not found in container <id>` / `MappedPort` 返回 0
- testcontainers-go 库 v0.35.0 redis / postgres / pulsar 模块 spawn 多 container 并发场景

**不适用**：
- 单 container 测试（无并发竞争）
- 使用 docker-compose 起的固定端口 container（如 `localhost:16379`）—— 测试若改用该路径直接连即无此 race

## 2. 错误模式（Anti-Pattern）

### 2.1 一次性 MappedPort 调用，无 retry

```go
// ❌ 错误
func StartRedis(t *testing.T, ctx context.Context) (string, func()) {
    container, _ := redis.Run(ctx, "redis:7-alpine")
    port, err := container.MappedPort(ctx, "6379/tcp")  // 偶发 "not found"
    if err != nil { t.Fatal(err) }
    host, _ := container.Host(ctx)
    return fmt.Sprintf("%s:%s", host, port.Port()), cleanup
}
```

**问题**：testcontainers-go v0.35.0 在 `WaitingFor` 返回后偶发**端口表未完全回填**，立刻调 `MappedPort` 返回 `port not found`。25+ 个测试在并发起容器时随机命中此 race。

### 2.2 误诊"双源竞争"硬切到 docker-compose

```go
// ❌ 反模式：怀疑 testcontainers 不靠谱 → 全部改连 docker-compose 起的 redis
const redisAddr = "localhost:16379"
```

**问题**：
- 集成测试失去 isolation（多 test 共享同 redis db，key 冲突）
- CI 环境必须额外起 docker-compose 服务（增加 CI 复杂度）
- 真实 bug（testcontainers race）被掩盖，未来 v0.36+ 仍会复现

## 3. 正确做法（Required）

### 3.1 MappedPort retry 兜底（5×200ms backoff）

```go
// ✅ server/internal/testutil/containers/redis.go::StartRedis
func StartRedis(t *testing.T, ctx context.Context) (string, func()) {
    container, err := redis.Run(ctx, "redis:7-alpine")
    require.NoError(t, err)

    // testcontainers-go v0.35.0 race：WaitingFor 返回后偶发 port 表未回填。
    // 见 C015：最多 5 次 ×200ms backoff 容错。
    var (
        port nat.Port
        host string
    )
    for i := 0; i < 5; i++ {
        port, err = container.MappedPort(ctx, "6379/tcp")
        if err == nil { break }
        if !strings.Contains(err.Error(), "not found in container") {
            t.Fatalf("redis MappedPort: %v", err)  // 真错，不 retry
        }
        time.Sleep(200 * time.Millisecond)
    }
    require.NoError(t, err, "redis MappedPort race after 5 retries")
    host, err = container.Host(ctx)
    require.NoError(t, err)

    return fmt.Sprintf("%s:%s", host, port.Port()), func() { _ = container.Terminate(ctx) }
}
```

### 3.2 跨 module 统一兜底

postgres / pulsar 同一根因（v0.35.0 库内 race），相同 retry pattern：

```go
// ✅ postgres.go / pulsar.go 类似 retry 兜底
```

合并到 `testutil/containers/retry.go` helper 复用：

```go
// MappedPortWithRetry — testcontainers v0.35.0 race 兜底
func MappedPortWithRetry(ctx context.Context, c testcontainers.Container, p nat.Port) (nat.Port, error) {
    var err error
    for i := 0; i < 5; i++ {
        port, e := c.MappedPort(ctx, p)
        if e == nil { return port, nil }
        err = e
        if !strings.Contains(e.Error(), "not found in container") { return "", e }
        time.Sleep(200 * time.Millisecond)
    }
    return "", fmt.Errorf("after 5 retries: %w", err)
}
```

### 3.3 docker-compose redis（dev 跑后端用）不归此 harness

`docker-compose.yml` 的 `redis:7-alpine` host:16379 是**开发跑后端**用的（`make run-dev` 连 `localhost:16379`），**与集成测试隔离**。两者共存无冲突，因为：
- 集成测试用 testcontainers 自起 random port
- dev 后端用 docker-compose 固定 16379

不要把测试切到 16379 — 会破坏测试 isolation 且引入 CI 依赖。

## 4. 检查方法（Verification）

### 4.1 自动 grep

```bash
# 凡是直接调 MappedPort 而无 retry 的，必须改 helper
grep -rn 'container\.MappedPort(\|\.MappedPort(ctx,' server/internal/testutil/containers/ \
  | grep -v "MappedPortWithRetry\|_retry_test.go" \
  | wc -l    # 必须 = 0
```

### 4.2 集成测试稳定性

```bash
# CI 上跑 3-5 次 confirm 无 race
for i in 1 2 3 4 5; do
    go test -tags integration -count=1 -timeout 30m ./tests/integration/... \
      2>&1 | grep -E "^FAIL|--- FAIL"
done
# 5 轮全空 → 稳定
```

### 4.3 v0.36+ 升级后回归

testcontainers-go 升级到 v0.36 / v0.37 时跑 retry path 与无 retry path 各 10 次，对比 fail 率。若上游修复了 race（hopefully），可以考虑去除 retry helper（merge 进 lifecycle）。

## 5. 复现历史（Recurrence Log）

| # | 日期 | 触发场景 | 处置 |
|---|------|---------|------|
| 1 | 2026-05-14 | C014 4 CI gate 接管后跑全集成测试，41/197 FAIL；误诊 "docker-compose 双源 race" 5+ 小时；FX1 worktree 实测后定位为 testcontainers v0.35.0 redis 内 race，5×200ms retry 后 197/197 PASS | **本 harness 创建**；commit `52246d9` retry helper 落地；merge `70a27b0` 进 main |

## 6. 反例与边界（Don't Over-Apply）

- ❌ **不要扩大 retry 范围到非 `port not found` 错误**：真错（如 container Failed / OOM）必须立刻 fail，retry 会掩盖问题
- ❌ **不要把 retry 用在产品代码**：仅 `internal/testutil/containers/` 测试基础设施允许；生产代码该崩就崩
- ❌ **不要因此切到 docker-compose 双源**：测试 isolation 优先级最高
- ❌ **不要无限 retry**：上限 5 次，超过即认 hard fail
- ✅ 边界：若 testcontainers v0.36+ 上游修复 race（github.com/testcontainers/testcontainers-go/pull/<TBD>），本 harness 可以 deprecate，删 retry helper

## 7. 升级 / 弃用条件（Lifecycle）

- **当前**：active（FX1 已落地 `52246d9` retry helper）
- **升级 merged**：连续 30 天 CI 集成测试 0 race fail + retry helper inline 进 `~/.claude/rules/golang/testing.md` "testcontainers race 模板" 小节
- **弃用 deprecated**：testcontainers-go 上游 race 修复后（升级到修复版本 + 跑 100 轮验证无 race）→ 删 retry helper，本 harness deprecate

## 8. 误诊教训（FX1 复盘）

**root cause 分析为何耗时 5+ 小时**：

1. P-D agent 报告 25 fail "testcontainers redis port not found"，**字面误读**为"两套 redis 来源竞争"
2. P-E agent 沿用该诊断，未自己复现
3. T6 agent 沿用，未深查
4. 直到 FX1 agent 实测 `docker ps` + `grep testcontainers redis` 才发现：
   - 集成测试**完全不用** docker-compose 起的 redis
   - testcontainers 自起 random-port redis，每个 test 独立容器
   - 但库内 v0.35.0 `MappedPort` 在 ContainerReady 后偶发未回填 port 表

**沉淀点**：agent 报错信息**字面**有"port not found in container"，应该立刻定位到 testcontainers-go 库 issue（v0.35 changelog / GitHub issues），而不是脑补"双源竞争"叙事。

→ 对后续 autonomous agent 的约束：**遇到第三方库错误信息，先查库版本 + GitHub issues，再做架构层猜测**。

---

**Owner**：im 后端
**最后更新**：2026-05-14
**下次更新触发**：testcontainers-go v0.36+ 升级 / CI 集成测试再次 random fail
