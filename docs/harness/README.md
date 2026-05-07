# Harness Engineering — im 项目踩坑沉淀索引

> 这份目录是 **Harness Engineering** 的总入口。Harness ≠ 文档：
> 它是**约束 LLM / 人类未来不再踩同一坑的可执行工程契约**，每条都带 grep / CI gate / 单测验证手段。
>
> 触发条件、生命周期与升级路径见本目录 §3-§5；模板见 [`TEMPLATE.md`](TEMPLATE.md)。
> 本索引由 im 项目根 `CLAUDE.md §8` 指向，所有 Go / Angular / Tauri Rust 改动都默认加载。

---

## 1. 当前在册 Harness（按编号顺序）

| 编号 | 标题 | 状态 | 来源日志 | 复现次数 |
|---|---|---|---|---|
| [C001](C001-allocseq-and-insert-only-message-write-path.md) | `repo.MessageRepo.AllocSeqAndInsert` 是消息写入唯一入口 | active | logs/2026-04-23.json#L42 | 2 |
| [C002](C002-cross-pod-push-must-go-gateway-crosspodpush.md) | 跨 pod 推送必须走 `gateway.CrossPodPush` / `CrossPodBroadcast` | active | logs/2026-04-21.json#L17 | 2 |
| [C003](C003-pulsar-topic-localname-suffix.md) | Pulsar topic 必须经 `PushTopicFor`，本地必带 USER/HOSTNAME 后缀 | active | logs/2026-04-19.json#L88 | 3 |
| [C004](C004-redis-routing-ttl-and-heartbeat.md) | routing TTL = 45s 与心跳 15s × 3 是耦合的；改一边必须同时改另一边 | active | logs/2026-04-25.json#L11 | 1 |
| [C005](C005-ws-event-types-locked.md) | WSMessageType 锁定 22 种（含 contradiction 待用户拍板）；新增走 V2 RFC | active | logs/2026-04-22.json#L65 | 2 |
| [C006](C006-httpexpect-query-encoding.md) | httpexpect v2 路径禁拼 `?q=`，必须 `.WithQuery` | active | logs/2026-04-26.json#L33 | 1（commit 43c05ab） |
| [C007](C007-response-envelope-no-double-wrap.md) | 全局 responseEnvelope 中间件已生效，handler 禁止再 wrap status/data | active | logs/2026-05-01.json#L120 | 1 |
| [C008](C008-handler-coverage-gate.md) | 76 端点 × 5 case + 16 WS × 6 case 是 100% 覆盖率硬门 | drafting | logs/2026-05-07.json#L1（本次会话） | 1（rules/golang/testing.md 落地清单） |
| [C009](C009-test-helper-tracks-middleware-shape.md) | 加 / 改 middleware 必须 sweep 所有 test helper（Run #1 ~120 fail 教训） | active | logs/2026-05-07.json#L1（envelope 注入事故） | 1 |

> 新条目命名规范：`C{NNN}-{kebab-case-slug}.md`，编号严格递增，删除条目用 `status: deprecated` 不删除文件。

---

## 2. 模板与字段强制

每条 harness 文件**必须**用 [`TEMPLATE.md`](TEMPLATE.md) 七段式结构：

1. **Trigger（触发场景）** — 哪些代码路径 / 文件 glob 必须加载本约束
2. **Anti-Pattern（错误模式）** — 反面教材代码 + 后果
3. **Required（正确做法）** — 首选 / 备选 / 绝对禁止
4. **Verification（检查方法）** — 自动 grep（必须 0 条）+ 单测 / CI gate
5. **Recurrence Log（复现历史）** — 表格：日期 / 触发场景 / 引用日志 / 处置
6. **Don't Over-Apply（反例与边界）** — 哪些场景**不适用**本约束（防止过度泛化）
7. **Lifecycle（升级 / 弃用）** — 晋升 / 弃用条件

**Frontmatter 强制字段**：

```yaml
---
id: C{NNN}
title: 一句话标题
status: drafting | active | merged | deprecated
created: YYYY-MM-DD
last_recurred: YYYY-MM-DD
recurrence_count: N
source_logs:
  - logs/YYYY-MM-DD.json#L{line}
applies_to:
  - server/**/*.go
  - client/src/app/**/*.ts
inline_target: ~/.claude/rules/golang/coding-style.md  # 晋升后 inline 到的位置（可选）
---
```

---

## 3. 触发条件（什么情况下要新建 / 升级 harness）

任何符合以下**任意一条**的踩坑：

