# 全面替换 Mattermost — 总体路线图

> **目标：** 以 im 项目为基座（Telegram 思想、高性能），全面替换旧 Mattermost (csesapi + web_hub) 架构。
> **核心命题：** 用更少的代码、更少的协议、更少的节点状态，支撑同等或更高的业务容量。

本文档是总纲。后端细节见 [BACKEND.md](./BACKEND.md)，前端切换见 [FRONTEND.md](./FRONTEND.md)。

---

## 一、为什么要替换

### 1.1 架构负债对比

| 维度 | Mattermost (csesapi) | im/server |
|------|---------------------|-----------|
| 代码体量 | 9232 文件 / 108k 符号 / 247k 关系 | 230 文件 / 1.9k 符号 / 5.3k 关系 |
| HTTP 端点 | ~120（RPC 风格） | ~36（REST） |
| WS 事件类型 | 70+ | 7 |
| 消息同步模型 | bitmap(按天)+segment(按时段)+ACK | 频道级 seq 游标 |
| 同步状态机 | Rust 侧 `ChannelBitmapIndex` + `PendingRequest` + `empty_segments` + `fetch_previous/next_segment` 128 轮跳跃 | 客户端比 seq、服务端比 seq |
| Hub 分片 | `runtime.NumCPU()` 个，一致性哈希 | 单 Hub map |
| 跨 Pod 同步 | Mattermost Cluster 内置 broadcast | Redis 路由 + Pulsar topic（业务可复用） |
| 分层 | Handler → App → Store interface → SQLStore（4 层） | HTTP → Service → Repo（3 层） |
| 监控 | 自建 wsmonitor + ES + MCP 工具 | OTel 标准（Prometheus + Jaeger，厂商中立） |

### 1.2 TG 思想核心借鉴

Telegram 的协议/架构以「**序列化状态 + 客户端缓存 + 服务端无状态化**」著称：
1. **单调 seq（pts/qts/seq）**：所有变更带版本号；客户端拿 pts 就能判断「是否落后」
2. **Heartbeat + updates.getDifference**：心跳发现落差 → 客户端主动拉
3. **协议压缩**：MTProto 二进制帧极小；连接层可复用
4. **服务端分区**：DC 按用户物理分片，消息表按 chat 分片
5. **客户端容错**：即使服务端故障，客户端能用本地 state 继续操作（发失败的排队补发）

im/server 在 Go 栈里复刻了核心 2 条（seq + 心跳差分），架构上已对齐；**剩下的是把业务长尾端点补齐 + 前端切换**。

---

## 二、替换的三条并行线

```
              ┌───────────────────────────────────────────────┐
              │       总里程碑 T0 → T6（见 §三）              │
              └─────┬───────────┬───────────┬─────────────────┘
                    │           │           │
        ┌───────────▼──┐  ┌─────▼─────┐  ┌──▼──────────┐
        │   后端线      │  │  前端线    │  │   数据线     │
        │  BACKEND.md   │  │ FRONTEND.md│  │  (本文 §四)  │
        └───────────────┘  └────────────┘  └─────────────┘
```

- **后端线**：补齐 csesapi 缺失能力（P0/P1/P2/P3），Pulsar 跨 Pod 推送闭环（集群水平扩容的前置），压测达标
- **前端线**：54 条 Mattermost API 按模块批量替换到 im REST + 精简 WS
- **数据线**：Mattermost PostgreSQL → im Schema 迁移 + 影子双写 + 灰度切流 + 旧服下线

### 2.1 明确的范围外（不纳入 im 替换）

| 模块 | 承担方 | 前端/调用方如何处理 |
|------|-------|------------------|
| **搜索** `/Im/search/*`、`/search/*` | Java 搜索服务 | 保留现有 Java 调用路径，不改 baseUrl |
| **投票** `/vote/*` | Java 投票服务 | 保留现有 Java 调用路径 |
| **文件上传分片 / 断点续传** | 独立对象存储服务 | 前端现有上传组件保持；im 只登记元数据 |

> 三者在各里程碑验收中**不统计**，不影响 im 替换 Mattermost 的下线节奏。

---

## 三、里程碑（T0 → T6，约 4-5 个月）

