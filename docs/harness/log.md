# Harness 维护日志

> 每次创建 / 状态变更 / 弃用 harness 在此追条。时间倒序。

---

## [2026-05-07] activate | C008 drafting → active

由 Batch-A/B/C/D/E 落地（Batch-E autonomous agent 跑中）+ `scripts/check-handler-coverage.sh`
写完 + Makefile `check-handler-coverage` target 落地触发升级。

**晋升达成的条件**：
- Batch-B 130 case 进 tests/integration/（commit `3d39889`，tag `v0.7.3-batch-b-tests`）
- 后续 Batch-A/C/D 共 ~189 测试 + Batch-E 6 测试（agent 跑中）= ≈195 测试函数
- `server/scripts/check-handler-coverage.sh` exec gate（routes ≥84 / tests ≥190 / family 启发式扫描）
- `server/Makefile::check-handler-coverage` target 已接入 `verify-integration` 前置

**晋升 active → merged 还需**：
- 30 天零 CI 失败计时
- 行覆盖率 100% 独立 task（按 ~/.claude/rules/golang/testing.md，单独 autonomous 工程）
- inline 进 `~/.claude/rules/golang/testing.md` § 接口组覆盖

**Source log**：`logs/2026-05-07.json`（本会话 Batch-A→E 全程）

---

## [2026-05-07] create | C009 — test helper 与 middleware shape 同步

由 Run #1（envelope middleware 注入但漏扫 seedDM/seedGroup 导致 ~120 fail，但 `tail -100` 仅捕末尾 2 fail）固化为 active harness。

**关键教训**：
- middleware 注入是高风险操作 → 必须先 sweep 所有 test helper / fixture 是否依赖旧 shape
- 集成测试输出禁止 `| tail` —— full log 必须落 file，否则中间 fail 信号丢失
- 编译 + vet 全绿 ≠ 测试全绿，shape 错位是运行时错

**晋升路径**：CI 接管 §4.1 grep + 30 天零复现 → inline 进 `~/.claude/rules/golang/testing.md`

**Source log**：`logs/2026-05-07.json`（本会话 envelope alignment 阶段）

---

## [2026-05-07] create | C001-C008 + 框架搭建

由会话 `fb6d6037` 用户要求"构建 harness Engineering 在 docs/harness 文件夹下"触发，三轮迭代落地。

**搭建内容**：
- `docs/harness/README.md` — 索引 + §3 触发条件 / §4 弃用条件 / §5 与 CLAUDE.md 优先级
- `docs/harness/TEMPLATE.md` — 七段式模板（Trigger / Anti-Pattern / Required / Verification / Recurrence / Don't Over-Apply / Lifecycle）
- `docs/harness/log.md` — 本文件
- 项目根 `CLAUDE.md §8` — Harness Engineering 节，含触发 / 升级 / 弃用条件 + active 索引

**8 条 harness**：

| 编号 | 主题 | 状态 | 关键证据链 |
|---|---|---|---|
| C001 | `AllocSeqAndInsert` 唯一入口 | active | commit `5dc95e5` + `66c2a67` |
| C002 | 跨 pod 推送 `CrossPodPush` | active | commit `8cf1a3b` + `1f9b78c`，tag `v0.4.0/v0.4.1` |
| C003 | Pulsar `PushTopicFor` + dev-suffix | active | commit `e0ecb32` + `topic.go:17` |
| C004 | routing TTL 45s + 心跳 15s × 3 | active | commit `1f9b78c`，tag `v0.4.1` |
| C005 | WS 事件类型 22 种锁定 | active | commit `8cf1a3b`（v0.7.0 reaction × 2 + channel_top + channel_info）— **§6.1 contradiction 待用户拍板** |
| C006 | httpexpect `?` 编码陷阱 | active | commit `43c05ab`（13 处批量改 `WithQuery`）|
| C007 | responseEnvelope 不双重 wrap | active | commit `441ba37` + `66c2a67`（Phase 1 中间件 + 第一次踩坑）|
| C008 | 100% 覆盖路径 5 batch | drafting | 当前 `internal/http` 3.4% / `internal/service` 0.3% — 实测数据 |

**待用户拍板**（C005 §6.1）：
- A：把"V1+M2=16"措辞升级为 **"V1+M1+M2+v0.7 = 22"**（最准确）
- B：v0.7 扩展声明为 V1.5（兼容旧客户端）
- C：正式启动 V2 RFC

**待补**：
- C002-C007 的 §4.4 集成测试（绝大多数仍是占位，依赖 C008 Batch-D 落地）
- `scripts/check-handler-coverage.sh` / `scripts/topic-name-smoke.sh` / `scripts/check-ws-types.sh` 三个 verification 脚本未实际创建（写在各 harness §4 中作为 contract）

**Source logs**：`logs/2026-05-07.json`（本会话 prompt 时间线 04:58 → 05:11）
