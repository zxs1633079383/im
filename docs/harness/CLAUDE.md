# CLAUDE.md — docs/harness/ 模块级指令

> 这是 **harness 元目录**的项目级 Claude 指令。优先级低于用户全局 `~/.claude/CLAUDE.md` 与项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md`，但**高于本目录任何 C{NNN}-*.md 条目内的"操作型陈述"**——条目讲"业务约束"，本文件讲"如何在本目录新增 / 升级 / 弃用 / 引用 harness"。
>
> 必读前置：项目根 `CLAUDE.md §8 Harness Engineering`、本目录 `README.md`、`TEMPLATE.md`、`log.md`、`~/.claude/skills/im-harness-engineering/SKILL.md`。

---

## 0. 模块定位

- **本目录是 harness 元目录**：里面每个 `C{NNN}-*.md` 都是"可执行踩坑契约"，七段式（Trigger / Anti-Pattern / Required / Verification / Recurrence Log / Don't Over-Apply / Lifecycle）。
- **三文件分工**（不要混淆，不要交叉职责）：
  - `README.md` — **索引 + 流程文档**（§1 在册表 / §2 模板字段 / §3 触发条件 / §4 lifecycle / §5 与 CLAUDE.md 优先级）
  - `TEMPLATE.md` — **七段式模板 + frontmatter 字段**（拷过去填空即可）
  - `CLAUDE.md`（本文件）— **LLM 操作本目录的唯一流程规则**：新建 / 升级 / 弃用 / 引用应该做什么、不应该做什么
  - `log.md` — **晋升 / 弃用 / 创建流水**（时间倒序追加，状态变更必登记）
- **LLM 角色**：把本目录视为"项目的常驻 strong constraints 库"。任何对本目录的写操作（创建、改 frontmatter、改 §5 Recurrence Log）都必须走本 CLAUDE.md 的 SOP。

---

## 1. 影响范围（谁会被本目录约束）

- 所有 **Go 改动**（`server/**/*.go`）：项目根 `CLAUDE.md §8.1` 触发加载所有 `status: active` 条目；不合规直接被打回。
- 所有 **Angular 客户端改动**（`client/src/app/**/*.ts`）与 **Tauri Rust 改动**（`client/src-tauri/src/**/*.rs`）：同上加载范围。
- 所有 **集成测试 / httpexpect / migration** 类改动（`server/tests/integration/**`、`migrations/*.sql`）：受 C006 / C008 / C014 / C015 等条目机械约束。
- 跨仓镜像（feat/im-reactor-2 系列）：cses-client 仓库读 README.md §1.1 跨仓镜像表执行串行 gate。

> 任何新进会话开局必扫一遍 README.md §1 表头 + 本 CLAUDE.md，知道当前 active 条目编号上限与最近活跃条目。

---

## 2. 功能模块清单

| 文件 | 类型 | 改动权 | 说明 |
|---|---|---|---|
| `README.md` | 索引 + 流程文档 | 仅 §1 表新增条目 / §1.1 跨仓镜像表 / 状态变化时改条目状态列 | 不动 §2-§7 流程文本（动需用户拍板） |
| `TEMPLATE.md` | 模板 | 几乎不动（动需用户拍板 + 全量条目回填字段） | 七段式 + frontmatter 字段定义 |
| `CLAUDE.md`（本文件）| 模块流程指令 | 仅在 root CLAUDE.md §8 流程升级时同步 | LLM 操作本目录的唯一规则 |
| `log.md` | 流水日志 | **每次状态变化必追条**（create / activate / merge / deprecate） | 时间倒序，不删历史 |
| `C001..C016-*.md` | 条目正文 | 仅在复现 / 状态变更时改 §5 Recurrence Log + frontmatter 状态/计数；§1-§4 / §6-§7 改动需用户拍板 | 七段式正文 |

**当前编号上限**：`C016`。新增必须 `C017` 起步，**编号严格递增不复用**（弃用条目不释放编号）。

---

## 3. SOP — 标准操作流程

### 3.1 新增条目（drafting → active）

```
判定触发条件（root §8.2 三选一）
    ↓ 满足
跑 grep / 日志统计验证复现次数
    grep -rn "<root cause keyword>" /workspace/java/logs/  # 验证 ≥ 3 次
    或确认用户/Spec 拍板锚点
    ↓ 通过
确定下一编号 = (ls C*.md | 最大编号) + 1
    ↓
cp TEMPLATE.md C{NNN}-{kebab-slug}.md
    ↓ 填七段 + frontmatter
更新 README.md §1 表新增一行（编号 / 标题 / 状态 / 来源日志 / 复现次数）
    ↓
log.md 追条：## [YYYY-MM-DD] create | C{NNN} | <title>
    ↓
（如需）同步 root CLAUDE.md §8.4 表（仅当 active 且对 Go 改动产生强约束时）
```

### 3.2 已有条目命中复现

```
当前事故 root cause 命中某 C{NNN} §1 Trigger
    ↓
该条目 §5 Recurrence Log 追一行：日期 / 触发场景 / 引用日志 / 处置
    ↓
frontmatter 更新：last_recurred / recurrence_count + 1
    ↓
status: drafting → active（如果是第一次实战命中）
    ↓
log.md 追条：## [YYYY-MM-DD] activate | C{NNN} drafting → active（仅状态跃迁时）
```

### 3.3 晋升（active → merged）

按 §6 checklist 全过 → 改 frontmatter `status: merged` + `inline_target` → README.md §1 状态列改 `merged` → log.md 追条。

### 3.4 弃用（active → deprecated）

按 §6 checklist 全过 → 改 frontmatter `status: deprecated` → README.md §1 状态列改 `deprecated`（**不删行不删文件**）→ log.md 追条。

---

## 4. Pre-commit 自检清单

每次写 / 改 harness 后，commit 前必须过以下 5 项（缺一不可）：

- [ ] **① 新条目编号无 gap**：`ls docs/harness/C*.md | sort` 验证 C001..C{NNN} 连续，新增 = 当前 max + 1，且未复用弃用编号
- [ ] **② frontmatter 字段齐全**：`id / title / status / created / last_recurred / recurrence_count / source_logs / applies_to`，merged 状态额外 `inline_target`
- [ ] **③ §4 Verification 有 grep 命令且本地可执行返回 0 条**：复制 §4.1 grep 命令到终端跑一遍，必须返回 0（否则说明项目内还有违例代码，先修代码再立 harness）
- [ ] **④ README.md §1 表已加 / 已改对应行**：编号 / 标题 / 状态 / 来源日志 / 复现次数 四列同步
- [ ] **⑤ 若涉及状态跃迁**（drafting→active / active→merged / active→deprecated）→ `log.md` 已追一行 `## [YYYY-MM-DD] {create|activate|merge|deprecate} | C{NNN} | <理由>`

---

## 5. Commit 规范

沿用项目根 `CLAUDE.md §3` 与 `~/.claude/rules/common/git-workflow.md` 的 Conventional Commits（中文 description）：

```
docs(harness): 新增 C017 xxx 沉淀踩坑契约

来源日志：/workspace/java/logs/2026-05-18.json#L42
触发条件：≥3 次复现（2026-05-{10,14,18}）
覆盖 §1 Trigger glob：server/internal/service/foo/**/*.go
配套 §4.1 grep gate 本地验证 0 条。
```

强制：

- `scope` 必须是 `harness`（不是 `docs` 不是 `chore`）
- type 三选一：`docs`（新增 / 改正文）/ `refactor`（合并条目 / 改模板）/ `chore`（仅改 log.md 流水）
- body **必须**写出**来源日志路径**（`/workspace/java/logs/{date}.json#L{line}` 或 spec / commit 锚点）；无来源 = 不该立 harness
- 单 commit 单条目：新增 C017 与改 C014 不能混 commit