| 里程碑 | 周期 | 后端 | 前端 | 数据 | 退出条件 |
|--------|------|------|------|------|---------|
| **T0 准备** | 1 周 | OpenAPI 生成、跨 Pod Pulsar 骨架 | `ImApiAdapter` 骨架、feature flag 架子 | 设计映射表 | 文档齐备，Cl/Cr review 通过 |
| **T1 核心消息 + 集群水平扩容** | 3 周 | M1：撤回/编辑/线程/已读列表 + **按时间跳段 `/messages/around?timestamp=`** + **跨 Pod Pulsar 闭环** + **3 Pod 压测** | F1：发/拉/已读/撤回/线程/按时间跳段 切 im；Rust 切 seq | Users/Channels/Messages 影子映射表跑通 | 单元 80%；单 Pod 50k WS / 集群 150k；投递 P99 <80ms |
| **T2 频道治理** | 4 周 | M2：PATCH /channels + 成员细项 + 公告/紧急/审批/通知/定时/快捷回复 | F2：频道 CRUD + 成员管理 + 收藏切（**搜索不切**） | 影子双写跑起 | 前端聊天+频道覆盖率 >80%，两侧数据一致性校验通过 |
| **T3 企业协作 + 组织** | 3 周 | M3：模板/组织/部门；**投票不在 Go 侧做** | F3：企业协作模块切 | 存量数据迁移脚本 Dry-run | 前端企业协作覆盖率 >90% |
| **T4 Bot 生态** | 3 周 | M4：Agent/Bot/Webhook（新建 subservice） | F4：Bot UI 切 | Bot 元数据迁移 + 正式 ETL 演练 | 新老双跑验证 |
| **T5 全量切换** | 2 周 | M5：生产观测 + hotfix 通道 | 前端 baseUrl 全量切 im；`backend_flavor=im` 永久 | 正式 ETL 迁移（低峰窗口） | 新侧承接 100% 流量，连续 3 天无重大事故 |
| **T6 下线旧服** | 1 周 | M6：删除 legacy mux、关闭 Mattermost 进程 | 清掉 `/api/cses/*` 死代码（保留 search/vote 的 Java 路径） | 归档旧 DB，删 `bitmap`、segment 相关表 | 仓库里搜不到 `/api/cses/posts\|channel\|post/*`；Mattermost 镜像归档 |

> **切流策略（本次决策）：** **不做百分比灰度、不保留回切通道**。T5 一次性全量切换到 im；若出问题通过 hotfix 修 im，不回切到 Mattermost。理由：① 消息 seq 在新旧两侧是不同的单调序列，双向回切会让已读位置错乱；② 切流窗口内的消息在旧侧不存在，回切后不可见——等于回切本身就造成数据断层。一次性切+hotfix 胜于脆弱的回切幻觉。

**总时长：** T0→T6 约 **17 周**（假设两个后端 + 一个前端 + 一个 Rust 专职，DBA 配合）。

### 3.1 集群水平扩容（T1 交付，贯穿 T2–T6）

生产集群模式是本次替换的核心目标之一。**T1 必须完成**：
- Gateway 单镜像多副本部署
- 用户连接分布在任意 Pod，消息跨 Pod 实时投递（Redis routing + Pulsar topic）
- 新 Pod 启动 → 自动订阅 `msg.push.{gatewayID}` topic → LB 分流即可承接
- 任一 Pod 挂掉 → 该 Pod 的用户断开，客户端重连到其他 Pod，通过 `/api/sync` 恢复状态
- 扩容命令：`kubectl scale deploy/im-gateway --replicas=N`，秒级生效

---

## 四、数据迁移大纲

> **策略定调（本次决策）：**
> - **不切回流量**：本次为**全面替换**，切流后旧系统只读归档，不保留双向回切通道
> - **先保证新数据正确**：phantom 机制在历史数据里默认 `phantom_count=0`、`phantom_at_read=0`，未读数可能偏大一次性（首次 /sync 后用户读一遍即自愈）。**历史数据的 phantom 回算暂不做**，列入 T6 之后的 follow-up
> - **影子双写仅正向**（Mattermost → im），不做 im → Mattermost 回写

### 4.1 Schema 映射

| Mattermost 表 | im 表 | 关键转换 |
|--------------|-------|---------|
| `Users` | `users` | 保留原 ID；`Roles` 字符串 → `is_admin bool` |
| `Teams` | `teams` | 直接映射 |
| `Channels` | `channels` | `Type (O/P/D/G)` → `kind (public/private/dm/group)`；`TotalMsgCount` → `last_seq` |
| `ChannelMembers` | `channel_members` | `MsgCount` → `last_read_seq`；`MentionCount` → 运行时重算；`phantom_count=0、phantom_at_read=0`（简化迁移，见上） |
| `Posts` | `messages` | **关键转换**：按 §4.2 migration_sort_key 排序分配 `seq BIGINT`；`Type`（系统消息）→ `msg_type`；`UserId` → `sender_id`；`Props.mentions` → 单独关联表；`OriginalId` 用于标识 edit 链；软删 `DeleteAt>0` 保留 |
| `Posts.RootId` (线程) | `messages.reply_to` | 直接映射 |
| `FileInfo` | `files` + `messages.file_ids` | 外键数组；文件 URL 保持原对象存储路径（不迁移对象存储） |
| `UserSegmentDailyMetadata` | **弃用** | bitmap 体系整条裁掉 |
| `Preferences` | `user_settings` | key/value 平铺 |
| `Reactions` | `message_reactions` | |
| `ChannelBookmark` | `favorites` | |
| `ScheduledPost` | `scheduled_messages` | M2 需要 |

