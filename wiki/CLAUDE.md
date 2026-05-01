# wiki/CLAUDE.md — Schema（im 项目 LLM Wiki 维护契约）

> 这份文件是 **wiki 的 Schema**：定义页面类型、命名规范、摄入工作流、质量标准、矛盾处理。
> 优先级：低于项目根 `CLAUDE.md`、高于默认 LLM 行为。LLM 操作 `wiki/` 时必读。
>
> 与项目根 `CLAUDE.md` 的关系：根 `CLAUDE.md` 管「写代码 / 写测试 / 提交规范」；本文件管「写 / 维护知识页面」。

---

## 1. 三层架构（在本项目里的具体落点）

| 层 | 物理位置 | 谁负责 | 是否可变 |
|----|---------|-------|---------|
| **Raw（源）** | `docs/`、`server/docs/`、`server/internal/**/*.go`、`SESSION.md`、Git 历史 | 人 + 代码 | 不可变（按时间沉淀） |
| **Wiki** | `wiki/`（本目录） | LLM 主导，人审阅 | 可变（compile，不是 retrieve） |
| **Schema** | `wiki/CLAUDE.md` + `wiki/index.md` 的目录约定 | 人 + LLM 共同进化 | 半可变（改要写 log） |

**核心心法**：不是 RAG 检索，而是 LLM 把 Raw 编译成结构化的 Wiki，让知识跨会话累积。

---

## 2. 页面类型

| 类型 | 目录 | 命名 | 用途 |
|------|------|------|------|
| Entity | `entities/` | `kebab-case.md`（组件/文件/函数名） | 一个具体的代码实体：服务、组件、关键函数、数据表 |
| Concept | `concepts/` | `kebab-case.md`（概念名） | 抽象思想：seq 单调、跨 pod 推送、cookie 单栈 |
| Flow | `flows/` | `kebab-case.md`（流名） | 跨多组件的端到端数据流（送消息、增量同步、auth） |
| Milestone | `milestones/` | `M{N}-{slug}.md` | 一个里程碑（M1–M6）的范围、状态、产出 |
| Decision | `decisions/` | `kebab-case.md` | 硬约束、技术选型、不可妥协的决策 |
| Comparison | `comparisons/`（按需建） | `A-vs-B.md` | 两件事的对比（im vs Mattermost、seq vs bitmap） |
| Synthesis | `syntheses/`（按需建） | `kebab-case.md` | 深度问题的合成回答（性能调优、并发模式落地） |

**强制**：每个页面必须含 frontmatter（见 §4），必须双向链接（用 `[[wikilinks]]`）。

---

## 3. 三大操作

### 3.1 Ingest（摄入）

触发条件：
- 用户说「把 X 加进 wiki」「整理 Y 到 wiki」
- 项目里出现了 Wiki 还没编译过的源（新的 `*.md`、新的关键 commit、tag、设计决定）
- 完成一个 milestone（如 M4 收尾）

步骤：
1. 读源 → 列出**5 件以上**新事实/决策/组件
2. 决定影响哪些已有页面（updated）+ 是否需要新增页面（created）
3. 一次性更新 `index.md`（一行摘要 + 路径）
4. 更新或创建对应 entity / concept / flow / milestone / decision 页
5. 在 `log.md` 追加一条 `## [YYYY-MM-DD] ingest | <title>` 块，列出 source、touched files、key insights
6. 检查双向链接：新页要有 ≥1 backlink，孤儿页面要登记到 §6 lint 待办

### 3.2 Query（查询）

触发条件：
- 用户在新会话里问 wiki 已覆盖的问题
- 自己在做 plan / review 时需要查跨 commit 的事实

步骤：
1. 优先读 `index.md` → 定位相关页 → 必要时跟 wikilinks 跳转
2. 答案需要 cite：直接给出 `[[wiki/entities/foo.md]]` 或 `wiki/flows/send-message.md` 路径
3. 如果是有价值的合成（用户给反馈说「这个解释好，记下来」），就**反向归档**为 synthesis 页

### 3.3 Lint（巡检）

触发条件：
- 周末早安复盘第二步（用户全局规则要求）
- 完成一个 milestone 后
- 用户说「跑一下 wiki lint」