---

## 6. 约束规范（硬约束，不可妥协）

### 6.1 新建条件 = 项目根 §8.2 触发条件之一

满足任一即可建：
- ≥ 3 次复现（同一 root cause 在 logs/ 累计 ≥ 3）
- 用户明确说"沉淀下来 / 写到 harness / 别再让我说第二遍"
- Spec / RFC / GOAL.md §4 硬约束拍板
- 跨项目重复（≥ 2 个项目 logs/ 出现同类）

### 6.2 禁止触发清单（root §8.2 镜像）

- ❌ 单次踩坑 + 非用户/Spec 拍板 → SESSION.md "已知债务"
- ❌ 纯风格偏好（喜欢早 return / 命名习惯）→ 走 `~/.claude/rules/golang/coding-style.md`
- ❌ 临时 workaround → commit message + 代码注释
- ❌ "我觉得这个挺重要的可能下次会踩" → 拿日志数据来再说

### 6.3 编号严格递增不复用

- 下一条 = `max(ls C*.md) + 1`
- 弃用条目（`status: deprecated`）**不删文件 + 编号不释放**
- 极少数同主题分支用 `C{NNN}a` `C{NNN}b`（避免，除非用户拍板）

### 6.4 弃用不删文件

`status: deprecated` 即可。文件保留作历史索引；README.md §1 表行保留并显示 `deprecated`。删除文件会破坏过往 commit / log 引用，绝对禁止。