### 4.2 seq 分配：MongoDB ObjectId 思路的 migration_sort_key

Mattermost `Posts.CreateAt` 是毫秒精度，同一 channel 同一 ms 内多条消息常见；`Posts.Id` 是 26 字符随机字母数字串（非时间序）。直接 `ORDER BY (CreateAt, Id)` 会得到**字母序 tiebreaker**，与"先发先序"的业务语义**不一定**一致。

**借鉴 MongoDB ObjectId 设计（12 字节：4B timestamp + 5B random + 3B counter）**：在 ETL 期为每条 Post 在内存中生成 `migration_sort_key`，用这个 key 排序后分配 seq：

```
migration_sort_key (BIGINT, 64 bit) 布局：
  [48 bit] CreateAt_ms (Mattermost 原毫秒时间戳)
  [16 bit] intra_ms_counter (同一 channel 同一 ms 内的单调计数)

ETL 生成算法（按 channel 分组单线程处理）：
  for each channel:
    sort posts by (CreateAt ASC, Id ASC)        # stable sort, 与 MM 收到顺序 best-effort 一致
    last_ms := 0
    counter := 0
    for post in sorted:
      if post.CreateAt == last_ms:
        counter++
      else:
        last_ms = post.CreateAt
        counter = 0
      post.migration_sort_key = (last_ms << 16) | counter

    # 再按 migration_sort_key 稳定重排 —— 等价于上一步，但显式建索引字段
    sort posts by migration_sort_key ASC
    seq := 1
    for post in sorted:
      INSERT INTO messages (..., seq = seq, created_at = from_ms(post.CreateAt), ...)
      seq++
    UPDATE channels SET last_seq = seq-1 WHERE id = channel.id
```

**关键性质：**
- 单调：同 channel 内 seq 严格递增，无空洞
- 可复现：给定同一份 Mattermost 数据 + 同一排序算法，分配结果确定性一致（便于重跑）
- 溢出安全：16 bit intra_ms counter 允许单 channel 单 ms 内 65535 条消息，远超实际峰值
- 保留 `mm_post_id` 作 `messages.legacy_id` 字段，建索引，供审计追溯

> **为什么不直接用 Mattermost 的 Post.Id：** MM Post.Id 是随机 UUID 类字符串（非时序）。按字母序排会把同 ms 内消息次序与用户感知打乱。本方案用 MM 的 `CreateAt` 做主时间轴、用 Post.Id 字母序做确定性 tiebreaker，并**显式构造** migration_sort_key 记录分配意图——未来如果发现排序有问题，可以按此算法重跑一次而不改变代码。

### 4.3 迁移步骤

```
阶段 A — Dry-run（T4 开始）
  1. ETL 脚本连 Mattermost PG 读数据（只读 snapshot）
  2. 按 §4.2 算法计算 migration_sort_key、分配 seq
  3. 导入到 im 的测试环境 PG
  4. 跑验证清单（见 §4.4）
  5. 失败修 bug、重跑，直到清单全绿

阶段 B — 影子双写（T4 末 - T5）
  旧系统继续正常运行（Mattermost PG 主写）
  新消息在业务层同时写入 im PG（旁路，只正向）
  周期校验：最近 1 小时两边消息数对齐
  ⚠️ 本阶段 im 仅接收写入，不对外提供服务

阶段 C — 正式 ETL（T5 切流当天低峰窗口）
  1. 冻结 Mattermost 写（只读模式，应用层开 read-only flag）
  2. 增量 ETL 把 T4 Dry-run 之后新增的 Posts 导入 im
  3. 数据一致性校验全绿
  4. 前端 baseUrl 全量切 im
  5. Mattermost 解冻，但此时前端不再访问 → Mattermost 实际无流量

阶段 D — 稳定观察（T5 剩余时间）
  观察 48-72 小时，错误率、P99、未读数抽样校验
  ⚠️ 不做灰度分流。本次是全面切换，问题只通过 hotfix 修 im，不回切

阶段 E — 下线（T6）
  旧 Mattermost PG 降级为只读，保留 90 天后彻底归档
  Mattermost 应用停服、镜像入仓
```

