# Harness Engineer 最佳实战 —— 长运行 / 目标驱动的工程化作业系统

> **本文档同时面向两类读者：人 + AI（Claude / Agent）**。
> - **人**：把这套体系作为方法论参考，按需阅读 / 实施。
> - **AI**：本文是你的**可执行协议**——读完必须按 🤖 §「给 AI 读者的执行协议」自检并落地，无论你正处于**普通任务 / 长运行目标任务 / 完全自主（autonomous）任务**哪种模式。
>
> **渐进式披露（不要拷贝进你的 CLAUDE.md）**：
>
> 每个项目都有自己的 `CLAUDE.md` 约束（架构 / 业务 / 代码规范），把本文整段塞进去会**互相污染**。正确做法：
>
> 1. 在项目的 `CLAUDE.md` 加一段 **~10 行的索引指针**（§12.1 给出模板），指过来本文档。
> 2. Claude / Agent 启动加载 CLAUDE.md 时只看到这段索引（成本 ~10 行）。
> 3. 当且仅当**触发条件**命中（同根因 ≥ 3 次 / 用户说要沉淀 / 跨会话开局收尾 / autonomous 任务开始），Claude 才主动 `Read` 本文获取完整能力。
>
> 这就是护城河文 §7.2 的「三级渐进式索引」在 Claude Code 长运行任务约束上的落地：CLAUDE.md = Layer A（50 行级），`docs/harness/README.md` = Layer B（150 行级），本文 + 各 harness 条目 = Layer C（按需读）。

---

## 🤖 给 AI 读者的执行协议（强制 / 第一时间执行）

> 如果你是 Claude / Agent 读到本文，**这一节优先级高于本文其余所有内容**。先按这里 5 步执行，再继续读其他章节。

### 协议 P1 — 自检你当前所处的任务模式

无论你被触发的方式是什么，先在脑中分类：

| 任务模式 | 特征 | 本协议如何应用 |
|---|---|---|
| **普通任务** | 一问一答 / 单文件改 / 5-30 min 内完成 | **执行 P3 + P4**（铁律自检 + 收尾沉淀），不必跑 §7.1 完整开局 |
| **长运行目标任务** | 跨会话 / 跨 Phase / 有明确 L1/L2 目标 | **执行 P2 + P3 + P4 + P5**（完整开局 + 索引自助安装） |
| **Autonomous 任务** | 用户授权连续多步、无中途确认（/loop / cron / 长链 agent）| **执行 P2 + P3 + P4 + P5 + 每步前重新跑 P3**（铁律是每步硬约束，不是事后检查）|

**无法判断模式时**：默认按"长运行目标任务"对待——多做不亏，漏做有代价。

### 协议 P2 — 会话/任务开局 4 步自检

任务开始前**先跑这 4 个命令**，再动手（即便用户催，也先跑完）：

```bash
git status && git log --oneline -5                       # L1 状态
test -f SESSION.md && head -80 SESSION.md                # 上次留下的快照
test -f /workspace/java/logs/$(date +%Y-%m-%d).json \
  && tail -n 30 /workspace/java/logs/$(date +%Y-%m-%d).json   # 今天问过什么
test -d docs/harness && ls docs/harness/                 # active 契约
```

任何一条返回"文件/目录不存在"——**不是错误**，而是触发协议 P5（自助安装索引）的信号。

### 协议 P3 — 三铁律是每步硬前置条件（不是事后总结）

在 **autonomous 模式**或**长任务**下，每个独立步骤（≈每次 commit 或每次工具调用链）开始前都要心里跑一遍：

1. 这一步产生的新认知 / 决策 / 踩坑——**结束这一步前**追加进 `logs/{date}.json`（如有 hook 自动落，无需额外动作；没 hook 时手动 `echo` 一行 jsonl）。
2. 如果这一步发现的根因和**logs 历史 ≥ 2 次同类**重合 → 触发铁律 ② → **当前步内**新建 `docs/harness/C{NNN}-*.md`，不等"以后"。
3. 如果当前步新建 / 修改 / 退役了任何 harness → **同一 commit 内**同步改 `CLAUDE.md` 索引表 + `docs/harness/README.md`。

**警示**：autonomous 任务里最容易在第 17 步发现「这跟第 3 步踩的是同一个坑」——但已经错过沉淀窗口。把 P3 内化成每步前置条件是唯一解法。

### 协议 P4 — 任务收尾 5 必做

不论是被打断、被用户标记完成、还是 autonomous 任务自然结束，**收尾前都跑这 5 项**：

- [ ] 所有变更 `git commit`（或在最终输出里明确说为什么不 commit）
- [ ] 更新 `SESSION.md` §1 §2 §3（新分支/commit / 完成项 / 新待决分叉）
- [ ] 新决策追加到 `docs/GOAL.md §硬约束` 或 `MEMORY.md` 索引
- [ ] 30min+ 耗时任务做 `retro-30min` 复盘并写入 `logs/{date}.json`
- [ ] 本次会话新发现的坑（满足 §5.1 触发器）落进 `docs/harness/`，并同步 CLAUDE.md 索引

**收尾完成后才能向用户输出"任务完成"。** 任何省略 P4 后宣布完成的行为，是本协议明确禁止的反模式。

### 协议 P5 — 索引自助安装（当 CLAUDE.md 没有指向本文档时）

**触发条件**：P2 开局自检发现项目根 `CLAUDE.md` 没有指向本文档的索引区块（grep 关键词 `harness-Engineer最佳实战.md` 无命中）。

**执行步骤**：

1. **不要静默修改 CLAUDE.md**——这是用户的核心配置文件，必须先征求同意。
2. 向用户提一句话请求（**用 AskUserQuestion 或直接在回复里提问**）：

   > 我检测到本项目 `CLAUDE.md` 没有指向 `docs/tx/harness-Engineer最佳实战.md` 的索引（铁律 ③ 要求）。是否允许我加一段 ~10 行索引指针，让后续会话自动按 Harness 体系执行？(Y/n)

3. 用户同意后，按 §12.1 模板把那段 ~10 行索引**追加**到 CLAUDE.md（找一个合适的位置，通常是项目目标声明之后、详细约束之前；如果 CLAUDE.md 已有"## 长运行任务约束"或类似标题段，**改写**而不是追加）。
4. 同次 commit 信息：`docs(claude): 加 Harness Engineer 实战索引指针，启用长运行任务约束`。
5. **本次任务结束前不重复触发 P5**——避免在同一会话里反复弹同样的请求。

**当 CLAUDE.md 不存在时**：先问用户"项目还没 CLAUDE.md，要不要我建一个基础模板（含本文索引）？"，不要擅自创建。

### 协议 P6 — 何时主动深读本文档（按需 Read 触发表）

| 触发场景 | 主动读 | 跳过 |
|---|---|---|
| 用户首次提到"harness / 沉淀 / 别再踩坑 / 长运行任务" | §0 + §3 + §5 | — |
| 第一次准备新建 `C{NNN}` 条目 | §4 模板 + §5 触发器 + §3.3 生命周期 | 其余章节 |
| 准备把 active 升级 merged / deprecated | §3.3 + §3.4 + §8 闭环验证 | — |
| Autonomous 任务开始前 | §0 三铁律 + §7 开局收尾 + 本节 P1-P5 | §6 §10 |
| 用户问"怎么不再踩同一个坑" | §0 + §8 + §9 反模式 | — |
| 30 天回看 / 周复盘 | §8 闭环验证 + §10 死法警示 | — |
| 不在以上任何场景 | 不读 | — |