| 触发条件 | 处置 |
|---|---|
| **第 3 次复现**（同一 root cause 在 logs/ 下被记录 ≥ 3 次） | **必须**沉淀为 harness（不再"下次注意"） |
| 用户**明确说**「沉淀下来 / 写到 harness / 别再让我说第二遍」 | 立即沉淀 |
| Spec / RFC 拍板的**硬约束**（含 `docs/GOAL.md §4` / `M{N}_SPEC.md §决策点`） | 创建 harness 并指向 spec 锚点 |
| **跨项目重复**：同类问题在 ≥ 2 个项目 logs/ 中出现 | 沉淀为 harness 并加 `~/.claude/rules/{lang}/` 候选 inline |
| CI / grep gate **已经能机械检测**但还没 harness | 反向沉淀（gate 已就位 → 文档化原因 → 让人理解为什么这条 gate 是 hard fail） |

**禁止触发**：

- ❌ 单次踩坑（rec count = 1）且非用户/Spec 拍板 — 在 SESSION.md "已知债务"里登记即可，不进 harness
- ❌ 纯风格偏好（如"喜欢早 return"）— 走 `coding-style.md` 不进 harness
- ❌ 临时 workaround — 在 commit message + 代码注释里说明，不进 harness

---

## 4. 弃用 / 取消条件（Lifecycle）

| 状态 | 进入条件 | 退出条件 |
|---|---|---|
| `drafting` | 刚创建，未在 logs/ 反复验证 | 第 1 次实战命中 → `active` |
| `active` | recurrence ≥ 1 + grep gate 接管 | 见下两栏 |
| `merged` | **连续 30 天零新复现** + 自动化 grep / CI gate 接管 + 规则 inline 进 `~/.claude/rules/{lang}/{file}.md` | 终态（保留文件作为历史） |
| `deprecated` | 场景已不存在（如该模块被删 / 协议被换 / 上游 lib 消失） | 终态（保留文件不再加载） |

**晋升 active → merged 的强制 checklist**：

- [ ] §4 grep 命令存在且 CI 中 `grep ... | wc -l` 必须为 0 才放行
- [ ] §5 复现日志表 ≥ 1 条且最后一条 ≥ 30 天前
- [ ] 规则原文已写进 `~/.claude/rules/{lang}/{file}.md` 对应小节（inline）
- [ ] 本 harness 文件 frontmatter `status: merged` + `inline_target:` 指向 inline 后的锚点

**弃用 active → deprecated 的强制 checklist**：

- [ ] 场景源代码已删除（grep 0 条引用）或被替换协议覆盖
- [ ] 在 `log.md` 写一条 `## [YYYY-MM-DD] deprecate | C{NNN} | <原因>`
- [ ] frontmatter `status: deprecated`，文件不删

---

## 5. 与项目根 CLAUDE.md / 用户全局规则的关系

| 层 | 物理位置 | 优先级 | 用途 |
|---|---|---|---|
| 用户全局 | `~/.claude/CLAUDE.md` + `~/.claude/rules/golang/*.md` | 最高 | 跨项目通用 Go 规范 + 30min 复盘 / 早安复盘 / 日志收敛工作流 |
| 项目根 | `/Users/mac28/workspace/golangProject/im/CLAUDE.md` | 中 | im 项目特有约束（cses-client 工作目录、Phase tag 序列、`AllocSeqAndInsert` 等） |
| **Harness（本目录）** | `docs/harness/C{NNN}-*.md` | 中（与项目根并列） | **可执行的踩坑契约**：每条带 grep / 单测 / CI gate；晋升后 inline 进上层规则 |

**双向触发**：

- 项目根 `CLAUDE.md §8` 列出本目录所有 active harness 编号 → 任何 Go 改动都必须扫读
- 任何 active harness 的 §3 Required 与项目根 / 用户全局规则**冲突**时，**以更具体者为准**（harness > 项目根 > 用户全局）

---

## 6. 为什么不直接写到 CLAUDE.md？

CLAUDE.md 管"**写代码 / 写测试 / 提交规范**"是**指南性**的；harness 管"**这条坑过去踩了 N 次，现在用 grep / CI 物理拦死**"是**约束性**的。两者协作模式：

```
踩坑 → logs/{date}.json 自动记录
       ↓ ≥ 3 次复现 / 用户明确要求
创建 harness（drafting）
       ↓ 第 1 次实战命中
harness 转 active + 加 grep gate
       ↓ 30 天零复现 + gate 稳定
inline 进 ~/.claude/rules/{lang}/*.md → harness 转 merged
       ↓ 协议 / 模块消失
harness 转 deprecated
```

---

## 7. 维护契约

- 每完成一条 harness：在 `log.md` 追条 `## [YYYY-MM-DD] create | C{NNN} | <title>`
- 每次状态变更（active → merged / active → deprecated）：同上日志一条
- 每周日早安复盘第二步顺手扫一遍：检查 active 条目最近 30 天复现次数，决定是否晋升
- LLM 自动 ingest 触发时（用户全局规则 §4 "向前回看 → 收敛"）：把 logs/ 中 ≥ 2 次同类的整理成 drafting 候选