### 6.5 晋升 active → merged 必须 inline

晋升前必须把规则原文 inline 进 `~/.claude/rules/{lang}/{file}.md` 对应小节，frontmatter `inline_target:` 指向 inline 锚点。**没有 inline 不许 merged**——否则 harness 终态后规则反而失去常驻 context 加载。

---

## 7. 对应 Harness 映射（本目录是元目录 → 与正文条目的接口对齐）

本 CLAUDE.md 是元目录指令，最相关的两个条目：

| 条目 | 接口对齐点 | 说明 |
|---|---|---|
| **C008** — handler-coverage-gate | §4 Verification 形态范例 | C008 §4.2 `scripts/check-handler-coverage.sh` 是「grep gate 接管」的样板：新条目 §4.1 grep 风格、§4.2 CI gate 命名规范向 C008 对齐 |
| **C014** — test coverage 100% | §4 Verification 单测/集成测试规范 | C014 §4.3 / §4.4 是「每条 harness §4 都要有可机械执行的单测 / 集成测试」的范例。新条目 §4.3 / §4.4 写法向 C014 对齐 |
| **C016** — msg_update single gate | §1 Trigger glob + §6 Don't Over-Apply 写法 | C016 §1 给出了具体 path glob + 关键词组合（不是"所有 Go 代码"），§6 列出了明确边界。新条目这两段向 C016 对齐 |

> 写新条目时若不确定 §4 / §1 / §6 怎么写，直接参考上表三个条目的对应小节。

---

## 8. Update / Insert 规则（详尽流程）

### 8.1 新增 C{NNN} 流程

1. 确认编号：`ls docs/harness/C*.md | sed 's/.*C\([0-9]*\).*/\1/' | sort -n | tail -1` → +1
2. 复制模板：`cp docs/harness/TEMPLATE.md docs/harness/C{NNN}-{kebab-slug}.md`
3. 填七段：Trigger 必须有 path glob + 关键词；Anti-Pattern 必须有可 grep 的代码片段；Required 必须有可复制代码；§4 必须有可机械执行的 grep / CI gate；§5 至少 1 行复现记录；§6 给出明确边界；§7 写晋升 / 弃用门槛
4. 填 frontmatter：`id / title / status: drafting / created: today / last_recurred: today / recurrence_count: 1 / source_logs / applies_to`
5. 跑 §4.1 grep 命令本地验证返回 0 条
6. 更新 `README.md §1` 表加一行（紧贴最大编号下）
7. 写 `log.md` 追条：`## [YYYY-MM-DD] create | C{NNN} | <title>`
8. （如需常驻 Go 改动加载）同步 `项目根 CLAUDE.md §8.4` 表
9. commit：`docs(harness): 新增 C{NNN} xxx` + body 带来源日志

### 8.2 升级 active → merged checklist