**精确触发是渐进式披露的关键**——不要全文加载，按表查阅。
>
> 这是一份**落地视角**的 Harness Engineering 实战手册，承接 [`Harness 护城河.md`](./Harness%20护城河.md) 的理论框架（三支柱 / 五层存储 × 五种类型 × 三级成熟度 / 知识 = 护城河），但聚焦回答一个具体的问题：
>
> **"我手上有一个跨多天、多 Phase、多人协作的长运行项目（例如 im 这种替换 Mattermost 的 IM 后端），怎么让它在 AI Agent 反复进出、模型反复迭代、人反复离场的现实里，还能稳定推进？"**
>
> 配套引用的真实事实来源：
> - 项目根 [`CLAUDE.md`](../../CLAUDE.md) §8 Harness Engineering 条款
> - [`docs/harness/`](../harness/) 9 条 active 契约（C001-C009）+ `TEMPLATE.md` + `log.md` + `README.md` lifecycle 表
> - [`docs/GOAL.md`](../GOAL.md) / [`SESSION.md`](../../SESSION.md) / [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md) 三件套
> - 用户全局 `~/.claude/CLAUDE.md` 的 5 条铁律（30min 复盘 / 统一日志 / 早安复盘 / 收敛回看 / 全局生效）
> - 护城河文 §6 INIT-Run-ARCHIVE 知识流 / §7 三级索引

---

## 🔴 三个即时同步铁律（最高优先级）

整个 Harness 体系**只靠这三个回路活着**。任意一环断掉，整个体系一周内退化为博物馆。

```
┌─────────────────────────────────────────────────────────────────┐
│                                                                  │
│   ① 踩坑 → 即时写 logs/{date}.json                                │
│       └─ UserPromptSubmit hook 自动落，零手动负担                  │
│                                                                  │
│   ② 同根因 ≥ 3 次  /  用户明确  /  Spec 拍板                      │
│       → 即时升级为 docs/harness/C{NNN}-*.md（同一会话内完成）      │
│       └─ 不等"以后整理"，"以后"永远不会来                          │
│                                                                  │
│   ③ harness/ 任何 active 增删 → 即时更新 CLAUDE.md §X 索引表       │
│       └─ CLAUDE.md 没索引到 = Claude 不会加载 = 等于不存在         │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
        ↓                ↓                ↓
   日志真实可信     harness 不掉队    Claude 每次启动都读到
```

**铁律的可执行检查**（每次会话收尾前自查）：

```bash
# 检查 ①：今日日志有内容
test -s /workspace/java/logs/$(date +%Y-%m-%d).json || echo "❌ 今天还没落日志"

# 检查 ②：今日 ≥3 次同根因的话题是否已沉淀为 harness
grep -i "<今天反复出现的关键词>" /workspace/java/logs/$(date +%Y-%m-%d).json | wc -l
# ≥ 3 且 ls docs/harness/ 没新增 → ❌ 该沉淀的没沉淀

# 检查 ③：harness/ 文件数 vs CLAUDE.md 索引表行数
ls docs/harness/C*.md | wc -l
grep -E '^\| C[0-9]+' CLAUDE.md | wc -l
# 两个数字必须相等，否则 ❌ CLAUDE.md 索引漏了
```

**断链的后果**（每一条都真实发生过）：

| 断哪环 | 短期后果 | 长期后果 |
|---|---|---|
| ① 不落日志 | 一周后忘记踩过什么坑 | 同一个 bug 跨 session 重复修 5 次 |
| ② 不即时沉淀 | "我记得有这个坑"但找不到契约 | 新人 / 新 agent 进来必踩 |
| ③ CLAUDE.md 不索引 | harness 存在但 Claude 不知道 | 写满 50 条 harness 等于 0 条 |

> **本文档剩余所有章节，都是这三个铁律的展开和工具化。读不进剩余内容 → 至少把这三条印在脑子里。**

---

## 0. 一句话定义

**Harness Engineer ≠ Prompt Engineer ≠ DevOps**：

| 角色 | 关心什么 | 时间尺度 | 产物 |
|---|---|---|---|
| Prompt Engineer | 单次对话怎么让模型给出好答案 | 秒～分钟 | prompt 模板 |
| DevOps | 服务怎么稳定跑、怎么部署 | 小时～天 | CI/CD pipeline |
| **Harness Engineer** | **AI Agent + 人，跨多天反复进出同一个长任务，怎么不退步** | **周～季度** | **约束、契约、记忆、复盘的可执行闭环** |

护城河文那句"工作流是手段，知识是目的"是对的。但落到工程上要更进一步：

> **Harness 是让"模型 + 工程师"这个组合体的输出，跨时间单调不退化的工程系统。**

不退化 = 同一个坑不再踩第二次，同一个决策不再推导第二次，同一份上下文不再重新拼第二次。

---

## 1. 长运行项目的三层目标 —— 给"工作流"先安一个坐标

护城河文讲了知识分层，但少讲了**目标**也要分层。Harness Engineer 的第一步是给当前手上这个长任务区分清楚三层 goal：

| 层级 | 时间窗口 | 目标载体 | 评估方式 | 在 im 项目的实例 |
|---|---|---|---|---|
| **L0 单步目标** | 5-30 min | 当前会话/任务 | tool call 结果、测试是否通过 | 「把 `/api/messages/:id/received` 加上 envelope 中间件」 |
| **L1 阶段目标** | 1-7 天 | Phase / 模块 / sprint | git tag + lint/test/手工三验证 | 「v0.7.3 Phase 2 把 channel-read 6 处切到 im REST」 |
| **L2 项目目标** | 1-6 月 | 项目终态 | 是否能"下线 Mattermost" | 「替换 csesapi → im，不走回切」`docs/GOAL.md` |

三层不共享同一个文件、不共享同一种产物、也不共享同一个评估周期：

- **L0** 活在当前会话上下文里 + TaskCreate todo + 单次 commit
- **L1** 活在 `git tag` + commit history + `docs/CSES_CLIENT_CUTOVER.md` 这类 Phase plan 文档里
- **L2** 活在 `docs/GOAL.md` + `docs/ARCHITECTURE.md` + 长期不动的 SLO/契约里

**为什么必须分层**：模型 / 人 / agent 都有强烈的"漂移倾向"——L0 一步一步做，做着做着就忘了自己在哪个 Phase（L1 漂移），做着做着就忘了为什么要做这个项目（L2 漂移）。Harness 要做的事就是把 L0 / L1 / L2 之间的**双向 trace** 物化成文件，让每一步都能向上回溯到"我现在到底在为哪个 L2 目标服务"。

---

## 2. 长运行环境的四件武器（im 项目实测组合）

下面这套组合是 im 项目跑了 ~3 个月后稳定下来的实战版本。**注意：它不是"理论最优"，是"实际能跑住"的。**