### 4.4 一致性验证清单
- [ ] 每 channel 的消息数相等（含软删）
- [ ] 每 user 的 last_read_seq 与旧 `ChannelMembers.MsgCount` 映射正确（落点一致）
- [ ] 文件附件 URL 可访问（保持原对象存储路径，不迁移存储）
- [ ] 时间戳不漂移（`messages.created_at` ≈ MM `CreateAt`，毫秒对齐）
- [ ] 收藏、反应、回复链关系完整
- [ ] 系统消息（加入频道/改名等）转换后文案一致
- [ ] **新增**：同 channel 同 ms 内多条消息的 seq 顺序 = 按 mm_post_id 字母序（可复现）
- [ ] **新增**：每 channel 的 `channels.last_seq` == max(`messages.seq` where channel_id = X)

---

## 五、替换成功验证计划（收敛版）

> **节奏转变：** 本次是"一晚 AI 生成全量后端功能 + 前端对接"。验证必须**今晚可跑完、能出明确 GO/NO-GO 结论**，不等 3 个月稳态。
>
> **替换成功分两阶段定义：**
> - **今晚 GO**：`make verify-all` 全绿（§5.1 一键剧本）→ 第二天可推 staging
> - **生产 GO**：上线后持续 30 天 §5.6 业务 KPI 达标 → 宣告最终替换成功（长尾验证）

### 5.1 今晚验证剧本：`make verify-all`（目标 90 分钟跑完）

> 所有验证收敛到一条 make 命令。**跑完全绿 = 第二天可进 staging**。
> 写成 Makefile target，每一步是一条 shell 脚本；失败立刻停、打印故障点。

```makefile
verify-all: verify-build verify-unit verify-integration verify-cluster verify-smoke verify-frontend
	@echo "✅ ALL GREEN — ready for staging"
```

| 阶段 | Makefile target | 超时 | 目标 |
|------|----------------|------|------|
| **V1** 编译与静态检查 | `verify-build` | 3 min | `go build ./... && go vet ./... && golangci-lint run` |
| **V2** 单元测试 | `verify-unit` | 10 min | `go test -race -short ./...`；核心包覆盖率 ≥ 70% |
| **V3** 集成测试（docker-compose 起 pg+redis+pulsar+2 gateway） | `verify-integration` | 30 min | 全量端点 smoke；每端点 1 条 test（存在 + 返回预期 schema） |
| **V4** 集群容错冒烟 | `verify-cluster` | 15 min | §5.2 四场景（跨 Pod、Pod kill、Pulsar 停、并发 seq） |
| **V5** 业务流程 + 模块组 | `verify-smoke` | 30 min | §5.3 单流程 10 条 + §5.3.1 模块组 10 组（联动剧本） |
| **V6** 前端切换冒烟 | `verify-frontend` | 10 min | cses-client 分支指向本地 im，登录+列表+发消息 |

**总时长 ≈ 98 分钟。** 跑完绿灯 → **今晚替换验证完成**。

### 5.2 V4 集群容错冒烟（docker-compose 2 Pod 版）

只跑本地 docker-compose 版本，不上真 K8s；今晚全绿即 Pass，真 K8s 扩缩容验证推迟到 staging。

```bash
# docker-compose.test.yaml: pg + redis + pulsar + gw-1 + gw-2
make verify-cluster  # 内部跑 4 个 Go test
```

| 场景 | 判定 | 预期耗时 |
|------|------|---------|
| 跨 Pod 推送 | A 连 gw-1 发消息，B 连 gw-2 收到 `push_msg` | 30s |
| Pod kill 后重连 | `docker kill gw-1` → 客户端 30s 内重连到 gw-2 → `/api/sync` 无损恢复 | 60s |
| Pulsar 抖动 | `docker pause pulsar` 20s 后 unpause；期间消息走 markOffline；恢复后 /sync 补齐，**零丢消息** | 60s |
| seq 并发安全 | 100 goroutine 同 channel 发消息，断言 seq 严格单调 gap-less | 30s |

> **注意：** 今晚不跑 150k WS 压测（需要专用压测环境 + 较长时间）；这属于 **staging 必须跑**的指标，不在今晚验收。

### 5.3 V5 业务流程冒烟（10 条关键路径）

Go test 脚本化，每条失败立刻停，日志定位到故障端点。

