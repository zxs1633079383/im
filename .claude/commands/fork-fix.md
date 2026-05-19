---
description: 自动化排障流水线 — fork worktree+agent+cron 调查并修复 bug；默认 cross-repo (golangProject/im + cses-client) 双仓并行；内置 C012 cwd/branch guard
argumentHint: <bug 描述> [--server-only | --client-only] [--baseline <sha>] [--budget <min>]
---

User bug: $ARGUMENTS

你（主对话）被 `/fork-fix` 调用（im server 仓视角）。立即按流水线执行。

# Pipeline

## Stage 1: 解析 + scope detection

flags:
- `--server-only` → 只派 server agent
- `--client-only` → 只派 client agent
- `--baseline <sha>` / `--budget <min>` 默认 40

默认 dual（server + client）。

bug 关键词 → hypothesis：

| 关键词 | scope hint |
|---|---|
| PG / SQL / migration / Ent | server: server/migrations / store / repo |
| Pulsar / topic / consumer | server: pulsar consumer / producer |
| Redis / cache | server: redisstore / cache layer |
| WebSocket / hub / fan-out / 群广播 | server: gateway/hub / cross-pod routing |
| router / endpoint / 404 / handler | server: cmd/server / api router |
| 头像 / picture / 渲染 / 列表 | client-leaning: cses-client/src-tauri/src/features/im/** + src/pages/message-v3/** |
| 同步 / 重连 / mediator / dispatch | client-leaning: cses-client sync_engine + mediator |

提取 phase slug (kebab-case)。

## Stage 2: Pre-flight

```bash
df -g ~ | tail -1 | awk '{print "Avail: " $4 " GB (≥ 15 dual / ≥ 10 single)"}' && \
git -C /Users/mac28/workspace/golangProject/im log -1 --oneline && \
git -C /Users/mac28/workspace/angular/temp/cses-client log -1 --oneline && \
git -C /Users/mac28/workspace/golangProject/im worktree list && \
git -C /Users/mac28/workspace/angular/temp/cses-client worktree list && \
grep -l "/worktrees/" /Users/mac28/workspace/golangProject/im/.gitignore /Users/mac28/workspace/angular/temp/cses-client/.gitignore 2>/dev/null
```

记录 SERVER_BASELINE / CLIENT_BASELINE。

## Stage 3: TaskCreate 4 tasks

setup / agent server / agent client / main 协调

## Stage 4: 创建 worktrees

```bash
SLUG="<phase-slug>"
git -C /Users/mac28/workspace/golangProject/im worktree add worktrees/$SLUG-server -b fix/im-$SLUG-server $SERVER_BASELINE
git -C /Users/mac28/workspace/angular/temp/cses-client worktree add worktrees/$SLUG-client -b fix/im-$SLUG-client $CLIENT_BASELINE
```

## Stage 5: 派 2 agent + 2 cron（4 tool calls 并行）

### Agent A — server (general-purpose, run_in_background: true)

```
你是 general-purpose autonomous worktree agent，负责 Go IM 服务端。并行于 agent B (cses-client)。

# ⚠️ C012 cwd / branch guard

pwd | grep -q "worktrees/<SLUG>-server" || { echo "❌ cwd 漂"; exit 1; }
[ "$(git rev-parse --abbrev-ref HEAD)" = "fix/im-<SLUG>-server" ] || exit 1

任一失败 → 停止 / 不 git checkout / 写 ISSUE.md。
任何 cd / go build / go test 后立即重跑。
REPORT_<SLUG>-SERVER.md 顶部声明 guard PASS。
每次 git commit 前重跑。

# 任务: <bug>

## 工作区
- cwd: /Users/mac28/workspace/golangProject/im/worktrees/<SLUG>-server
- branch: fix/im-<SLUG>-server
- baseline: <SERVER_BASELINE>

## 用户原话
> $ARGUMENTS

## scope detection hypothesis
<按 Stage 1 提取的 server-side hypothesis>

## 跨仓 read-only 协调
- 客户端代码 /Users/mac28/workspace/angular/temp/cses-client — 可 grep / Read，**不要改**
- 发现客户端 gap → 写 NEED_CLIENT_FIX_<id>.md 到自己 worktree 根

## 文件白名单（im 仓内）
<按 hypothesis 收敛；默认 internal/** + cmd/** + server/migrations/** + pkg/**>

## 文件黑名单
- /CLAUDE.md /SESSION.md /README.md /AGENTS.md
- docs/harness/* (im 仓 harness 不动)
- server/config.yaml（运行时配置不动）

## 编译纪律（im 仓 policy B — agent 必须自跑）
- commit 前: go build ./... && go vet ./...
- commit 前: go test -short ./internal/... -count=1
- 集成测试 (-tags=integration) 禁止中途跑，仅 phase 完成前最后一次
- 集成测试用 dev 共享 PG（config.yaml dsn）不要新起 testcontainers

## 输出 contract
- ≥ 1 commit on fix/im-<SLUG>-server（C012 强制，绝不直 commit 主 branch）
- 结构化 5 段 body（影响范围/影响面/功能/Harness/验证）
- REPORT_<SLUG>-SERVER.md: guard PASS / 根因 / 文件 / 改前改后 / build+vet+test / 黑名单 / NEED_CLIENT_FIX / ✅ DONE
- 时间预算 <BUDGET> min
```

### Agent B — client (cses-client-dev, run_in_background: true)

```
你是 cses-client-dev autonomous worktree agent，负责 cses-client。并行于 agent A (server)。

# ⚠️ C012 cwd / branch guard

pwd | grep -q "worktrees/<SLUG>-client" || exit 1
[ "$(git rev-parse --abbrev-ref HEAD)" = "fix/im-<SLUG>-client" ] || exit 1

REPORT_<SLUG>-CLIENT.md 顶部 guard PASS；每次 git commit 前重跑。

# 任务: <bug>

## 工作区
- cwd: /Users/mac28/workspace/angular/temp/cses-client/worktrees/<SLUG>-client
- branch: fix/im-<SLUG>-client
- baseline: <CLIENT_BASELINE>

## scope detection
<client-side hypothesis>

## 跨仓 read-only
- 服务端 /Users/mac28/workspace/golangProject/im — grep / Read 可，不改
- 发现服务端 gap → 写 NEED_SERVER_FIX_<id>.md

## 文件白名单（cses-client 仓内）
- src-tauri/src/features/im/** + src/pages/message-v3/** + src/app/core/im/** （按 hypothesis 收敛）

## 文件黑名单
- /CLAUDE.md /SESSION.md /README.md /AGENTS.md
- docs/harness/C001-CNNN.md
- types_v2.rs Channel struct

## 编译纪律（cses-client policy A — autonomous worktree agent 例外）
- 中途靠 type signature + grep
- commit 前 ≤ 1 次: cd src-tauri && cargo check --lib --no-default-features --features custom-protocol --message-format=short
- commit 前 ≤ 1 次: npx tsc --noEmit -p tsconfig.json 2>&1 | grep "error TS" | head -20

## 输出 contract
同 A，REPORT_<SLUG>-CLIENT.md
```

### Cron A (server agent monitor)

```
cron: 5,15,25,35,45,55 * * * *  (avoid :00 :30)
recurring: true, durable: false

prompt:
[CRON-MON-SERVER-<SLUG>] Check agent A (general-purpose, <bug>) — dispatched <ts>.
Worktree: /Users/mac28/workspace/golangProject/im/worktrees/<SLUG>-server
Branch: fix/im-<SLUG>-server / Baseline: <SERVER_BASELINE>

NO spawn agents / go build / cargo. Inspect:
  git -C <wt> log --since='12 minutes ago' --oneline
  git -C <wt> log <baseline>..HEAD --oneline
  git -C <wt> status --short
  ls <wt>/REPORT_<SLUG>-SERVER.md <wt>/NEED_CLIENT_FIX_*.md <wt>/ISSUE.md 2>/dev/null
  head -50 <wt>/REPORT_<SLUG>-SERVER.md 2>/dev/null
  git -C <wt> rev-parse --abbrev-ref HEAD   # C012 核验 = fix/im-<SLUG>-server

Output ≤ 80 words:
- 📊 commits / 🏷️ subject / 📄 root cause / 🆘 NEED_CLIENT_FIX / 🚨 ISSUE / ⚠️ 卡点 / ⏱ 已用

REPORT 含 "✅ DONE" → `✅ A done — main C012 核验 + 冲突分析 + CronDelete <id>`
ISSUE.md → `🚨 A C012 guard failed — main 介入`
超 60 min → `⚠️ A over budget`
```

### Cron B (client agent monitor, 错开)

```
cron: 9,19,29,39,49,59 * * * *
```

prompt 同上格式，路径换 cses-client + branch `fix/im-<SLUG>-client` + REPORT_<SLUG>-CLIENT.md。

## Stage 6: 主对话给用户的 launch summary

输出表格：2 agent / 2 cron / worktree / baseline / budget。

## Stage 7: 完成通知 handler（事件驱动）

```
收到 task-notification status=completed:
  1. CronDelete 对应 cron id
  2. C012 事后核验:
     branch=$(git -C <wt> rev-parse --abbrev-ref HEAD)
     [ "$branch" = "fix/im-<SLUG>-<side>" ] || 告警 + 查主仓 reflog
     commits=$(git -C <wt> log <baseline>..HEAD --oneline | wc -l)
     [ "$commits" -ge 1 ] || 告警
  3. 冲突分析:
     comm -12 <(git -C <repo-root> diff --name-only <baseline>..fix/im-<SLUG>-<side> | sort) \
              <(git -C <repo-root> diff --name-only <baseline>..HEAD | sort)
  4. 空集 → dry-run:
     git -C <repo-root> merge --no-commit --no-ff fix/im-<SLUG>-<side>
     成功 → commit (结构化 5 段) / 失败 → git merge --abort + 报用户
  5. 非空集 → STOP + 报用户决策
  6. TaskUpdate completed
  7. 等另一通知或进 Stage 8
```

## Stage 8: 全部完成后清理

```bash
git -C /Users/mac28/workspace/golangProject/im worktree remove --force worktrees/<SLUG>-server
git -C /Users/mac28/workspace/golangProject/im branch -D fix/im-<SLUG>-server
git -C /Users/mac28/workspace/angular/temp/cses-client worktree remove --force worktrees/<SLUG>-client
git -C /Users/mac28/workspace/angular/temp/cses-client branch -D fix/im-<SLUG>-client
```

输出验证命令清单：

```
# im (policy B — agent 已自跑 go build/vet/test，列给用户复跑)
cd /Users/mac28/workspace/golangProject/im && go build ./... && go vet ./...

# cses-client (policy A — agent 自跑过 cargo+tsc，主对话不主动)
cd /Users/mac28/workspace/angular/temp/cses-client/src-tauri && cargo check --lib --no-default-features --features custom-protocol --message-format=short
cd /Users/mac28/workspace/angular/temp/cses-client && npx tsc --noEmit -p tsconfig.json 2>&1 | grep "error TS" | head -20
```

## Stage 9: 沉淀（可选）

若三铁律命中（≥ 3 次同根因 / 用户明确 / Spec 拍板）→ 新建 `docs/harness/C{NNN}-*.md` + 同步索引 + commit。

---

# Flags 语义

- `--server-only`: 跳过 client 路径
- `--client-only`: 跳过 server 路径
- `--baseline <sha>`: 覆盖当前仓 baseline
- `--budget <min>`: 调整时间预算（默认 40）

# Defaults

- Dual worktree
- Baseline = 各自仓 HEAD
- Cron 间隔 10 min；server `5,15,...,55` / client `9,19,...,59`（错开整十）
- 编译：server = policy B（agent 自跑 go build/vet/test）；client = policy A（agent 自跑 cargo/tsc）
- 时间预算 40 min；超 60 min ⚠️
- C012 guard 强制注入 agent prompt

# 镜像

- 客户端版本：/Users/mac28/workspace/angular/temp/cses-client/.claude/commands/fork-fix.md
- 两侧结构对称；从任一仓调用都会拉起双仓 worktree