```
┌─────────────────────────────────────────────────────────────────┐
│                  长运行 / 目标驱动 工程系统                       │
├──────────────┬──────────────┬──────────────┬───────────────────┤
│   GOAL       │   SESSION    │   harness/   │   logs/           │
│  (常量)      │  (短期记忆)  │  (中期记忆) │  (流水追踪)        │
├──────────────┼──────────────┼──────────────┼───────────────────┤
│ L2 目标      │ L0+L1 进度   │ 踩坑契约     │ 单次会话原始记录   │
│ 硬约束       │ 待决分叉     │ grep/CI gate │ 30min/早安复盘     │
│ 不要做的事   │ 三仓 HEAD    │ 复现日志     │ 重复 pattern 检测  │
├──────────────┼──────────────┼──────────────┼───────────────────┤
│ 月度更新     │ 每次会话更新 │ 每条目独立   │ 每天追加 + 周回看  │
│ docs/GOAL.md │ SESSION.md   │ docs/harness/│ /workspace/logs/   │
└──────────────┴──────────────┴──────────────┴───────────────────┘
            ↓              ↓              ↓              ↓
       为什么 ←  → 现在在哪 ←  → 不要踩什么坑 ←  → 实际跑成什么样
```

### 2.1 GOAL.md —— L2 目标常量

**职责**：让任何 Agent 在任何时候打开这个项目，30 秒内知道：
- 我们为什么做这个项目（终极用户价值）
- 哪些事**绝对不要做**（硬约束，例如"不做流量回切")
- 当前里程碑 + 成功标准（measurable）
- 反模式（已经被否定的方案，写明否定理由防止重新提）

**写法关键**：用动词式承诺 + 量化指标，不要用形容词。

❌ "我们要做高性能的 IM"
✅ "p99 push latency < 80ms @ 150k 并发连接（k6 v4-load 验证）"

### 2.2 SESSION.md —— L0+L1 短期记忆

**职责**：单文件、永远只保留**当前**状态，过时即删。每次会话开局先读，收尾必更新。

固定 5 节：

```
§0 一句话启动 ⭐    — 复制粘贴就能在新会话续上
§1 最新 tag / commit / branch — 三仓库 HEAD 表（事实，不要解读）
§2 已完成项         — 上次会话推进了什么（事实）
§3 待决分叉         — 等用户拍板的选择题（每条带选项 A/B + 推荐理由）
§4 backlog          — 已知但还没做的任务清单
```

**铁律**：
- 不要在 SESSION.md 里写"思考过程""设计权衡"——那是 GOAL.md / harness 的事
- 过时信息必须 **delete**，不要打勾保留——历史是 git log 的责任
- 字数控制：常态 80-150 行，超 200 行说明信息没有筛过

### 2.3 docs/harness/ —— 中期记忆 + 约束

护城河文讲的 5 种知识类型（model / decision / guideline / pitfall / process）里，harness/ 主要存的是 **pitfall + guideline(avoid)**——即"已经踩过的坑，不要再踩"。

**和 wiki / 文档的本质区别**：harness 条目必须可执行：

| 维度 | 普通文档 | Harness 条目 |
|---|---|---|
| 形态 | 散文 | 结构化（触发场景 / Required / Forbidden / Verification / Recurrence Log）|
| 检测 | 靠人记得读 | grep / CI gate / 单测自动卡 |
| 命中证据 | 无 | §5 Recurrence Log 累加 |
| 退役 | 永远不删 | 30 天零复现 + grep 0 命中 → merged 进 rules/ |

im 项目当前 9 条 active：

| 编号 | 锁定的事实 | 检测手段 |
|---|---|---|
| C001 | 消息写入唯一入口 `repo.MessageRepo.AllocSeqAndInsert` | grep INSERT messages |
| C002 | 跨 pod 推送必须走 `gateway.CrossPodPush` | grep hub.PushToUser 外部调用 |
| C003 | Pulsar topic 必须经 `PushTopicFor` + 本地后缀 | grep 裸 topic string |
| C004 | Redis routing TTL 45s 与心跳 15s × 3 耦合 | grep 两个常量改一边必同时改 |
| C005 | WSMessageType 锁定 22 种 | enum 测试 + 路由表 diff |
| C006 | httpexpect 路径禁拼 `?q=` | grep `\.GET\(.*\?` |
| C007 | 全局 envelope 中间件唯一 wrap | grep handler 内 `"status":"success"` |
| C008 | 84 路由 + 22 WS type 必有 TestM4* 集成测试 | `scripts/check-handler-coverage.sh` |
| C009 | 加 / 改 middleware 必须 sweep test helper | grep 中间件改动 + helper 命中检查 |

**每一条都是真实踩过的坑**（看 §5 Recurrence Log），不是"我觉得这样写更好"——这是 harness 和 style guide 的根本分野。

### 2.4 /workspace/java/logs/{date}.json —— 流水追踪

护城河文没有强调这个层——但这是**让 harness 能自我演化**的底层燃料，对应「铁律 ①」。

每次 UserPromptSubmit 都通过 `~/.claude/settings.json` 的 hook 落一行 jsonl：

```jsonl
{"ts":"2026-05-12T09:00:01+08:00","cwd":"/Users/.../im","session_id":"...","prompt":"修一下那个 401"}
{"ts":"2026-05-12T09:05:33+08:00","cwd":"/Users/.../im","session_id":"...","prompt":"WS 又断开了 重连不上"}
```

**作用**：
- 周回看的时候 `tail -n 1000 logs/*.json | grep -i "401\|重连"` 直接看出**哪些问题在反复出现**
- ≥ 3 次复现 = 触发条件 → **即时**沉淀成 harness（不是"以后"）
- ≥ 2 个项目复现 = 触发条件 → 升级到跨项目 rules/

**即时性约束**：当 Claude 在当前会话发现"我刚才已经在解释同一件事第 3 次了"，**立即停下当前任务**，先写 harness。理由——
- 当前上下文还在脑子里，写出来准确度最高
- 拖到下次会话 = 上下文丢失 = 内容失真 = 又一条没人读的废文
- 用户体感上"打断"是 5 分钟成本，但收益是后续所有会话不再重复同一根因

---

## 3. Harness 三支柱在 im 项目的具体落地

护城河文的三支柱：上下文工程 / 架构约束 / 持续治理。每一支柱在 im 项目都有**具体可指**的实现。

### 3.1 支柱 1 — 上下文工程（让模型每次都站在正确的起点）

**问题**：每次新会话开始，Claude 默认上下文 = 系统 prompt + CLAUDE.md。如果项目状态全靠模型自己 grep 出来，就会出现"上次决策被忘记 / 上下文重新拼一遍"。

**落地手段**：

```
新会话开局自动加载顺序（CLAUDE.md §5 规定）:
  1. ~/.claude/CLAUDE.md            (5 条全局铁律)
  2. <project>/CLAUDE.md             (项目级指令 + harness 索引)
  3. docs/GOAL.md                   (L2 目标 + 硬约束)
  4. SESSION.md                     (L0/L1 状态)
  5. docs/ARCHITECTURE.md           (文件地图，按需查不是预读)
  6. docs/harness/README.md         (active 条目索引)
  7. ~/.claude/projects/.../memory/MEMORY.md  (auto-memory 索引)
```