| # | 流程 | 涉及端点 |
|---|------|---------|
| 1 | 注册 → 登录 → 拿 JWT → `GET /api/me` | `/api/auth/*`、`/api/me` |
| 2 | 创建频道 → 加成员 → 群内发消息 → 成员 WS 收到 | `/api/channels`、`/channels/:id/members`、`/channels/:id/messages`、WS `push_msg` |
| 3 | DM 创建 → 发消息 → 对方收 | `/api/channels/dm`、`/channels/:id/messages` |
| 4 | 批量 /sync → 小 gap 返回全量消息 + unread 计算正确 | `/api/sync` |
| 5 | /sync 大 gap → `has_more=true` + 最后 50 条；再拉 `?before_seq=` 追平 | `/api/sync`、`GET /channels/:id/messages?before_seq=` |
| 6 | 已读推进 → 其他设备 WS 收 `read_sync` | `/channels/:id/read`、WS `read_sync` |
| 7 | 消息撤回 → 成员收 `msg_deleted` | `DELETE /messages/:id`、WS `msg_deleted` |
| 8 | 消息编辑 → 成员收 `msg_updated` | `PATCH /messages/:id`、WS `msg_updated` |
| 9 | 线程回复 → 主消息挂 reply → `GET /messages/:id/replies` 返回链 | `POST /channels/:id/messages (reply_to)`、`GET /messages/:id/replies` |
| 10 | 按时间跳段 → `GET /channels/:id/messages/around?timestamp=<ms>` 返回前后 50 条且 seq 单调 | `GET /channels/:id/messages/around` |

**达标判定：10 条单流程全绿 + 下一小节 10 个模块组全绿 = V5 Pass**。

### 5.3.1 V5 模块组联动（增量追加：剧本式连续性验证）

> **为什么需要模块组？** 单接口绿灯只证明"这个 API 能跑"，不能证明「**一组有连续性的功能之间状态对齐**」。例如：发消息返回 seq=10 → 已读 API 需要能把 last_read_seq 推到 10 → 其他设备 /sync 回来的 unread 必须 = 0。任一步状态断链都意味着生产会出 bug。
>
> **设计原则：** 每个组是一个**端到端剧本**（script），启动一次测试环境（WS + HTTP 客户端池），按步骤串行跑，每步验证**当前状态** + **与前一步的连续性**。
>
> **失败即停**：组内任一步失败，立即停止该组并打印状态机断点。

| 组 | 剧本 | 步骤 | 关键断言 |
|----|------|------|---------|
| **G1** 消息生命周期 | 发→读→编辑→撤回→/sync | ① A 发消息得 seq=S；② A 标已读 last_read_seq=S；③ A PATCH 消息内容；④ A DELETE 消息；⑤ B `/sync` | 每步后 `/sync` 返回的消息状态与预期一致；最终 B 端消息应为 deleted 占位 |
| **G2** 多设备一致性 | A-web + A-mobile + B 三端同时在线 | ① A-web 发消息；② B 收 push_msg；③ A-mobile 同时收到自己消息回显；④ A-web 标已读；⑤ A-mobile 收 read_sync | A 两端 unread 对齐；B 独立 unread 正确；无重复推送（push_id 去重有效） |
| **G3** 线程会话 | 发主消息 + 3 条回复 + 编辑回复 + 撤回主消息 | ① 主消息 seq=M；② 3 条 reply_to=M 的回复；③ `GET /messages/M/replies` 返回 3 条；④ 编辑 reply[1]；⑤ 撤回主消息 | 回复链完整、编辑推 msg_updated、撤回主消息后回复仍可见（设计决策：主消息软删不级联） |
| **G4** 频道治理 | 创建→加员→发消息→移除→再/sync | ① 创群加 B；② B 发 10 条消息；③ 踢 B；④ B 再 /sync | B 的 /sync 返回**不包含**该 channel delta（服务端按 GetMemberChannelSeqs 过滤）；A 侧频道仍存在且消息完整 |
| **G5** 跨 Pod 连续性 | A 连 gw-1 → gw-1 kill → A 重连 gw-2 继续发 | ① A 连 gw-1 发 seq=1..5；② docker kill gw-1；③ A 自动重连 gw-2；④ A 继续发 seq=6..10；⑤ B（始终连 gw-3）/sync | seq 严格连续 1-10；B 在 gw-3 上收到全部 10 条；重连过程未丢消息 |
| **G6** 离线消息补齐 | B 断 WS → 群内 3 条 → B 登录 → 已读 → 他端同步 | ① B 下线；② A 发 msg1/2/3；③ B 登录 → /sync 返回 3 条、unread=3；④ B 标已读；⑤ B 另一设备登录 | 另一设备 /sync 返回 unread=0、last_read_seq=3 |
| **G7** 好友全流程 | A → B 请求 → 接受/拒绝 | ① A `POST /friends/request(B)`；② B WS 收 friend_event(request, from=A)；③ B `POST /friends/accept(A)`；④ A WS 收 friend_event(accepted, from=B)；⑤ 双端 `GET /friends` 包含对方 | 拒绝路径同样跑一遍：A WS 收 friend_event(rejected)，双端好友列表无对方 |
| **G8** 文件附件联动 | 上传 → 带附件发消息 → 拉附件 → 撤回 | ① `POST /files` 得 file_id；② `POST /channels/:id/messages` 携带 file_ids；③ B `GET /messages/:id/attachments` 返回文件元数据；④ A DELETE 消息；⑤ B 再拉附件 | 撤回后附件元数据状态 = unavailable 或 404（设计决策：消息 soft-delete 时附件保留 30 天） |
| **G9** 断网恢复 | A 发 10 条 → 断网 30s → 重连 → /sync | ① A 发 10 条；② A 断网（模拟 `conn.Close()`，knownSeq=5 假设只推到 5）；③ 30s 后 A 重连 WS；④ 首次 ping 携带 channel_seqs={X:5}；⑤ pong 返回 server > client；⑥ A POST /sync 补齐 6-10 | 断网期间其他成员正常收消息；A 重连后本地 seq 从 5 平滑补到 10，UI 无抖动 |
| **G10** 大群扇出 | 10 成员群，1 人发 → 9 人同步到 | ① 10 人分布在 3 个 gateway Pod（按 userID hash）；② A 发一条消息；③ 统计 9 个接收方 WS `push_msg` 到达时间；④ 并发发 10 条 | 到达率 = 100%；P99 <80ms；seq 与到达顺序一致；无重复（push_id 去重） |