检查项（优先级降序）：
1. **过时事实**：每个页面 frontmatter 的 `last_verified` 距今 > 30 天，列出 → 重新对照源
2. **矛盾**：同一事实在两处给出不同答案（如 routing TTL = 45s vs 30s）
3. **断链**：`[[xxx]]` 指向不存在的页
4. **孤儿**：没有 backlink 的页面（除 root index）
5. **重复**：两页讲同一件事（合并或互链）
6. **新空白**：项目里出现了重要源但 wiki 没编译

输出：写到 `log.md` 一条 `## [YYYY-MM-DD] lint | health: X/10`。

---

## 4. Frontmatter 模板（强制）

每个页面顶部：

```yaml
---
type: entity | concept | flow | milestone | decision | comparison | synthesis
title: 页面标题（中文 OK）
status: stable | drafting | stale | deprecated
last_verified: 2026-04-28        # 最后一次对照源校验的日期
sources:                          # 来源（raw 或代码）
  - server/internal/repo/message.go:79
  - server/docs/BACKEND.md#§4.1
related:                          # 相关页面（双向链接的种子）
  - entities/cross-pod-push
  - concepts/seq-cursor
confidence: high | medium | low   # v2 信心位
---
```

**信心位规则**（从 LLM Wiki v2 的 confidence scoring）：
- `high` = 直接来自代码 / commit / 用户明确确认
- `medium` = 基于多份文档推断、但未在代码里 grep 验证
- `low` = 单一来源 / 老旧文档 / 未交叉验证

---

## 5. 双向链接规范

- 用 `[[entities/foo]]` 而不是 `[entities/foo](entities/foo.md)`（Obsidian 友好）
- 在 frontmatter 的 `related:` 里登记主要关联，正文用 `[[xxx]]` 自然引用
- 每条 `[[xxx]]` 都隐含 typed relation，如：
  - `uses` —— 「Service 用 Repo」
  - `supersedes` —— 「v0.6.0 替换 v0.5.x」
  - `contradicts` —— 「文档 A 和文档 B 给的 TTL 不一致」（必须解决）
  - `caused` —— 「commit 43c05ab fix 了 httpexpect ?-encode 坑」

---

## 6. 矛盾处理（critical）

发现矛盾时**禁止默默用其中一个**：

1. 在 `log.md` 写一条 `## [YYYY-MM-DD] contradiction | <topic>` 记录两端
2. 去代码 / Git 历史里查真相
3. 更新错的那一端 → 把 frontmatter 的 `status` 改 `stale`，加 `superseded_by:` 指向正确版本
4. 在正确页加 `## 历史误解` 节，避免后人重蹈

---

## 7. 不要做的事

- ❌ 把 Raw 内容**整段复制**到 Wiki —— Wiki 是合成 + 摘要，不是镜像
- ❌ 写「TODO 待补充」却不在 `log.md` 登记
- ❌ 长篇大论 —— 单页 ≤ 300 行，超了拆分
- ❌ 没有 frontmatter
- ❌ 没有 `[[wikilinks]]` 的孤立页面
- ❌ 「未来再说」的内容写进 wiki —— 那是 SESSION.md 的活
- ❌ 直接覆盖一份 stale 页面而不留版本痕迹（要写 supersession）

---

## 8. 启动一个新会话时怎么用

如果会话目标是**改代码**：
1. 读 `wiki/index.md`
2. 找相关 entity / flow，读 1–2 页
3. 跳到代码

如果会话目标是**理解/定位**：
1. 直接 `wiki/index.md` 浏览
2. 跟 wikilinks 钻

如果会话目标是**给 wiki 加东西**：
1. 读本文件
2. 选页面类型
3. 走 §3.1 ingest 流程

---

## 9. 与 GitNexus 的协作

GitNexus 提供**代码图谱**（实时、grep 替代品）；本 wiki 提供**人化的合成知识**（决策、约束、历史）。两者互补：

- 找「这个函数被谁调」→ GitNexus
- 找「为什么 TTL 是 45s」→ Wiki

改代码前的 impact / detect_changes 走 GitNexus；改完一段写写法本身的合成走 Wiki。

---

## 10. 路线图（v2 渐进）

当前是 v1（基础三层 + Ingest/Query/Lint）。后续按需要进阶：

- v1.1：confidence scoring 写入 frontmatter（已支持）
- v1.2：lint 自动化脚本（`wiki/scripts/lint.sh`）
- v1.3：Obsidian Vault sync（用户已有 Vault，可符号链接 `wiki/` 进去）
- v2.0：typed relations 自动推断 + 自愈 lint