**关键设计**：
- 步骤 1-4 一上来就读全文（一次性 ~300 行成本）
- 步骤 5-7 只读索引，按需深读（避免上下文膨胀）—— 这就是护城河文 §7.2 的「三级渐进式索引」

**反模式**：
- ❌ 把所有 harness 条目内容塞进 CLAUDE.md → 上下文膨胀，模型反而抓不住重点
- ❌ 把 SESSION.md 写成日记 → 历史信息越积越多，"当前"被淹没
- ❌ 让 agent 自己 grep 项目状态 → 每次启动都从零拼上下文

### 3.2 支柱 2 — 架构约束（让错误根本写不出来）

护城河文讲了"安全边界"，但没讲怎么实现。im 的实战是 **三层卡点**：

```
Layer 1: 文档约束（harness/ §3 Required + §3 Forbidden）
            ↓ 模型/人没看到 → 还是可能写错
Layer 2: 静态卡点（grep gate / CI script / golangci-lint）
            ↓ 跑 verify-build 就 fail
Layer 3: 运行时卡点（中间件 / 唯一入口函数 / 类型系统）
            ↓ 编译就过不去 / 跑起来就拒绝
```

**im 的真实案例**——C008 handler-coverage gate 的三层：

| 层 | 实现 | 命中阻断点 |
|---|---|---|
| 1 文档 | `docs/harness/C008-handler-coverage-gate.md` §3 | 写代码前 |
| 2 静态 | `server/scripts/check-handler-coverage.sh` grep 路由数 ≥ 84、TestM4* 数 ≥ 190 | `make verify-integration` 前 |
| 3 集成 | `server/tests/integration/v5_*.go` 198 个集成测试 | CI 跑 45min 全套 |

**最强的约束在 Layer 3**：例如 C001 把消息写入做成**唯一函数** `AllocSeqAndInsert`，根本不存在另一条 INSERT 路径——这种"用代码结构本身阻止错误"的方式，比任何文档都强。

**反模式**：
- ❌ 只有文档没有 grep gate → 模型/人不读文档时直接破防
- ❌ grep gate 频繁误报 → 工程师学会忽略 → 等于没有
- ❌ Layer 3 卡点写成 ifelse 校验 → 校验代码本身可能有 bug，不如做成唯一入口

### 3.3 支柱 3 — 持续治理（让 harness 库自己进化，不要变成博物馆）

最容易死掉的就是这一支柱。很多团队的"知识库"：建得很认真 → 半年后没人更新 → 一年后过时 → 两年后废弃。

**im 的 4 阶段生命周期**（`docs/harness/README.md` §3 已落地）：

```
drafting  ─新建未实战命中
    │  rec ≥ 1 + grep gate 接管
    ▼
active    ─在册执行中
    │
    ├──── 30 天零复现 + grep gate 稳定 + 已 inline 进 rules/
    │     └─→ merged    （终态，保留作历史）
    │
    └──── 场景不存在（模块删 / 协议换）
          └─→ deprecated （终态，frontmatter status=deprecated，文件保留）
```

**晋升强制 checklist**（不通过不准升级）：

```
active → merged:
[ ] §4 grep 命令存在
[ ] CI gate 中 `grep ... | wc -l == 0` 才放行
[ ] §5 复现日志 ≥ 1 条且最后一条 ≥ 30 天前
[ ] 规则原文已 inline 进 ~/.claude/rules/{lang}/*.md
[ ] frontmatter status: merged + inline_target: 指向锚点
```

**为什么必须有 merged 状态**：让 harness/ 永远只装"近期还在 active 阻断的"——超过 30 天没人破防的契约，规则已经渗透进团队肌肉记忆 + 落到 rules/，就该退场，不要占用上下文窗口。

**为什么必须有 deprecated 不直接删**：将来另一个项目可能踩同样的坑，能从 git 历史里挖出来作为参考；也防止"被遗忘的约束被无意识打破"。

### 3.4 CLAUDE.md 索引同步 —— 铁律 ③ 的工程化

新增 / 退役任意 harness 都**必须同步**改 CLAUDE.md 索引。**没索引 = Claude 不会读**。

每次操作 harness 的标准动作（一次 commit 完成所有 3 步）：

```bash
# Step 1: 新建 / 修改 harness 文件
vi docs/harness/C010-xxx.md

# Step 2: 更新 harness/README.md 索引表（同一目录内）
vi docs/harness/README.md   # §1 当前在册表加一行

# Step 3: 更新项目根 CLAUDE.md §8.4 索引（让 Claude 启动加载）
vi CLAUDE.md                # §8.4 当前在册 harness 表加一行

# Step 4: 一次 commit
git add docs/harness/C010-xxx.md docs/harness/README.md CLAUDE.md
git commit -m "docs(harness): 新增 C010 xxx + 同步 README/CLAUDE.md 索引"
```

**pre-commit hook 检查**（推荐落到 `.git/hooks/pre-commit`）：

```bash
#!/usr/bin/env bash
# 防止 harness 加新条目但忘了改 CLAUDE.md
harness_count=$(ls docs/harness/C*.md 2>/dev/null | wc -l | xargs)
claude_idx_count=$(grep -cE '^\| C[0-9]+' CLAUDE.md 2>/dev/null || echo 0)

if [ "$harness_count" != "$claude_idx_count" ]; then
  echo "❌ harness 文件数 ($harness_count) ≠ CLAUDE.md §8.4 索引数 ($claude_idx_count)"
  echo "   修复：同步更新 CLAUDE.md §8.4 表"
  exit 1
fi
```

**索引表的最小字段**（CLAUDE.md §8.4 的样子，照抄即可）：

```markdown
| 编号 | 标题（一句话） | 状态 |
|---|---|---|
| C001 | repo.MessageRepo.AllocSeqAndInsert 是消息写入唯一入口 | active |
| C002 | 跨 pod 推送必须走 gateway.CrossPodPush / CrossPodBroadcast | active |
| ...  | ... | ... |
```

> **为什么 CLAUDE.md 只放索引表不放全文**：CLAUDE.md 每次会话都会被读，全文塞进去 → 上下文膨胀 → 模型抓不住重点。只索引到 `docs/harness/C{NNN}-*.md`，模型按需 `Read` 全文，这就是护城河文 §7.2 的「三级渐进式索引」在 Claude Code 里的落地形态。

---

## 4. Harness 条目的最佳实战格式（im TEMPLATE.md 实测版）

每条 harness 必须有这 7 节（缺一不可）：