- [ ] §4.1 grep 命令存在且 CI 中 `grep ... | wc -l` 必须为 0 才放行（接管为 hard fail）
- [ ] §5 复现日志表 ≥ 1 条且**最后一条 ≥ 30 天前**（连续 30 天零新复现）
- [ ] 规则原文已 inline 进 `~/.claude/rules/{lang}/{file}.md` 对应小节
- [ ] frontmatter：`status: merged` + `inline_target: ~/.claude/rules/{lang}/{file}.md#anchor`
- [ ] README.md §1 表状态列改 `merged`
- [ ] `log.md` 追条：`## [YYYY-MM-DD] merge | C{NNN} → ~/.claude/rules/{lang}/{file}.md#anchor`
- [ ] 项目根 `CLAUDE.md §8.4` 表状态列同步改 `merged`（如有列出）

### 8.3 弃用 active → deprecated checklist

- [ ] 场景源代码已删除（grep 0 条引用）或被替换协议覆盖（给出替换 commit / RFC 引用）
- [ ] frontmatter：`status: deprecated`（不动其他字段）
- [ ] README.md §1 表状态列改 `deprecated`（**不删行**）
- [ ] `log.md` 追条：`## [YYYY-MM-DD] deprecate | C{NNN} | <废弃原因 + 替换条目编号 / 协议>`
- [ ] 项目根 `CLAUDE.md §8.4` 表状态列同步改 `deprecated`（如有列出）
- [ ] **不删文件**（破坏 commit / log 历史引用）

### 8.4 Recurrence Log 加新行的格式

每次同 root cause 再次踩坑，**必须**在该条目 §5 表追一行（时间倒序或顺序均可，本目录默认按 # 编号顺序）：

```markdown
| # | 日期       | 触发场景                            | 引用日志                                | 处置                                |
|---|------------|-------------------------------------|-----------------------------------------|-------------------------------------|
| N | YYYY-MM-DD | 一句话描述（≤ 40 字）              | /workspace/java/logs/YYYY-MM-DD.json#L{line} | commit hash / 回滚 / 加测试 / etc |
```

同时改 frontmatter：

- `last_recurred: YYYY-MM-DD`（最新这次）
- `recurrence_count: +1`

如果触发场景不在 §1 Trigger glob 范围内 → 不是同一 root cause，**不要硬塞 §5**，应该新建另一条 harness 或扩 §1 glob（扩 glob 要在 commit body 说明）。

---

## 9. 文档关联

| 上游（强制阅读） | 用途 |
|---|---|
| 项目根 `/Users/mac28/workspace/golangProject/im/CLAUDE.md §8` | Harness Engineering 总章；本 CLAUDE.md 是其 §8 的"如何维护本目录"细化 |
| 项目根 `docs/GOAL.md §4 硬约束` | 部分 harness 是这里的硬约束落地（如 C001 / C005）；新增 harness 若指向 GOAL.md 硬约束必须给出锚点 |
| `~/.claude/rules/common/git-workflow.md` | Commit 规范母版；本 CLAUDE.md §5 是其在 harness 场景的特化 |
| `~/.claude/rules/golang/*.md` | merged 后 inline 目的地；§8.2 checklist 第 3 项指向这里 |
| `~/.claude/skills/im-harness-engineering/SKILL.md` | 跨项目 harness 方法论；本 CLAUDE.md 是 im 项目特化版 |

| 下游（被本文件约束）| 用途 |
|---|---|
| 本目录 `README.md` | §1 表与本 CLAUDE.md §3 / §8.1 / §8.2 / §8.3 流程一一对应 |
| 本目录 `TEMPLATE.md` | 七段式模板；本 CLAUDE.md §8.1 第 2-4 步直接调用 |
| 本目录 `log.md` | 流水日志；§4 第 5 项 / §8.{1,2,3} 末项强制追条 |
| 本目录 `C{NNN}-*.md` | 所有条目正文；§8.4 Recurrence Log 加行规范作用于此 |

---

> 最后一句：harness 不是"想到了就写一条"。**没有日志数据 / 没有用户拍板 / 没有 Spec 锚点 → 不立 harness**。立错条目的代价比不立更高（噪音常驻 context）。宁可让 SESSION.md "已知债务"先吃一段时间，等数据攒齐再沉淀。