**组级通用断言（每组额外校验）：**

1. **状态连续性**：每步后客户端本地状态（unread / last_read_seq / 消息列表）= 服务端通过 /sync 返回的状态
2. **事件顺序**：WS 事件到达顺序 = 业务动作顺序（发消息 → 已读；不会反过来）
3. **幂等性**：同 push_id 重复推送，客户端应识别并去重，UI 不显示两条
4. **跨端一致**：同用户多设备，任一设备的写操作必须在其他设备通过 `read_sync` / `msg_updated` / `msg_deleted` 同步

**达标判定：10 组剧本全部按顺序跑完无断点 = V5 模块组 Pass。**

**实现建议：**
- Go test 用 `testify/suite` 每组一个 Suite，Setup/Teardown 管理客户端连接池
- 所有组在同一 docker-compose 测试环境下串行跑（共享 pg/pulsar/redis 但每组用独立 tenant/channel 避免互相污染）
- 每组超时 2-3 分钟，10 组 <30 分钟

### 5.4 V6 前端冒烟（cses-client 分支版）

```bash
# cses-client 新分支里：
cd cses-client && git checkout -b im-backend-switch
# 改 environment.ts: imGatewayHttp = http://localhost:8080
npm run dev
# 浏览器：登录 → 进 message-v3 → 看频道列表 → 发一条消息 → 看见自己的消息立刻出现
```

| 判定项 | 达标 |
|-------|------|
| 登录 `POST /api/auth/login` 返回 JWT | ✅ |
| `GET /api/channels` 返回频道列表 | ✅ |
| 发消息 `POST /channels/:id/messages` 返回 201 + seq | ✅ |
| WS 连接建立、收到自己刚发的 `push_msg` | ✅ |
| 浏览器 console 无红色错误 | ✅ |

**5 条全绿 = V6 Pass = 今晚替换验证完成**。

### 5.5 今晚 GO / NO-GO 决策

| 条件 | 状态 | 行动 |
|------|------|------|
| `make verify-all` 全绿 | ✅ | **今晚 GO**；明早推 staging |
| 某个 V* 红灯 | ❌ | 停止前进；定位故障在 V1/V2/V3/V4/V5/V6 哪层；按优先级修（V1 > V2 > V3 > V4 > V5 > V6）；修到全绿再走下一步 |
| 超过 3 小时仍有红灯 | ⚠️ | 记录 blocker 清单；暂停替换计划；第二天与团队 review |

**今晚 GO 不等于可以切生产流量**。它意味着：
- 代码功能**在本地全量通** → 可以进 staging
- staging 跑 1-2 天真实流量 + 压测（见 §5.6）→ 才能切生产

### 5.6 Staging 与生产上线验证（今晚之后的长尾）

**今晚不跑，但必须跑**的验证（放到上 staging / 生产后持续观察）：

| 阶段 | 验证项 | 目标 |
|------|-------|------|
| **Staging（上线前 1-3 天）** | 真 K8s 集群 3 Pod 压测 | 单 Pod 50k WS / 集群 150k / 10k msg/s / 投递 P99 <80ms |
| | 真 Pulsar + 真 Redis 集群 | 跨 Pod 推送成功率 >99.9% |
| | 内网真人流量回放（镜像部分生产流量） | 错误率 <0.1% 持续 24h |
| | 数据迁移 Dry-run | 一致性清单（§4.4）100% 达标 |
| **生产切流后 72h** | 监控观察窗 | 错误率 <0.1% + P99 保持 + 无 P0 告警 |
| **生产切流后 30 天** | 业务 KPI（见下表） | 全达标 → 宣告最终替换成功 |

**生产 30 天业务 KPI 清单：**