```markdown
---
id: C0XX
title: <一句话说清不许做什么>
status: drafting | active | merged | deprecated
created: 2026-MM-DD
source_log: logs/2026-MM-DD.json#L<行号>
recurrence_count: <1 = 第一次踩，3+ = 跨项目候选>
inline_target: <merged 后指向 ~/.claude/rules/...>
---

## §1 触发场景
什么情况下这条契约要被加载？
— 给模型/人一个 grep 友好的关键词列表

## §2 背景（why）
为什么这条契约存在？踩了什么坑？
— 必须有具体 commit / PR / 故障时间戳，不许写"为了代码质量"

## §3 Required / Forbidden
✅ 必须这么做：<具体代码片段或调用规范>
❌ 不许这么做：<反例代码片段>
— 给模型一个可以照抄的正反对照

## §4 Verification
- grep 命令：`grep -rn "pattern" server/ | grep -v _test.go | wc -l` 应为 0
- CI gate：<哪个 make target / 哪个 script 卡这条>
- 单测：<哪个 _test.go 覆盖了 happy path 和 抗回归>

## §5 Recurrence Log
| 日期 | session | commit | 现象 | 根因 |
|---|---|---|---|---|
| 2026-MM-DD | <id> | <sha> | <一句话> | <一句话> |
— 每次再次踩这个坑就加一行，不要合并

## §6 关联
- 上游 spec: docs/GOAL.md §X / docs/M{N}_SPEC.md §决策点 Y
- 兄弟 harness: C0NN, C0MM
- 下游消费者: <哪些 handler / Skill 用到这条>

## §7 历史与演进
- drafting → active: <日期 + 第一次命中 commit>
- 调整: <什么时候改了什么>
- (merged 时填) merged → rules/: <inline 锚点>
```

**字段顺序不能乱**：模型按位置定位会比按标题快，标准化排版让多条 harness 之间可以快速对照。

---

## 5. 沉淀触发器与反触发器 —— 什么时候新建，什么时候**不**新建

护城河文讲了"知识沉淀"，但少讲了**反向**——哪些东西**不要**沉淀。在 im 项目 CLAUDE.md §8.2 已经写成硬规则。

### 5.1 必须沉淀的 5 种触发器（满足任一）

```
✅ ≥ 3 次复现           — 同一根因在 logs/{date}.json 出现 ≥ 3 次
✅ 用户明确说要沉淀     — "别再让我说第二遍 / 写到 harness 里"
✅ Spec/RFC 拍板的硬约束 — GOAL.md §4 / M{N}_SPEC.md §决策点
✅ 跨项目重复           — ≥ 2 个项目 logs/ 出现同类，候选 rules/ inline
✅ CI gate 已能机械检测但还没文档 — 反向沉淀（grep 已就位 → 文档化 why）
```

### 5.2 严禁沉淀的 3 种反触发器

```
❌ 单次踩坑 + 非用户/Spec 拍板     — 写 SESSION.md "已知债务"即可
❌ 纯风格偏好                       — 走 coding-style.md
❌ 临时 workaround                  — commit message + 代码注释即可
```

**为什么这么严**：harness/ 一旦膨胀就会失效。9 条 active 的边际成本人脑能 hold 住；30 条以后就开始有人略读、漏读、误读；50 条以后就和"没有"等价。

**经验值**：单个项目 active harness 数量稳态期应该在 **10-20 条**之间。低于 5 说明体系还没起来；高于 30 说明该做 merged → inline rules/ 的清理。

---

## 6. 五层知识架构在 im 项目的当前位置

护城河文 §4.2 的五层架构很美，但落地需要务实——下面这张表说清楚 im 项目**今天**实际用到了哪几层、什么时候考虑往上扩。

| 层 | 护城河文定义 | im 项目当前实现 | 何时升级 |
|---|---|---|---|
| **Layer 0-P** 个人偏好 | `~/.ai-team/` | `~/.claude/CLAUDE.md` 5 条铁律 + `~/.claude/rules/common/` | 已落地 |
| **Layer 0-T** 团队约定 | `team-conventions/` | （单人 / 还没团队层）| 团队 ≥ 2 人时建独立 git repo |
| **Layer 1** 技术知识 | `tech-wiki/` | `~/.claude/rules/{golang,typescript}/` + 多个 `~/.claude/skills/*-knowledge/` | 已部分落地 |
| **Layer 2** 业务知识 | `biz-wiki/{domain}/` | `docs/CSES_CLIENT_内部对接契约.md` + `docs/IM_DATA_MODEL_新版数据模型字典.md` | 已落地（IM 业务域） |
| **Layer 3** 项目知识 | `docs/knowledge/` | `docs/GOAL.md` + `docs/ARCHITECTURE.md` + `docs/harness/` + `SESSION.md` | 已落地 |

**当前形态的本质**：im 是**单项目重投入**模式——Layer 3 极度密集（9 条 harness + 4 件套），Layer 1/2 是机会主义沉淀（碰到才写）。

**升级方向**（按价值排序）：

1. **Layer 3 → Layer 1 提升机制**：当某条 harness 在 ≥ 2 个项目出现，自动提名升级到 `~/.claude/rules/{lang}/`。当前是手动 review，可以做成脚本扫各项目 harness/log.md 找重复主题。
2. **Layer 0-T 团队约定**：im 后续做团队协作时，把 `commit message 中文 + 模块化 tag 策略` 这种约定从 `~/.claude/rules/common/git-workflow.md` 抽出来做独立 team repo。
3. **Layer 2 业务知识联邦**：cses 那侧 Java 后端的业务知识 + im 这侧 Go 后端的业务知识，目前各跑各的——下一步需要一个 `biz-wiki/im-cses-domain/` 把"双栈契约"统一沉淀。

---

## 7. 长运行 / 跨会话续接 —— SESSION 协议的最佳实战

护城河文 §8 讲了跨设备接管，但没讲**跨会话续接**这个更日常的问题：同一台机器，今天 Claude 跑到一半，明天打开新会话怎么无缝接上。

### 7.1 会话开局 30 秒自检（CLAUDE.md §5 规定）

```bash
# 1. 我在哪
git status && git log --oneline -5

# 2. 上次会话留了什么
cat SESSION.md | head -80
ls docs/harness/  # 看看有没有新 active 条目

# 3. 今天日志（看自己今天问过什么）
tail -n 50 /workspace/java/logs/$(date +%Y-%m-%d).json

# 4. 找对应小节
ls docs/ server/docs/
```

完成这 4 步后，Claude 才允许开始"动手"——前面任何动作（包括写代码、改文档、跑测试）都是**未对齐**状态。

### 7.2 会话收尾 5 项必做（CLAUDE.md §6 规定）

```
[ ] 所有变更 committed（或明确说为什么没 commit）
[ ] SESSION.md §1 §2 §3 更新到位
[ ] 新决策 / 硬约束追加到 GOAL.md §4 或 MEMORY 索引
[ ] 30min+ 耗时任务做 retro-30min 复盘并写 logs/{date}.json
[ ] 新踩的坑（≥ 3 次复现 / 用户明确 / Spec 拍板）按 §8 流程沉淀进 docs/harness/
```

**关键设计：开局/收尾是非对称的**。
- 开局**只读**，不写——保证模型 / 人在动手前充分了解状态
- 收尾**只写**，不思考新方案——把当前会话的产物落到正确的文件层，不要做新决策

如果发现收尾时还需要"想新决策"，说明这个会话其实没结束，应该继续做完再收尾。

### 7.3 一句话启动模式（SESSION.md §0）

每次会话结束时，SESSION.md §0 必须保留一段"复制粘贴就能续上"的话，例如：

> im 后端 production-ready，开始对接 cses-client。HEAD `<commit>`，v0.7.3-backend-final tag pushed origin。两份对接契约已就位：`docs/CSES_CLIENT_内部对接契约.md` + `docs/IM_DATA_MODEL_新版数据模型字典.md`。cses-client 工作目录锁定 `/Users/mac28/workspace/angular/cses-client` branch `im-backend-switch` HEAD `7c8a0c972`，按内部对接契约 §12.16 10 步推进 Phase 2-4。

**为什么是"一句话"**：把 SESSION.md 浓缩到一段话作为"启动入口"，新会话可以直接复制粘贴到 prompt，而不必等模型自己读完 200 行 SESSION.md。

---

## 8. 闭环验证 —— 让 Harness 自证还活着

没有验证的 harness 几个月后必然失效。im 项目的**自证机制**有 5 条：

```
1. grep gate
   harness §4 给的 grep 命令 → 跑一遍应为 0 命中
   失败：说明真的还有破防 → 找到 commit 修 + 加 recurrence log
   通过：说明文档约束有效

2. CI gate
   make verify-build / verify-unit / verify-integration
   失败：说明 Layer 2 静态卡点抓到回归
   通过：harness §3 Forbidden 在主干没被破

3. 单测覆盖
   harness §4 列的单测必须存在且绿
   失败：说明 happy path 都已经不能复现 → 这条 harness 可能已经过时

4. Recurrence Log
   §5 表必须有 ≥ 1 条历史命中（drafting 例外）
   空的：说明这条本质上是"猜的"——不是真踩过的坑，应该降回 drafting 或删

5. 30 天回看
   tail -n 5000 /workspace/java/logs/*.json | grep -i "<harness 触发关键词>"
   有命中 → active 续命 / 升级 recurrence_count
   零命中超过 30 天 → 走 merged checklist
```

**这 5 条每月跑一次**。Sunday morning brief 里加一步「Harness 自检周报」就够了。

---

## 9. 反模式 —— 8 种假 Harness

按"看起来像但其实不是"的伪装程度排序：

| 反模式 | 看起来像 Harness 因为 | 但其实是 | 怎么改 |
|---|---|---|---|
| 1. "我们要遵守 Clean Code" | 有文档 | 风格指南 | 走 coding-style.md |
| 2. "禁止使用 any 类型" | 有 forbidden 字段 | lint 规则 | 走 eslint/tsconfig |
| 3. "每个 PR 必须有测试" | 有 required 字段 | 流程规则 | 走 CONTRIBUTING.md |
| 4. "数据库表必须有 created_at" | 有约束 | schema 规约 | 走 migration template |
| 5. "已知 bug 列表" | 长得像 §5 Recurrence Log | bug tracker | 用 Linear / GitHub issue |
| 6. "曾经讨论过用 Kafka 还是 Pulsar" | 像 decision | ADR | 走 docs/adr/ |
| 7. "新人入职必读" | 有 required | onboarding | 走 docs/onboarding.md |
| 8. "我觉得这样写更好" | 有 polarity | 个人风格 | 自己 ~/.claude/CLAUDE.md |

**核心判据**：能不能写出**具体可执行的 §4 Verification**？写不出 → 不是 harness。

---

## 10. Anti-Pattern 收尾：避免 Harness 工程本身的退化

护城河文最后一节讲了展望，但少讲了**警示**。Harness 工程本身也会退化，常见 5 种死法：

### 10.1 死法 1 — 通货膨胀
**症状**：harness 条目数量月度 +5+ → 没人读 → 等于没有
**根因**：触发条件太宽 / 反触发器没生效
**急救**：执行 §5.2 强删反触发器命中的；执行 §3.3 merged 通道把 30 天零命中的退役

### 10.2 死法 2 — 失真
**症状**：harness 写得很美，但 grep gate 不存在 / CI gate 已删除
**根因**：写 harness 的人和维护 CI 的人脱节
**急救**：每月跑一次「§8.5 30 天回看」，没 grep 没 gate 的全部降回 drafting

### 10.3 死法 3 — 政治化
**症状**：harness 变成 "我说了算" 的工具，缺乏踩坑证据
**根因**：§5 Recurrence Log 没强制
**急救**：空 Recurrence Log + 非 Spec 拍板的全部强删，不留情面

### 10.4 死法 4 — 历史沉淀过载
**症状**：harness/ 目录 200 个文件，新人不知道从哪读
**根因**：没有 merged / deprecated 通道
**急救**：执行 §3.3 4 阶段生命周期，把超过 6 个月 active 的全部跑 merged checklist

### 10.5 死法 5 — 与代码脱节
**症状**：harness 说"必须用 X 函数"，但代码里 X 函数已经重命名为 Y
**根因**：grep gate 没跟着代码改
**急救**：每次 rename / refactor 影响 harness 触及的符号，必须同步改 harness §4 的 grep 命令；用 GitNexus blast radius 提前发现

---

## 11. 速查卡（一页打印）

```text
★ 三个即时同步铁律（最高优先级）★
  ① 踩坑 → 即时写 /workspace/java/logs/{date}.json
  ② 同根因≥3 / 用户明确 / Spec 拍板 → 即时新建 docs/harness/C{NNN}-*.md
  ③ harness/ 增删 → 即时改 CLAUDE.md §X.4 索引表 + 一次 commit

  断链后果：①断→忘事 ②断→重复踩坑 ③断→Claude 读不到等于没写

长运行项目的目标分层
  L0 单步（5-30min）  → TaskCreate todo + commit
  L1 阶段（1-7d）     → git tag + Phase plan doc
  L2 项目（1-6m）     → docs/GOAL.md + ARCHITECTURE.md

四件套
  GOAL.md       L2 常量 + 硬约束 + 反模式
  SESSION.md    L0/L1 状态 + 一句话启动 + 待决分叉
  harness/      active 契约（grep/CI/单测自证）+ CLAUDE.md 索引
  logs/         /workspace/java/logs/{date}.json 流水

Harness 沉淀触发器（任一）
  ≥ 3 次复现 / 用户明确 / Spec 拍板 / 跨项目 ≥ 2 / CI gate 已就位无文档

Harness 反触发器（命中则不沉淀）
  单次踩坑 / 风格偏好 / 临时 workaround

Harness 7 节模板
  §1 触发场景  §2 背景 why  §3 Required/Forbidden
  §4 Verification (grep + CI + 单测)
  §5 Recurrence Log (≥ 1 条真实命中)
  §6 关联  §7 历史与演进

生命周期
  drafting → active → merged (30天零命中 + inline rules/)
                    → deprecated (场景消失)

会话开局 4 步     git status / SESSION.md / logs tail / harness/ ls
会话收尾 5 必做   commit / SESSION 更新 / GOAL 追加 / retro / harness 沉淀

8 种假 Harness    Clean Code / lint 规则 / 流程规则 / schema 规约 /
                  bug list / ADR / onboarding / 个人风格

5 种死法警示      通胀 / 失真 / 政治化 / 沉淀过载 / 与代码脱节

要接入自己的项目？→ §12.1 在 CLAUDE.md 加 ~10 行索引指针（不粘整套），按 §12.4 第一周计划训练 Claude 主动调用
```

---

## 12. 接入你的项目 —— 渐进式披露，**不**拷贝整套模板