| 维度 | 指标 | 达标 |
|------|------|------|
| 可靠性 | HTTP 5xx 错误率 | <0.1% |
| | 跨 Pod 推送成功率 | >99.9% |
| 性能 | /api/sync P99 | <100ms |
| | 发消息端到端 P99 | <200ms |
| 业务 | 日活与切流前基线 | ±5% 内 |
| | 客服"消息丢失"工单 | 回落到基线 |
| | 客服"消息收不到"严重工单 | = 0 |
| 运维 | P0 告警次数 | = 0 |
| | Pod OOMKilled | = 0 |
| | 扩容响应 | `kubectl scale` < 60s |

### 5.7 监控与可观测性（staging 前必须就位）

**今晚不阻塞**（功能验证不需要面板），但 staging 之前 SRE 必须完成：

- Prometheus 指标（im 内建 `/metrics`）：`im_ws_active_connections`、`im_http_request_duration_seconds{endpoint}`、`im_push_cross_pod_success_total`、`im_push_pulsar_error_total`、`im_seq_alloc_duration_seconds`、`im_routing_refresh_total`
- Grafana 4 面板：总览 / 跨 Pod 推送 / /sync / Pulsar 健康
- Jaeger：业务关键路径 100% 采样，心跳 1%
- Alertmanager：按 §5.6 阈值 × 1.5 = P1、× 2 = P0

### 5.8 事后复盘（上生产 30 天一次）

生产稳定 30 天后做一次复盘：对比 §5.6 KPI、客服工单、SRE 告警；产出《替换 30 天报告》，宣告最终成功或启动补救。

---

## 五附、原长尾验证计划（17 周节奏版，仅作备用参考）

> 如果节奏从"一晚生成"回到"T0-T6 17 周逐步交付"，验证可以采用更分层的金字塔模型：
>
> - **层 0** DAY 0 基座 PR
> - **层 1** 单元 + 集成（贯穿，覆盖 80%+）
> - **层 2** 功能对等（每里程碑末 vs Mattermost 行为逐项对照）
> - **层 3** 集群容错（跨 Pod 5 场景 + 150k 压测）
> - **层 4** 性能与容量（9 项硬指标 + 4 个压测场景）
> - **层 5** 数据迁移一致性（§4.4 全绿）
> - **层 6** 生产稳态（T6+30 天 KPI）
>
> 每层以下层为前提；Go/No-Go 门禁按 DAY 0→M1→M2→...→T6+30 天逐级推进。
>
> 本次采用"一晚生成"快节奏，**不走这套金字塔**；§五 主线就是 `make verify-all`。

---

## 六、风险矩阵

| 风险 | 影响 | 概率 | 缓解 |
|------|------|------|------|
| seq 唯一性在高并发下错乱 | 致命（消息乱序） | 低 | `AllocSeqAndInsert` 单一入口 + UPDATE...RETURNING 同事务；CI grep 检查无旁路；压测覆盖 |
| 跨 Pod 推送链路假死（Pod STOP / 网络分区） | 高（用户收不到消息） | 中 | Routing TTL=45s 自动过期；消息权威源是 PG + seq，客户端 `/sync` 兜底不丢 |
| Pulsar 消费滞后导致跨 Pod 推送延迟 | 高（用户感知延迟） | 中 | 监控 `im.push.pulsar_lag`；容量预留 3× 峰值；`markOffline` + 客户端 /sync 兜底 |
| 企业协作长尾端点漏迁 | 中（某功能不可用） | 高 | F3 前做端到端清单校验；每模块 QA 验收 checklist |
| 客户端 Rust 缓存与新 schema 冲突 | 中（升级后需清缓存） | 高 | 应用升级时强制清 SurrealDB；给用户明确提示"首次登录会重新同步" |
| 数据迁移 seq 分配错误 | 致命（历史消息乱） | 低 | migration_sort_key 可复现算法；Dry-run 跑到一致性清单全绿才进正式；每 channel 抽查 |
| phantom 历史数据初始为 0 造成未读偏大 | 低（首次读一次自愈） | 高 | 接受；产品侧提示"首次登录可能看到未读数偏大" |
| 已读语义改动让「已读人员」功能缺失 | 高（产品侧反对） | 高 | M1 交付 `/api/messages/:id/readers`；UI 保留原视觉 |
| T5 切流后发现严重问题 | 致命（不可回切） | 低 | 不走百分比灰度但 T0-T4 测试覆盖率必须 >80%；T5 前 3 天做 staging 全量模拟；hotfix 通道就绪 |
| 附件 URL 对象存储变更 | 中 | 低 | 保持原对象存储不动；im 只存元数据 |
| WS 事件命名两侧不一致 | 中（前端订阅漏收） | 中 | V1 锁定 12 种（见 BACKEND §1.1）；CI 校验 Go 常量名与 FRONTEND §4.1 表一致 |