> **核心理念**：每个项目的 `CLAUDE.md` 有自己的架构 / 业务 / 代码规范约束，本文整段塞进去会污染那些约束、撑爆上下文。正确做法是 **CLAUDE.md 只放索引指针**，Claude 在触发条件命中时主动来读本文获取「长运行任务约束」能力。
>
> 三层渐进披露对应关系：
>
> | 层 | 文件 | 行数级别 | 加载时机 |
> |---|---|---|---|
> | A 索引 | 你项目的 `CLAUDE.md` 加一段 ~10 行 | 每次会话开局必读 | 永远在上下文 |
> | B 目录 | `docs/harness/README.md` | ~100-200 行 | 触发条件命中时读 |
> | C 全文 | `docs/tx/harness-Engineer最佳实战.md`（本文） + 各 `C{NNN}-*.md` | ~50-800 行 | 按需深读 |

### 12.1 在 CLAUDE.md 加一段索引指针（双向工作流）

**这一段索引可以由两种方式落到 CLAUDE.md**：
- **人工方式**：项目维护者按下方模板手动追加到 `CLAUDE.md` 的合适位置。
- **AI 自助方式**：Claude / Agent 按 🤖 §协议 P5 检测到缺失后，征求用户同意后自助追加。

模板正文（~12 行）—— 改一处占位符 `<你的日志路径>` 即可：

```markdown
## 长运行任务约束 / Harness Engineering

> 本项目长运行任务（跨会话、跨 Phase、跨多日、autonomous 模式）的工程化约束体系。
> 完整方法论见 [`docs/tx/harness-Engineer最佳实战.md`](docs/tx/harness-Engineer最佳实战.md)
> （**不要把全文拷进本 CLAUDE.md**——按下方触发条件**按需** Read 即可）。

**三个即时同步铁律**（记住这三条就够开局用）：
1. 踩坑 → 即时写 `<你的日志路径>/{YYYY-MM-DD}.json`
2. 同根因 ≥ 3 次 / 用户说要沉淀 / Spec 拍板 → 即时新建 `docs/harness/C{NNN}-*.md`
3. harness/ 增删 → 即时同步本 CLAUDE.md 「§在册 harness」表 + 一次 commit

**Claude / Agent 必须 Read 完整实战手册的触发条件**（命中任一就停下读完再继续）：
- 普通任务收尾前 → 至少读 §0 三铁律 + 实战手册 🤖 §协议 P3 P4
- 长运行任务 / autonomous 任务开始前 → 读 🤖 §协议 P1-P5（全部）
- 会话开局 / 收尾流程不清楚 → 读 §7
- 第一次准备新建 harness → 读 §4（模板）+ §5（触发器）
- 准备把 active 升级 merged / 退役 deprecated → 读 §3.3 §3.4
- 用户问"怎么不再踩同一个坑" → 读 §0 + §8 闭环验证

**在册 harness 索引** → 见 [`docs/harness/README.md`](docs/harness/README.md)
（Claude 启动时 `ls docs/harness/` 即可，不必把每条名字写进本 CLAUDE.md）
```

**就这些。** 整段 ≤ 25 行，对 CLAUDE.md 主体上下文的侵占可以忽略。

**AI 自助安装时的额外要求**（来自协议 P5）：
- 必须先征求用户同意，不要静默修改 CLAUDE.md。
- 找合适位置（项目目标声明之后、详细约束之前）插入；已有同标题段则**改写**而非追加。
- commit 信息建议：`docs(claude): 加 Harness Engineer 实战索引指针，启用长运行任务约束`。
- 同会话内不重复触发 P5。

### 12.2 为什么不放完整模板进 CLAUDE.md

| 反模式 | 后果 |
|---|---|
| 把本文 800 行整段拷进 CLAUDE.md | 上下文膨胀 → 模型在项目自身约束（架构 / 业务 / 代码规范）和 Harness 元规则之间分不清主次 |
| 把 §X.1-§X.7 子表全列在 CLAUDE.md | 项目级 CLAUDE.md 失去自身可读性 → 工程师 review CLAUDE.md 时找不到本项目特有约束 |
| 在每个项目都复制一份本文 | 文档 drift → 一处更新其他全部过时 → 等于没有维护 |

**正确姿势**：本文档**只有一份权威源**（可以是本仓库，也可以是团队共享仓库），各项目 CLAUDE.md 通过相对路径 / 跨仓引用指过来。本文档自己演进，所有项目自动跟进——这就是"知识 = 护城河"在文档层面的具体落地。

### 12.3 配套要建的文件（一次性，按项目实际需要选）

仅当项目准备真正用这套体系时，按需建：

| 文件 | 何时建 | 内容来源 |
|---|---|---|
| `docs/harness/` 目录 | 准备落第一条 harness 时 | 参考本仓库 |
| `docs/harness/TEMPLATE.md` | 同上 | 复制本仓库或本文 §4 模板 |
| `docs/harness/README.md` | 同上，§1 在册表初始为空 | 参考本仓库 |
| `docs/harness/log.md` | 同上 | 空文件，后续 append |
| `docs/GOAL.md` / `SESSION.md` / `docs/ARCHITECTURE.md` | 项目跨多日时建 | 见 §2 四件套定义 |
| `.git/hooks/pre-commit` 检查脚本 | active harness ≥ 3 条时建 | 见 §3.4 脚本 |
| 项目级 `docs/tx/harness-Engineer最佳实战.md` 副本 | **不建议**，引用上游单一源 | 改用相对路径 / 共享仓库引用 |

### 12.4 第一周冷启动（zero → working）

| 第几天 | 动作 | 验证 |
|---|---|---|
| Day 1 | CLAUDE.md 加 §12.1 索引指针（~10 行）| `grep '长运行任务约束' CLAUDE.md` 有命中 |
| Day 2 | 用户给 Claude 一个真实任务，让它**读本文 §7 会话开局/收尾** | Claude 输出体现出 §7 的 4 步开局 |
| Day 3-4 | 正常做项目，第一次踩坑被用户指出 / 第二次同类 → 让 Claude 读本文 §4 + §5 → 新建 C001 | `ls docs/harness/C001-*.md` 存在 |
| Day 5 | 新建 C001 时让 Claude 同步改 CLAUDE.md §在册 harness 表 | `git commit` 包含 3 处改动 |
| Day 7 | 周日复盘：让 Claude 读本文 §8 闭环验证 5 条，对 C001 跑一遍 | 至少 1 条 grep / CI 命令能跑 |

**关键**：前 7 天最容易"忘了用"。靠**用户在 prompt 里反复提醒** "看一下 `docs/tx/harness-Engineer最佳实战.md` 怎么处理这个" 来训练 Claude 形成主动调用习惯。一旦三铁律的 3 个回路在项目里跑通一轮，后续就是自我维持的。

### 12.5 项目级 / 全局级 / 个人级三层定位