---

## 七、团队分工建议

| 角色 | 人 | 主要任务 |
|------|---|---------|
| 后端 Owner | 1 | gateway 架构、sync 语义、Pulsar 推送闭环 |
| 后端 P1 | 1 | 企业协作端点（公告/紧急/审批/通知/定时） |
| 前端 Owner | 1 | `ImApiAdapter` + `MessageHttpService` 改造 + 事件分发 |
| Rust 专职 | 1 | `ImSeqDataSource` + bitmap 下线 + 缓存迁移 |
| DBA | 0.5 | Schema 设计评审、ETL 脚本、索引规划、迁移演练 |
| QA | 1 | 场景用例沉淀、压测执行、切流期回归 |
| SRE | 0.5 | OTel 面板、告警、灰度控制面 |

---

## 八、产出物交付清单（T6 收官）

- [ ] **代码**：im/server 完整替换 Mattermost 功能；legacy mux 移除；Rust bitmap 代码删除
- [ ] **文档**：`docs/BACKEND.md` / `FRONTEND.md` / `OVERALL.md`（本套三件）持续更新；`docs/api.md`（OpenAPI）同步 Apifox
- [ ] **测试**：单元 80%；集成测试覆盖所有主路径；压测报告（3 场景基线）
- [ ] **运维**：OTel 面板（`/observe` skill）；Grafana dashboard（`grafana-knowledge` 引导）；告警规则（Alertmanager）
- [ ] **数据**：ETL 脚本 + 校验报告 + 迁移后全量一致性报告
- [ ] **回退预案**：灰度期任意时间点可回到旧系统的 step-by-step runbook

---

## 九、与 TG 思想的差距（未来演进）

本次替换在架构上已对齐 TG 核心，但还有演进空间：

1. **MTProto-like 二进制协议**：当前 JSON，下一步引入 MessagePack / Protobuf，WS 帧体积降 40%+
2. **Geo 分区**：TG 的 DC 按用户物理分片。im 目前单 DC；多 DC 需要 Channel shard key + 跨 DC 消息路由
3. **端到端加密（Secret Chat）**：TG 有；当前 im 未规划
4. **客户端 Offline Queue**：TG 客户端断网可继续排队发送。前端 Rust 侧可补
5. **分布式存储**：消息表超 1B 行时引入 Citus / Vitess / TiDB 分片
6. **Push Service**：APNs/FCM 推送（当前只走 WS），需要独立 push service 订阅 Pulsar

这些作为 **T6 之后的长期演进**，不在本次替换范围内。

---

## 十、下一步

### 今晚（DAY 0 — 三个前置 PR，详见 `BACKEND.md §六 DAY 0`）

严格按此顺序合并，解锁明早 M1 / F1 双端并进：

| 顺序 | PR | 内容 | 解锁 |
|------|----|------|------|
| 1 | **PR-A** | `POST /api/sync` 契约锁定（前端在 cses-client 新分支直接按 §3.3 手写 TS） | 前端 F0 `ImApiAdapter`、Rust `ImHttpClient` |
| 2 | **PR-B** | `AllocSeqAndInsert` Repo 签名（`ctx, tx *gorm.DB, msg`，可选外部事务）+ 测试骨架 | M1 所有发消息相关功能的 Repo 基座 |
| 3 | **PR-C** | `crossPodPush` + routing TTL 续期 + Pulsar `ProducerCache` + 环境感知 `pushTopicFor`（prod/pre 固定 ns、local 自动加 `$USER` 后缀）+ 4 Pusher 改造 | M1 所有 WS 推送自动享受集群扩容能力 |

### 本周（M1 / F1 启动）

- **后端**：DAY 0 三 PR 合并后，撤回 / 编辑 / 线程 / readers / around-timestamp 五端点并行开工
- **前端**：在 cses-client 新分支按 BACKEND §3.3 手写 TS 类型 + 搭 `ImApiAdapter` 骨架 + feature flag 架子；`ipcEvent.service.ts` 标注订阅迁移清单
- **Rust**：`ImHttpClient.sync()` + `ImSeqDataSource` 骨架（与后端 Go struct 对齐，同样手写）
- **DBA**：起草 Mattermost → im Schema 映射初稿 + `migration_sort_key` 算法脚本样稿
- **SRE**：预建 Pulsar namespace `im/push` 和 `im/push-pre`（详见 BACKEND §六 DAY 0 #1），PR-C merge 前就位

### 持续

- **每周同步**：每周五总结三条线进度，发到团队频道（用 im 自己的后端当然是最好的 dogfood 🙂）
- **文档滚动**：DAY 0 三个 PR 合并后，回填实际代码位置到 `BACKEND.md §六 DAY 0`（例如 `internal/gateway/cross_pod_push.go:42`）