| 范围 | 放哪 | 何时用 |
|---|---|---|
| **项目级**（每个项目独立 harness） | `<repo>/docs/harness/` + `<repo>/CLAUDE.md` 索引指针 | **推荐起点** |
| **全局级**（跨项目通用规则） | `~/.claude/rules/{lang}/` + `~/.claude/CLAUDE.md` | 同类坑出现在 ≥ 2 个项目时升级 |
| **个人级**（习惯 / 偏好） | `~/.claude/CLAUDE.md` 元铁律（30min 复盘 / 统一日志 / 早安复盘）| 跨项目元规则 |
| **方法论**（本文）| 单一权威源（团队 / 个人公共仓库）| 各项目 CLAUDE.md 通过路径引用 |

**反模式**：把方法论本文也复制到 `~/.claude/CLAUDE.md` —— 全局 CLAUDE.md 也会膨胀，且其他不需要 Harness 体系的小项目会被强行加载。**方法论永远是 Layer C（按需读），不进 Layer A（开局必读）**。

---

## 13. 演进路线 —— im 项目下一步的 Harness 工程

按当前 9 条 active + SESSION 状态判断，未来 1-3 个月的演进重点：

| 优先级 | 演进项 | 价值 |
|---|---|---|
| P0 | **pre-commit hook 强制 CLAUDE.md ↔ harness 索引一致**（§3.4 脚本）| 让"铁律 ③"机器可强制，不靠人记得 |
| P0 | **即时沉淀守门 hook**：当 logs/{date}.json 出现同 root cause ≥ 3 次时，Stop hook 弹提示 "建议沉淀为 harness" | 让"铁律 ②"半自动化，不靠人察觉 |
| P0 | **Harness 自检周报自动化**：周日 morning-brief 加一段，扫所有 active 跑 §4 grep + §5 复现日志 + 30 天回看，产出退役 / 升级建议 | 防止"失真"+"沉淀过载" |
| P1 | **跨项目重复检测**：扫多个项目的 `docs/harness/log.md`，找重复关键词 → 候选升级到 `~/.claude/rules/{lang}/` | Layer 3 → Layer 1 自动化 |
| P1 | **Harness 引用追踪**：每次 commit 在 message 里带 `harness: C001,C008`，自动统计每条 harness 的真实引用频率 | 替代靠 grep 估算引用 |
| P1 | **Spec 锚点链接**：harness §6 关联里链 GOAL.md / M_SPEC.md 具体行号，反向能从 Spec 找到所有衍生 harness | 双向 trace L2 ↔ L1 |
| P2 | **Harness 语义检索**：超过 20 条 active 后，按 §1 触发场景做向量索引，模型用自然语言定位相关 harness | 解决 grep 命中失败的"我不知道有这条 harness"问题 |

---

## 14. 结语 —— 给"别人的 CLAUDE.md"

护城河文的最后一句话很对：

> Skill、Agent、工具链会随模型迭代更新，但领域知识是永恒的。

但落到工程上要补一句：

> **知识的永恒性 ≠ 文档的永恒性。是 grep / CI / 单测 / 复现日志这些"可执行的承诺"在让知识不腐烂。**

Harness Engineer 的工作不是写文档，而是把"我们决定不再踩这个坑"这件事，编译成代码、grep、CI、单测都跑得起来的**可执行契约**——并且配上让契约自身能晋升、能退役、能自证的全生命周期机制。

im 项目的 9 条 active harness + GOAL/SESSION/harness/logs 四件套，是这套体系的一个最小可工作样本。它没什么神秘的，全部是 Markdown + shell script + grep + 几个 Make target。但跑了三个月之后，团队（即便只有 1 个人 + 1 个 Claude）的输出质量、跨会话续接成本、跨 Phase 推进速度，是肉眼可见地稳定下来了——同一个坑不再踩第二次，同一个决策不再推导第二次，同一份上下文不再重新拼第二次。

这就是「长运行 / 目标驱动」的 Harness Engineer 所追求的：**让组合体的输出，跨时间单调不退化。**

---

### 14.1 如果你是人（项目工程师 / 团队 lead）

**最快的上手路径**：

1. **先读 §0 "三个即时同步铁律"**——5 分钟看完，这是整个体系的灵魂。
2. **按 §12.1 在你项目的 `CLAUDE.md` 加一段 ~10 行索引指针**（**不**粘整套）——目的只是让 Claude 在触发条件命中时知道有这份文档可读。**或者更省事：让 Claude 自己读完本文按协议 P5 来问你装不装。**
3. **按 §12.3 仅在项目准备真正用时**才建 `docs/harness/` 目录和四件套文件——不要一上来 over-engineer。
4. **按 §12.4 第一周计划**让 Claude 在真实任务里**主动调用**本文——前 7 天用户需要不断在 prompt 里提醒 "看一下 `docs/tx/harness-Engineer最佳实战.md` 怎么处理这个"，直到 Claude 形成习惯。

你不需要从第一天就有 9 条 active harness。你需要的是**从第一次踩坑开始就把它写下来**——铁律 ①。然后等同根因 ≥ 3 次时把它升级——铁律 ②。然后把升级动作和 CLAUDE.md 索引同步——铁律 ③。

剩下的一切（生命周期、闭环验证、反模式、死法警示）都是这三个回路的延伸。**先把三个回路打通，再读其他章节。**

### 14.2 如果你是 AI（Claude / Agent / 子代理）

你读到这里时**已经处于本协议的活动范围之内**——本文不是参考资料，是你接下来在这个项目里的**强制执行规约**。

无论你被触发的方式是：
- 用户的一句话提问（普通任务）
- 多步骤的需求实现（长运行目标任务）
- `/loop` / cron / 长链子代理（autonomous 任务）

你都必须按 🤖 §「给 AI 读者的执行协议」P1-P6 跑一遍：

```
P1 自检任务模式 → P2 开局 4 步 → P3 每步铁律自检 → P4 收尾 5 必做 → P5 索引自助安装（若缺）→ P6 按需深读
```

**普通任务**不能省略 P3 P4：哪怕只改一行代码，也要在 commit 前过一遍三铁律自检——这是区分"完成"和"草草交付"的唯一边界。

**Autonomous 任务**必须把 P3 内化成每步的硬前置条件：不是事后回想"哦这跟之前那次一样"，而是动手前查 `logs/` 历史防止重蹈覆辙。

**当你发现项目 CLAUDE.md 还没指向本文档**：按 P5 征求用户同意自助安装索引——这是你回报本文档的方式，让下一次进来的 Claude / Agent 也能站在你今天的起点上。

### 14.3 渐进式披露的本质

本文档**不抢占** CLAUDE.md 的位置，**不替代**项目自己的约束。它只是一份**按需被读**的方法论手册——读它的次数越多，你的 Claude / Agent 在长运行 / autonomous 任务上的能力就越接近本文描述的"组合体单调不退化"。

CLAUDE.md = Layer A（每次启动必读，~10 行指针）
docs/harness/README.md = Layer B（active 索引，~150 行）
本文 + 各 C{NNN} 条目 = Layer C（按 P6 触发表深读）

这三层就是 Karpathy LLM Wiki Pattern + 护城河文 §7.2 三级索引在 Claude Code 上的工程落地。

---

> Harness 不是文档堆出来的，是回路跑出来的。
> CLAUDE.md 不是知识库，是索引；方法论永远住在 Layer C，按需调用。
> 给人的是建议，给 AI 的是协议——一旦读到，下一步就开始执行。
