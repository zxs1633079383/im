# im — 完整执行流程文档（EXECUTION_FLOWS）

> 本文档目标：**完整**描述 im 后端所有关键路径的执行流程，而不是速查。
> 配套图：`server/docs/diagrams/*.mmd` 共 8 张（VSCode Mermaid Preview / mermaid.live / Typora 可直接渲染）。
> 读前要会：`docs/ARCHITECTURE.md` 目录地图 + 知道 Gin/GORM/Pulsar/Redis 是干嘛的就够了。

---

## 目录

- [0. 为什么要读这份文档](#0-为什么要读这份文档)
- [1. 核心名词与不变量](#1-核心名词与不变量)
- [2. 【核心疑问】Redis 路由到底怎么工作](#2-核心疑问redis-路由到底怎么工作)
- [3. WS 连接建立与登出](#3-ws-连接建立与登出)
- [4. 心跳 ping-pong 与旧消息发现机制](#4-心跳-ping-pong-与旧消息发现机制)
- [5. 消息发送（HTTP / WS 两路径）](#5-消息发送http--ws-两路径)
- [6. 跨 pod 推送全链路](#6-跨-pod-推送全链路)
- [7. 已读标记与多端 read_sync](#7-已读标记与多端-read_sync)
- [8. 消息编辑 / 撤回（msg_updated / msg_deleted）](#8-消息编辑--撤回msg_updated--msg_deleted)
- [9. 重连与增量同步 POST /api/sync](#9-重连与增量同步-post-apisync)
- [10. "闲时同步" —— 本项目的等价机制](#10-闲时同步--本项目的等价机制)
- [11. 频道创建 / 更新 / 成员变更](#11-频道创建--更新--成员变更)
- [12. 好友流程：搜索 / 申请 / 接受 / 拒绝](#12-好友流程搜索--申请--接受--拒绝)
- [13. Topic 子群聊 / Presence](#13-topic-子群聊--presence)
- [14. M2 业务功能（公告 / 紧急 / 审批 / 通知 / 定时 / 快捷回复）](#14-m2-业务功能公告--紧急--审批--通知--定时--快捷回复)
- [15. 其他（收藏 / 文件 / 搜索 / 设置 / 资料）](#15-其他收藏--文件--搜索--设置--资料)
- [16. 极端场景补发与兜底](#16-极端场景补发与兜底)
- [17. 易踩坑列表](#17-易踩坑列表)
- [18. 每条路径的主入口文件](#18-每条路径的主入口文件)

---

## 0. 为什么要读这份文档

代码地图告诉你**文件在哪**，契约文档告诉你**接口长什么样**；但是当你需要：

- 定位一个 bug（"消息没收到" / "读已读不同步" / "断线后消息乱了"）
- 在一个新场景里做设计（"加一个新 WS 事件" / "支持群成员角色"）
- 判断某个改动的爆炸半径（"改 heartbeat 间隔行不行" / "能不能把 routing 搬到 etcd"）

你真正需要的是一张**执行时序图**：时间轴上的每一步是谁做的、为什么这么做、挂了怎么救。这份文档就是做这个。

---

## 1. 核心名词与不变量

### 1.1 名词

| 名词 | 含义 | 代码位置 |
|------|------|---------|
| `gatewayID` | 本 gateway pod 的唯一 ID，启动时从 hostname / env 注入 | `cmd/gateway/main.go` |
| `channels.last_seq` | 每频道单调自增的服务端序号，**唯一真相** | migration `001` |
| `channel_members.last_read_seq` | 每成员已读到的 seq | migration `001` |
| `channel_members.phantom_count` | 对该成员"不可见"的消息累计数（directed 消息） | migration `001` |
| `routing` | Redis Hash：`im-new:routing:user:{uid}` → `{deviceID → gatewayID}` | `repo/routing.go:41` |
| `push_id` | 每次 WS 推送的幂等键，客户端用它做 ACK | `gateway/types.go:77` |
| `PushTopicFor(gwID, env)` | Pulsar topic 名字：`persistent://im/push[-pre,-local]/msg.push.{gwID}[.{dev}]` | `gateway/topic.go` |
| `knownSeq` | Conn 对象内存里 `{chID → 已推过 / 客户端已声明的最大 seq}` | `gateway/conn.go:27` |
| `PulsarPushEvent` | 跨 pod 投递的消息结构（TargetUID + 全部推送字段） | `gateway/types.go:143` |
| `WSFrame` | WS 包装层：`{type, payload}` | `gateway/types.go:56` |

### 1.2 不变量（写在石头上，改代码先问自己有没有违反）

1. **seq 唯一入口 = `MessageRepo.AllocSeqAndInsert(ctx, tx, msg)`**
   - `UPDATE channels SET last_seq=last_seq+1 RETURNING seq` + `INSERT messages` 在同一个事务
   - `tx != nil` 时复用外部事务（可组合在更大事务里）
   - 任何其他地方写 `UPDATE channels SET last_seq=...` 都是 bug
   - 代码：`repo/message.go:110-153`

2. **跨 pod 推送唯一入口 = `Hub.CrossPodPush(...)`**
   - 三路径：local hub hit → Redis routing.Lookup → Pulsar producer
   - 任何绕过它直接 `hub.PushToUser` 的路径都会导致"跨 pod 用户收不到"
   - 代码：`gateway/cross_pod_push.go:46-67`

3. **WS conn 与 gateway pod 绑死**
   - 一个 WS conn 只存在于一台 pod 的内存里
   - Redis routing 告诉发送方"目标 user 在哪个 gatewayID"
   - Pulsar topic 按 gatewayID 分片，每 pod 只订阅 `msg.push.{自己的 gatewayID}`
   - **这三条合起来实现了"不用 Hub RPC、不用广播、精准投递"**

---

## 2. 【核心疑问】Redis 路由到底怎么工作

先回答一个经常被问的问题：

> **"你用 Redis 当 WS 注册功能来用，WS 怎么知道我的推送应该推给哪几台机器？"**

### 2.1 视角转换

答案是：**WS 侧不做这个决策，发送方做**。具体来说：

- Redis routing 的**读者**不是 WS conn，而是**要发推送的代码**（HTTP handler 里的 pusher、broadcaster 等）
- WS conn 只是 Redis 的**写者**：登录时写入"我在这个 pod"，心跳时刷新 TTL，登出时删除

### 2.2 机制拆解

```
注册阶段（写 Redis）：
  user-1024 连上 pod-A 的 WS
  → pod-A 的 ws_handler:
      hub.Register(conn)                    内存层：本 pod 内 {1024 → *Conn}
      routing.Register(1024, deviceID)      Redis：HSET im-new:routing:user:1024 deviceID pod-A, TTL 45s

推送阶段（读 Redis）：
  任何 pod 收到"要推给 user-1024"的需求（可能来自 HTTP handler / 定时任务 / msg_updated 广播）
  → Hub.CrossPodPush(1024, ...):
      Step 1 本 pod hit? hub.PushToUser(1024) → 如果成功直接 return
      Step 2 routing.Lookup(1024) → ["pod-A"]  (Redis HGETALL 去重)
      Step 3 对每个 gwID (≠本 pod):
               topic = PushTopicFor(gwID, env)  → "msg.push.pod-A"
               producer = cache.GetOrCreate(topic)
               producer.Send(key=1024, PulsarPushEvent)

消费阶段（Pulsar topic 天然路由）：
  pod-A 启动时做了：
      PushConsumer.Start(ctx):
          topic = PushTopicFor(本pod gatewayID, env)  → "msg.push.pod-A"
          subscribeName = "gateway-push-pod-A"         ← 独占订阅
          pulsarClient.NewConsumer(topic, subscribeName, Handle)
  只有 pod-A 会收到 msg.push.pod-A 的消息，其他 pod 订阅的是别的 topic
  pod-A 的 Handle: hub.ConnsForUser(1024) → [*Conn] → 推送
```

### 2.3 为什么这样设计而不是别的

| 替代方案 | 为什么不选 |
|---------|-----------|
| 广播给所有 pod | N 倍放大，成员 1 万用户 × 3 pod = 3 万次 conn 查询浪费 |
| Hub 之间加 RPC（一 pod 转发给另一 pod） | 加 pod 数量时 N² 连接；pod 重启复杂度大 |
| 把 WS conn 的信息也塞 Redis | conn 是 TCP socket，不能序列化；Redis 只能存"位置"不能存"socket" |
| 用一致性哈希把 user 固定到 pod | 负载均衡硬约束，用户迁移复杂；Ingress 不按 user 粘性打流量 |

### 2.4 可视化

→ 见 [`diagrams/01-cross-pod-push.mmd`](./diagrams/01-cross-pod-push.mmd)

### 2.5 多端（同一 user 多设备）的自然扩展

一个 user 可能在手机、PC、平板三端同时在线：

```
routing:user:1024 = HASH {
  device-phone  → pod-A
  device-pc     → pod-B
  device-pad    → pod-C
}
```

`Lookup(1024)` 返回去重后的 `["pod-A", "pod-B", "pod-C"]`，对每个 gwID 各发一次 Pulsar → 三端都收到。

`read_sync` 用的就是这条路径（§7）。

### 2.6 弱点与已知风险

1. **Pulsar send 失败时不摘 routing**（`markOffline` 骨架位未实现）
   - 症状：对端 pod crash 后，新请求仍会往它的 topic 发；消息会堆在 Pulsar 里
   - 缓解：订阅 10min 过期清堆积；客户端重连后走 sync 兜底
   - 修复方向：send 失败累计到阈值 → HDEL 该 deviceID

2. **routing stale 窗口**
   - 用户从 pod-A 迁移到 pod-B 的 T 秒内（心跳周期），发送方可能仍拿到 "pod-A"
   - 旧 pod-A 的 PushConsumer 收到但 `ConnsForUser(1024)` 返空 → 直接 ACK Pulsar → 该条丢
   - 兜底：pong diff / sync 必然触发补齐

---

## 3. WS 连接建立与登出

### 3.1 入参

```
GET /ws?token=<JWT>&device=<device_id>
Upgrade: websocket
```

### 3.2 执行顺序（`gateway/ws_handler.go:82-136`）

```
ServeHTTP:
  1. tokenStr = query["token"]；auth.ValidateToken(secret, tokenStr) → claims.UserID
     失败：401 missing/invalid token

  2. deviceID = query["device"] 或 fmt.Sprintf("web-%d", time.Now().UnixNano())

  3. upgrader.Upgrade(w, r) → *websocket.Conn
     失败：log warn + return

  4. conn = NewConn(userID, deviceID, ws, hub)
       └─ 启动 writePump goroutine（从 conn.send 读，写到 ws）

  5. ctx, cancel = context.WithCancel(r.Context())
     defer cancel

  6. hub.Register(conn)
       └─ 加锁 append 到 h.conns[userID]

  7. routing.Register(ctx, userID, deviceID)
       └─ TxPipeline: HSET + Expire(connKeyTTL=2h)
     失败：log warn but continue（退化为本 pod only，客户端仍可收消息）

  8. go runHeartbeat(ctx, conn, channelSt, log)
       └─ ticker 15s → pong diff

  9. readPump(conn)
       └─ for ReadMessage: dispatch by frame.Type
       └─ 阻塞直到 ws 关闭 / ReadDeadline 超时

  10.（退出）
     conn.Close()                         关 send chan
     hub.Deregister(conn)                 从 conns 移除
     routing.Deregister(bgCtx, uid, devID) HDEL Redis key
```

### 3.3 关键点

- **Auth 靠 JWT query 参数**。因为 WS Upgrade 过程不方便带 Header（浏览器 WebSocket API 限制），换成 token 放 URL
- **Read/Write deadline = 45s**（`pongTimeout`）。客户端 15s 没任何数据 → 服务端主动关
- **每个 conn 有独立 writePump goroutine**，业务代码只 push 到 `conn.send` channel（buffered 256）；slow consumer 时 send 满直接 drop 帧 + 关 conn（`conn.go:114-120`）
- **Register/Deregister 是对称的**，但 Pod hard kill 时 Deregister 执行不到 → 靠 Redis TTL 45s 自动过期

---

## 4. 心跳 ping-pong 与旧消息发现机制

这是 im "不丢消息" 的关键机制之一。

### 4.1 两条独立 ticker

```
客户端 → 服务端 ping   每 15s（客户端驱动，携带本地 seq 游标）
服务端 → 客户端 pong   每 15s（服务端 runHeartbeat 驱动，携带 diff）
```

**两条 ticker 互相独立**，并不是"收到 ping 才发 pong"。这个设计让两侧都能主动告警。

### 4.2 ping 的 payload

```json
{
  "type": "ping",
  "payload": {
    "channel_seqs": {
      "42": 130,
      "88": 5,
      "99": 180
    }
  }
}
```

`channel_seqs` = 客户端本地每个**已打开**频道的最大 seq。不需要包含所有加入的频道（未打开的频道不在内存里）。

### 4.3 服务端收到 ping 做什么（`ws_handler.go:162-188`）

```
switch frame.Type == TypePing:
  1. 解析 ping.channel_seqs，逐个 conn.UpdateKnownSeq(chID, seq)
     意义：客户端告诉服务端"我已经看到了这些 seq"
          → 下次 pong diff 就不再把这些 seq 当成"漏送"

  2. conn.lastPong = time.Now()
     意义：视 ping 为存活证明，刷新读截止

  3. go routing.Refresh(uid, gwID, deviceID)
     Lua: HSET im-new:routing:user:{uid} deviceID gwID + EXPIRE 45s
     意义：用一次 EVAL 原子做 HSET+EXPIRE，避免 key 在两个命令之间过期
     注意：异步 goroutine，不阻塞 readPump（Redis 抖动也不影响 WS 消费）
```

### 4.4 服务端主动发 pong 做什么（`heartbeat.go:22-64`）

```
runHeartbeat(ctx, conn, channelSt, log):
  ticker := 15s
  for {
    <-ticker.C
    serverSeqs := channelSt.GetMemberChannelSeqs(uid)
      // SELECT c.id, c.last_seq FROM channels c
      // JOIN channel_members m ON m.channel_id=c.id AND m.user_id=?
    diff := {}
    for chID, srvSeq in serverSeqs {
      if srvSeq > conn.KnownSeqFor(chID) {
        diff[chID] = srvSeq
      }
    }
    conn.Push(TypePong, {server_time: now_ms, channel_seqs: diff})
    如果 Push 返 false（send buffer 满）→ ws.Close() 主动断开
  }
```

### 4.5 客户端如何用 pong

```
onPong(pong):
  if pong.channel_seqs 为空 → 空闲，什么都不做
  else → 对这批 chID 调 POST /api/sync 拉缺失消息
         (也可以批量：一次带上所有告警 chID 的本地 seq 游标)
```

### 4.6 为什么不在 pong 里直接塞消息

1. **放大系数**：Conn 数 × 15s = 每分钟 4 次 × 3 pod × 5 万 conn = 百万级 pong/分钟。pong 必须轻量
2. **可达性**：pong 是 UDP 化的信号（best-effort），真正的消息必须走可重传的 HTTP
3. **分层职责**：pong = 告警；sync = 拉取。两个职责混在一起不好扩展（想加压缩 / 缓存 / CDN 时会卡住）

### 4.7 `conn.knownSeq` 的生命周期

| 时机 | 动作 |
|------|------|
| conn 创建时 | `knownSeq = {}`（完全空白） |
| 客户端 ping 带 channel_seqs | `UpdateKnownSeq(chID, seq)`（声明已看到） |
| 服务端推送成功 ACK 后 | `UpdateKnownSeq(chID, seq)`（推过即视为已看到） |
| WS send 路径自己发消息 | `UpdateKnownSeq(chID, seq)`（自发自看） |
| 断连 | 整个 conn 销毁，knownSeq 没了 |

**重要**：knownSeq **不持久化**。重连后是空的。这意味着客户端**必须**在重连后立即发一次 sync（或 WS sync 帧）来建立基准，否则第一次 pong 会把整个频道的所有 seq 全部 diff 出来，客户端会懵。

### 4.8 可视化

→ 见 [`diagrams/02-ping-pong-seq.mmd`](./diagrams/02-ping-pong-seq.mmd)

---

## 5. 消息发送（HTTP / WS 两路径）

### 5.1 HTTP 路径：`POST /api/channels/:id/messages`

主流路径（大部分客户端用这个）。详见 [`diagrams/03-send-message-full.mmd`](./diagrams/03-send-message-full.mmd)。

**http/message.go:111-167** 的执行：

```
1. 鉴权 → uid
2. 解析 sendMessageReq{content, client_msg_id, msg_type, visible_to, reply_to, file_ids}
3. content == "" → 422

4. svc.SendMessage(ctx, SendParams{...})
     ├─ channels.GetMember(chID, uid) → 403 ErrNotMember
     ├─ messages.Send(ctx, msg):
     │    Begin Tx
     │      ├─ 幂等：SELECT id,seq WHERE channel_id=? AND client_msg_id=?
     │      │        命中 → 复用，直接返回
     │      ├─ AllocSeqAndInsert:
     │      │    ├─ UPDATE channels SET last_seq=last_seq+1 WHERE id=?
     │      │    │          RETURNING last_seq   ← 行锁保证并发原子
     │      │    └─ INSERT INTO messages (channel_id, seq, sender_id, content, ...)
     │      └─ 如果 visible_to 非空（directed 消息）:
     │           IncrementPhantomCount(chID, visible_to ∪ {sender})
     │           UPDATE channel_members SET phantom_count += 1
     │             WHERE channel_id=? AND user_id NOT IN (...)
     │    Commit Tx
     └─ 如果 file_ids 非空且 fileRepo 配置：
          for fid: AttachToMessage(msg.ID, fid)   // 失败 log+skip，不致命

5. 201 返回 msg 给发送方（含 seq / id）
6. 非阻塞 go pushToMembers(ctx=background, svc, pusher, msg, log):
     ├─ members := svc.ListMembers(chID)
     └─ for m in members:
          pushMsg := (visible_to 非空 且 m 不在 visible_to) ? phantom : msg
          pusher.PushMessage(m.UserID, pushMsg)   // → hubMessagePusher → CrossPodPush
```

### 5.2 WS 路径：`{"type":"send", ...}`

节点：客户端想省一次 HTTP handshake，直接复用已开的 WS 通道发。`ws_handler.go:224-325`：

```
1. 解析 SendPayload{client_msg_id, channel_id, content, msg_type, visible_to}
2. ValidateField: channel_id != 0 && content != ""
3. members.GetMember(chID, uid) → 鉴权
4. 构造 repo.Message，msgStore.Send(ctx, msg)  ← 和 HTTP 路径共用 repo.Send
5. conn.Push(TypeSendACK, {client_msg_id, server_msg_id, seq, channel_id})
6. conn.UpdateKnownSeq(chID, seq)
7. 非阻塞 go fanout 给所有成员 hub.PushToUser(m.UserID, TypePushMsg, payload)
```

### 5.3 两路径的关键差异

| 维度 | HTTP 路径 | WS 路径 |
|------|----------|---------|
| 幂等 | `client_msg_id` 短路复用 | 同上（走同一 repo.Send） |
| seq 分配 | `AllocSeqAndInsert` | 同上 |
| 返回发送方 | HTTP 201 body | WS send_ack frame |
| **跨 pod 扇出** | **走 `CrossPodPush`（完整）** | **只本 pod `hub.PushToUser`（缺跨 pod）** |
| 追踪 Span 根节点 | otelgin middleware | `wsTracer.Start("ws.send")` |

**所以**：WS send 路径目前**不完整**。跨 pod 的其他成员收不到实时推送，必须靠 pong diff + sync 兜底。如果做性能测试发现"WS send 跨 pod 延迟大到秒级"这里是根因。修复思路：把 fanout 换成 `xpod.dispatch` 而不是 `hub.PushToUser`。

---

## 6. 跨 pod 推送全链路

已在 §2 和 §5 讲到片段，这里把"从 HTTP handler 到对端客户端接到消息"的完整时间轴拼起来。

详见 [`diagrams/01-cross-pod-push.mmd`](./diagrams/01-cross-pod-push.mmd)。

### 6.1 发送侧：`Hub.CrossPodBroadcast`（`gateway/cross_pod_push.go`）

**核心接口是批量版**。单 user 的 `CrossPodPush` 是 `CrossPodBroadcast([]int64{uid})` 的薄封装。

```go
func (h *Hub) CrossPodBroadcast(
    ctx, userIDs []int64, partitionKey string,
    msgType, payload, routing, cache, gatewayID, env, log,
):
  // Step 1: 一次 json.Marshal 给所有接收者共享
  rawPayload := json.Marshal(payload)

  // Step 2: 本 pod hit 的直接用 PushRaw 零拷贝推送
  remote := []
  for uid in userIDs:
    if hub.PushRawToUser(uid, msgType, rawPayload) == 0:
      remote.append(uid)
  if empty(remote) { return }

  // Step 3: 一次 Redis Pipeline 查所有远端 gatewayID
  gwMap, err := routing.LookupBatch(ctx, remote)

  // Step 4: 按目的 gatewayID 聚合 bucket
  buckets := bucketByGateway(gwMap, gatewayID)  // map[gwID][]uid, 跳过自己

  // Step 5: 每个目标 pod 一次 Pulsar Send（带 TargetUIDs 列表）
  for gwID, uids in buckets:
    envelope := PulsarPushEnvelope{
        TargetUIDs: uids,
        MsgType:    msgType,
        Payload:    rawPayload,
    }
    producer.Send(topic=PushTopicFor(gwID, env),
                  key=partitionKey, envelope)
```

**为什么 partitionKey 传进来**：
- 按频道广播（push_msg / msg_updated / system_msg）→ `partitionKey = channelID` 保证同频道消息有序
- 按用户单播（read_sync / friend_event）→ `partitionKey = userID`

**为什么 envelope 里带 TargetUIDs**：跨 pod 消息必须自带接收者身份 —— 消费端不能从 Pulsar msg.Key 反推（Key 是 partition 路由用的，且 batching 下不是 userID）。`TargetUIDs []int64` 让一次 Send 带 N 个目标。

### 6.2 Producer 缓存（`producer_cache.go`）

- LRU 256 条目，key = topic，value = `*Producer`
- Miss 时 `createMu.Lock()` 串行化，避免并发为同一 topic 建两个 producer（重复链接）
- 淘汰（onEvict）时 `producer.Close()` + 递减 `im.pulsar.producer.active` 指标

### 6.3 Topic 命名（`topic.go`）

```
prod  → persistent://im/push/msg.push.{gwID}
pre   → persistent://im/push-pre/msg.push.{gwID}
other → persistent://im/push-local/msg.push.{gwID}.{USER或HOSTNAME或"anon"}
```

开发环境的 `.{USER}` 后缀是防窜台的关键 —— 两个开发者都连同一个 Pulsar 集群调试时不会互收消息。

### 6.4 接收侧：`PushConsumer.Handle`（`push_consumer.go`）

```go
func (pc *PushConsumer) Handle(ctx, data):
  env := json.Unmarshal(data → PulsarPushEnvelope)
  pushID := extractPushID(env.Payload)  // peek push_msg's push_id, 非 push_msg 返空
  for uid in env.TargetUIDs:
    conns := pc.hub.ConnsForUser(uid)
    if empty(conns) { continue }        // stale routing — 本 pod 不持有该 user

    // push_msg 走 ACK 跟踪 + 重试；其他类型 fire-and-forget
    if msgType == TypePushMsg && pushID != "":
      deliverWithRetry(pushID, env.Payload, conns)
    else:
      for c in conns: c.PushRaw(env.MsgType, env.Payload)
  return nil  // 总是 ACK Pulsar，丢失由 pong diff 兜底
```

**两个关键变化**（本次重构修的历史 bug）：

1. **envelope 自带 TargetUIDs** —— 以前 Consumer 硬读 `event.TargetUID`，但生产端发送的 `PushMsgPayload` 根本没这个字段，导致 TargetUID=0 → `ConnsForUser(0)` 返空 → 消息静默丢失。现在改用 `PulsarPushEnvelope.TargetUIDs` 显式携带。
2. **Consumer 不再硬编码 TypePushMsg** —— `env.MsgType` 决定 WS frame type，`read_sync / friend_event / msg_updated / system msg` 全部跨 pod 可达。

### 6.5 客户端 ACK 机制

```
服务端推出去：
  ackCh := globalACKRegistry.await(pushID)
  for c in conns: c.Push(TypePushMsg, payload)
  select:
    case <-ackCh:  推送成功
    case <-time.After(3s):  超时，重试 1 次
    case <-ctx.Done(): 退出

客户端收到 push_msg:
  处理消息 → 立即回 {type:"push_ack", payload:{push_id}}

服务端收到 push_ack（ws_handler.go:199-219）:
  globalACKRegistry.resolve(pushID) → close(ackCh)
```

`globalACKRegistry` 是进程级单例，map 管理 pending 等待 channel。

---

## 7. 已读标记与多端 read_sync

### 7.1 标记已读 `POST /api/channels/:id/read`

`service/message.go:187-202`：

```
MarkRead(chID, uid):
  1. GetMember(chID, uid) → 403 if not member
  2. ch = GetByID(chID) → 拿当前 last_seq
  3. MarkRead(chID, uid, ch.last_seq):
       UPDATE channel_members SET last_read_seq = ch.last_seq
       WHERE channel_id=? AND user_id=?
  4. return seq
```

### 7.2 多端 read_sync 扇出

Handler 层拿到 seq 后立即：

```
opts.ReadSyncer.PushReadSync(uid, chID, seq)
  → hubReadSyncer.PushReadSync → xpod.dispatch(uid, TypeReadSync, payload)
    → Hub.CrossPodPush(uid, ...) → 命中/routing/Pulsar 三路径
```

**注意 target = uid 本人**，不是频道其他成员。目的是让 user 的其他设备也知道已读进度。

### 7.3 客户端识别自己

`ReadSyncPayload{ChannelID, ReadSeq}` 不带 deviceID，所以每台设备收到 read_sync 都会:

- 如果本地 last_read_seq >= payload.ReadSeq → 忽略（自己就是发起者或更新的）
- 否则 → 把本地 last_read_seq 前移到 payload.ReadSeq，未读数归零

### 7.4 可视化

→ 见 [`diagrams/04-mark-read-multi-device.mmd`](./diagrams/04-mark-read-multi-device.mmd)

### 7.5 unread 的计算口径（在 sync 里）

```
unread = (server_seq - last_read_seq) - (phantom_count - phantom_at_read)
         ↑ 总未读           ↑ 其中对我不可见的 phantom 占位
```

`phantom_at_read` 是上次 MarkRead 时的 phantom_count 快照（在 `MarkRead` 内部同步更新），这样 unread 能在 directed 消息场景正确。

---

## 8. 消息编辑 / 撤回（msg_updated / msg_deleted）

### 8.1 PATCH `/api/messages/:id`（编辑）

`repo/message.go:165-193` → `service/message.go:353-361` → `http/message.go:359-400`：

```
1. ShouldBindJSON → editMessageReq{content}
   content == "" → 422

2. svc.EditMessage(msgID, uid, content):
     repo.UpdateContent(msgID, uid, content):
       existing := GetByID(msgID)       // 404 if missing
       if existing.sender != uid → 403 ErrForbidden
       if existing.deleted → 410 ErrGone
       UPDATE messages SET content=?, updated_at=now()
         WHERE id=? AND sender_id=? AND deleted=false
       existing.Content, existing.UpdatedAt = 更新
       return existing

3. broadcaster.BroadcastToMembers(chID, EventMsgUpdated, msg):
     members := svc.ListMembers(chID)
     for m: xpod.dispatch(m.UserID, "msg_updated", msg)
```

### 8.2 DELETE `/api/messages/:id`（撤回 = 软删）

同形，但 UPDATE `deleted=true, deleted_at=now()`。已删再删 → `ErrGone` → 200 `{ok:true, already_deleted:true}`，跳过 broadcast。

### 8.3 为什么不分配新 seq

- msg_updated/deleted 是**事件**不是新消息，客户端收到后覆盖本地那条 seq 的状态即可
- 分配新 seq 会导致 sync 返回的 messages 数组里出现"同一个业务消息多条记录"，客户端去重复杂
- 离线客户端重连后走 sync，直接拿 `messages` 表里**最新的 content / deleted 标志**，天然对齐

### 8.4 大频道的扇出热点

`BroadcastToMembers` 对每个成员单独 `xpod.dispatch`。频道 500 人 → 500 次 `Hub.CrossPodPush` → 500 次 routing.Lookup。

缓解方案（未实现，留作以后性能优化）：
- 按 gwID 聚合一次投递（一个 Pulsar 消息带 `TargetUIDs: [...]`）
- 或者对 `routing.Lookup` 做批量 `HMGET`

---

## 9. 重连与增量同步 POST /api/sync

最关键的"补齐"接口。详见 [`diagrams/05-reconnect-sync.mmd`](./diagrams/05-reconnect-sync.mmd)。

### 9.1 请求

```json
POST /api/sync
{
  "channels": [
    {"id": 42, "seq": 130},
    {"id": 88, "seq": 5},
    {"id": 99, "seq": 180}
  ]
}
```

限制：`len(channels) <= 500`（`MaxChannelsPerCall`）；超了 400。

### 9.2 算法（`service/sync.go:111-172`）

```
serverSeqs := channels.GetMemberChannelSeqs(uid)
  // 一次 SQL，返回用户加入的所有频道 {chID → last_seq}

clientSeqs := {cursor.ID → cursor.Seq}    // 客户端带来的

for chID, srvSeq in serverSeqs:
  clientSeq := clientSeqs[chID]  (不存在为 0)
  if clientSeq >= srvSeq: continue    // 已对齐，跳过

  unread 计算（见 §7.5）
  gap := srvSeq - clientSeq

  switch:
    case !known (客户端没带):                        // 新频道
      msgs = 最近 50 条 (FetchForUser afterSeq=srvSeq-50)
      has_more = (srvSeq > 50)

    case gap <= 100 (SyncGapThreshold):              // 小缺口
      msgs = FetchForUser(chID, uid, afterSeq=clientSeq, limit=100)
      has_more = false

    default (gap > 100):                              // 大缺口 fast-forward
      msgs = 最近 50 条
      has_more = true   // 客户端要翻更旧靠 GET /messages?before_seq=
```

### 9.3 响应

```json
{
  "channels": [
    {"id": 42, "server_seq": 130, "unread": 0},                          // 无变化，不带 messages
    {"id": 99, "server_seq": 200, "unread": 12, "messages": [...], "has_more": false},
    {"id": 777, "server_seq": 1500, "unread": 0, "messages": [...], "has_more": true}
  ]
}
```

对齐的频道只带 id/server_seq/unread 不带 messages —— 让客户端能拿到 server_seq 做对比。

### 9.4 幂等 & Stateless

`/api/sync` 完全 stateless：
- 同一个 cursor 集合调两次返回一样（`messages` 数组确定性相同）
- 不记录客户端状态
- 不触发副作用（不推送、不改 DB）

这意味着**客户端可以指数退避重试任意次**，不担心副作用。

---

## 10. "闲时同步" —— 本项目的等价机制

### 10.1 对标物

Mattermost / Slack / 部分 IM 实现里有"每 N 分钟做一次全量拉"的轮询。im **没有独立的闲时同步**，但语义被下面机制自然覆盖：

| 场景 | 触发 | 机制 | 代价 |
|------|------|------|------|
| 客户端挂着 WS 空闲 | 15s ping tick | pong 携 diff → 有变化才 sync | 低（有差异才 HTTP） |
| WS 断开（网络抖动 / 后台挂起） | 客户端重连 | 重连成功立即 sync 建基准 | 一次集中 HTTP |
| 推送丢失（ACK 超时） | 下一轮 pong | diff 非空 → sync | 同第一行 |
| 多端读已读丢 read_sync | 下一轮 sync | unread 由 DB 算 | 幂等覆盖 |
| 应用冷启动 | 启动时 | 发 full cursor 列表的 sync | 一次集中 HTTP |

### 10.2 设计取舍

**为什么不加独立 idle poller**：

1. **pong diff 本来就按 15s 节拍运行**，多加一个 idle poller 是重复 —— 已经在做"差异检测"了
2. **差异驱动 vs 无条件拉**：idle poller 大部分时候拉空（没新消息），浪费；pong diff 保证只在 _有差异_ 时拉
3. **pong 比 idle poller 更及时**（15s vs 通常 30~60s）
4. **手机后台被 OS 挂起**时，WS 本身就断了 —— 唤醒后走重连+sync 路径，不需要 poller

### 10.3 如果将来要加

客户端层加就行（服务端 `/api/sync` 已幂等 stateless）：

```
(伪代码)
每 N 分钟：
  if 当前 WS 是否 healthy:
    继续（pong diff 已经在做了）
  else:
    调 /api/sync 全量拉
```

---

## 11. 频道创建 / 更新 / 成员变更

> **所有频道级状态变化都作为 System Message 落 seq 流**（`msg_type=4` + JSONB `props`）。对标 Mattermost 的 `post.type + post.props`。
> 这样做的核心动机：前端渲染的"已读/未读人数"分母依赖实时的成员总数，成员变化必须立即通知；而我们又不愿为每种事件占 WS 事件槽位（硬约束 16 种 V1+M2）。
> System message 复用 `push_msg` 通道 + `/api/sync` 离线兜底，一条代码路径覆盖所有场景。
> 详见 [`diagrams/06-create-group-update.mmd`](./diagrams/06-create-group-update.mmd) 和 [`diagrams/09-system-messages.mmd`](./diagrams/09-system-messages.mmd)。

### 11.0 System Message 契约

| 字段 | 值 |
|------|-----|
| `messages.msg_type` | `4` (`MsgTypeSystem`) |
| `messages.content` | 空字符串（客户端不展示 content，全部渲染逻辑读 props） |
| `messages.props` | JSONB，**必含 `sys_type` 字符串键**，否则 `ErrInvalidSystemProps` |
| `messages.sender_id` | 触发者（`actor_id` 也记在 props 里方便直读） |
| 占 `channels.last_seq` | ✅ 是，走 `AllocSeqAndInsert` 同一条 seq 链 |
| 计入"未读人数" | 客户端应 **过滤 `msg_type=4`** 不计入红点；但占 seq 用于 sync 对齐 |

五种 `sys_type`（常量在 `repo/message.go`）：

```
channel_created     {actor_id, name}               建群时写 seq=1
channel_updated     {actor_id, name, avatar_url}   改名 / 改头像
member_joined       {actor_id, target_id}          加成员（含建群初始成员）
member_removed      {actor_id, target_id}          移除成员（target_id == self 时客户端退出）
member_left         {actor_id}                     主动离开（actor_id == leaver）
```



### 11.1 创建群聊 `POST /api/channels`

```
body: {name, member_ids: [...]}

service.CreateGroup(creator, name, memberIDs):
  1. channels.Create(Type=Group, CreatorID=creator) → INSERT + ch.ID
  2. channels.AddMember(ch.ID, creator, Owner)
  3. for uid in memberIDs (跳过 creator)：
       AddMember(ch.ID, uid, Member) (失败 log+skip)
       added.append({UserID:uid})
  4. PostSystemMessage(channel_created, actor=creator, name=ch.Name)   ← seq=1
  5. for m in added:
       PostSystemMessage(member_joined, actor=creator, target=m.UserID) ← seq=2..N
  return (ch, added)

handler 拿到 ch + added 后：
  for uid in added:
    pusher.PushChannelEvent(uid, "added", ch.ID, ch.Name)  ← 让 UI 先看到频道
      → dispatch → CrossPodPush(uid, TypeChannelEvent, payload)
  return 201 ch
```

双通道并行：
- **`channel_event added`**：WS 事件，让新成员的 UI 立即出现这个频道（不然看不见）
- **system message**：落 seq 流，让后续客户端通过 `/api/sync` 也能补齐整个创建时间轴
- 两者一起保证：在线用户实时、离线用户重连后也能看到历史

### 11.2 创建 DM `POST /api/channels/dm`

```
service.CreateOrGetDM(caller, peer):
  if peer == caller → 422 ErrSelfDM
  existing := channels.FindDM(caller, peer)  ← pre-6 用反向索引 EXISTS 优化
  if existing: return (existing, false)   // 200
  else:
     ch := channels.Create(Type=DM)
     AddMember(ch.ID, caller, Member)
     AddMember(ch.ID, peer, Member)
     return (ch, true)   // 201
```

DM 不发 `channel_event` —— 两边下次 sync 就会发现新增的 channelID，或者直接下次调 `GET /api/channels` 拉最新列表。

### 11.3 改名 / 头像 `PUT /api/channels/:id`

```
service.Update(chID, caller, name, avatar):
  requireAdminOrOwner(chID, caller)
  channels.Update(chID, name, avatar)
  ch := GetByID(chID)
  PostSystemMessage(channel_updated, actor=caller, name=ch.Name, avatar_url=ch.AvatarURL)
  return ch
```

在线成员通过 `push_msg` 立即收到 `{msg_type:4, props:{sys_type:channel_updated, name, avatar_url}}` → 客户端覆盖本地 `channel.name` / `avatar`。离线用户下次 sync 时在 messages 数组里拿到同一条记录。

**不占 WS 事件类型槽位** —— 所有元数据变更统一走系统消息通道。

### 11.4 添加成员 `POST /api/channels/:id/members`

```
service.AddMember(chID, caller, newUserID):
  requireAdminOrOwner
  channels.AddMember(chID, newUserID, Member)
  PostSystemMessage(member_joined, actor=caller, target=newUserID)  ← 已读/未读分母变化
  ch := GetByID(chID)
  return ch.Name

handler:
  pusher.PushChannelEvent(newUserID, "added", chID, channelName)   ← 新成员首次看到频道
```

现有成员收到 `push_msg{msg_type:4, props:{sys_type:member_joined, target_id:newUID}}`，立即：
1. 本地成员列表 +1
2. 所有已存消息的"已读/未读人数"分母 +1 → UI 重新渲染

新成员（`target=newUserID`）通过 `channel_event added` 先拿到频道对象，再通过后续 sync 拿到历史 system messages。

### 11.5 移除成员 `DELETE /api/channels/:id/members/:uid`

```
service.RemoveMember(chID, caller, target):
  requireAdminOrOwner(chID, caller)
  target := GetMember(chID, target) → 404 if not member
  if target.Role == Owner → 403 ErrCannotRemoveOwner
  channels.WithinTx(fn):                                    ← 原子！
    PostSystemMessage(tx, member_removed, actor=caller, target=target)
    RemoveMemberTx(tx, chID, target)
  COMMIT / ROLLBACK
```

**关键顺序**：**先 PostSystemMessage 再 DELETE**，且同一事务。原因：

- `pushToMembers` 走 `ListMembers(chID)` 拿当前成员列表
- 如果先 DELETE 再写 system msg，target 已不在 members 列表 → 推送漏掉他 → 他不知道自己被踢
- 同一事务里先写 msg：DB 里看成员数 N；事务 COMMIT 前 ListMembers 快照仍含 target → 推送能送达

被踢的 user 收到 `push_msg{msg_type:4, props:{sys_type:member_removed, target_id:self}}`：
- 客户端识别 `target_id == 自己的 user_id` → UI 弹"你被移出频道"，本地删该 channel
- 已读/未读分母 -1 → 剩余成员 UI 刷新

### 11.6 自己离开 `DELETE /api/channels/:id/members/me`

```
service.LeaveChannel(chID, caller):
  m := GetMember(chID, caller) → 404 if not member
  if m.Role == Owner → 403 ErrOwnerCannotLeave
  channels.WithinTx(fn):                                    ← 原子！
    PostSystemMessage(tx, member_left, actor=caller)
    RemoveMemberTx(tx, chID, caller)
  COMMIT / ROLLBACK
```

语义和 `RemoveMember` 相同，但 `sys_type=member_left` 让客户端区分"主动退群 vs 被踢"的 UI 文案。剩余成员的成员列表 / 已读比例同步更新。

### 11.7 频道成员列表 `GET /api/channels/:id/members`

`service.ListMembers` → `channel_members` JOIN `users` 拿 username/display_name/avatar_url。

---

## 12. 好友流程：搜索 / 申请 / 接受 / 拒绝

全流程见 [`diagrams/07-friend-flow.mmd`](./diagrams/07-friend-flow.mmd)。

### 12.1 搜索用户 `GET /api/users/search?q=bob`

```
users.Search(q):
  SELECT id, username, display_name, avatar_url FROM users
  WHERE username ILIKE ? OR display_name ILIKE ?
  LIMIT 20
```

纯 DB 查，不做权限检查（所有登录用户都能搜）。大规模场景要迁 ES，但 im 当前不拥有。

### 12.2 发送申请 `POST /api/friends/request`

```
body: {addressee_id: B}

friends.SendRequest(A, B):
  INSERT friendships(requester=A, addressee=B, status=pending)
  → Postgres unique(requester, addressee) 冲突 → error code 23505
  → service 识别 isAlreadyExistsErr → 返回 ErrAlreadyExists → 409

成功：handler 推 friend_event 给 B:
  pusher.PushFriendEvent(B, "request", A)
  → dispatch(B, TypeFriendEvent, {event_type:"request", from_user_id:A})
```

Bob 收到 `{type:"friend_event", payload:{event_type:"request", from_user_id:A}}` → UI 弹"Alice 想加你为好友"。

### 12.3 接受申请 `POST /api/friends/accept`

```
body: {friendship_id: F1}

friends.AcceptRequest(F1, B):
  UPDATE friendships SET status='accepted'
  WHERE id=F1 AND addressee_id=B
  RETURNING requester_id
  (RowsAffected=0 → repo.ErrNotFound → 404)
  (返回 requester_id 让 handler 不用二次查)

成功：handler 推 friend_event 给 requester:
  pusher.PushFriendEvent(A, "accepted", B)
```

### 12.4 拒绝申请 `POST /api/friends/reject`

同 accept，UPDATE 写 `status='rejected'`，推 `friend_event{event_type:"rejected"}` 给 A。

### 12.5 拉黑 `POST /api/friends/block`

维护单向的 block 关系，阻塞后续消息 / 申请。

### 12.6 列表

- `GET /api/friends` → accepted 好友
- `GET /api/friends/pending` → 入站 pending（别人申请我）

---

## 13. Topic 子群聊 / Presence

### 13.1 Topic 子群聊（M3）

`channel_topic.go`：

- `POST /api/channels/:id/topics` 创建子话题
- `GET /api/channels/:id/topics` 列出子话题

子话题本身也是 `channels` 行，`root_id` + `root_message_id` 指回父频道。seq **独立**（子话题有自己的 last_seq）。鉴权通过根频道成员身份继承（不单独加 channel_members 行）。

### 13.2 Presence `GET /api/presence?channel_id=X`

```
service.OnlineUsersInChannel(chID, caller):
  if !GetMember(chID, caller) → 403
  members := ListMembers(chID)
  online := []
  for m in members:
    devices, err := routing.DevicesForUser(m.UserID)
    if err == nil && len(devices) > 0: online = append(online, m.UserID)
  return online
```

- **不新增状态**，直接读 Redis routing
- 简单 per-user Lookup，成员 ≤ 1000 时 <30ms
- 更大频道要换 `Pipeline` 批量 `HGETALL`，或升 V2 WS 事件 `presence_changed` 主动推

---

## 14. M2 业务功能（公告 / 紧急 / 审批 / 通知 / 定时 / 快捷回复）

所有这些都共用下面一个模板：

```
1. HTTP handler 接收
2. Service 做鉴权 + DB 写入
3. 推 WS 事件给目标 user 集合（走 CrossPodPush）
```

### 14.1 公告（M2-B）

- `POST /api/announcements` 创建
- `GET /api/channels/:id/announcements` 列表
- `GET /api/announcements/:id` 详情
- `POST /api/announcements/:id/read` 标记已读
- 事件：`announcement_posted` 推给频道成员

### 14.2 紧急（M2-D）

- `POST /api/messages/urgent` 发紧急消息
- `GET /api/messages/urgent/...` 列表
- 事件：`urgent_posted` 推给接收者

### 14.3 审批（M2-E）

- `POST /api/approvals` 创建
- `POST /api/approvals/:id/approve|reject|cancel` 动作
- `GET /api/approvals/mine|pending` 列表
- 状态机由 `repo/approval.go` 的 WHERE 守卫保证（只能从合法前置状态跳）
- 事件：`approval_updated` 推给发起人+审批人

### 14.4 通知（M2-F）

- `POST /api/notifications` 系统消息
- `GET /api/notifications/received|sent`
- 事件：`notification_received` 推给目标 user（不是频道）

### 14.5 定时消息（M2-G）

有独立后台 goroutine：

```
ScheduledWorker.Run(ctx):
  ticker 10s
  for {
    rows := svc.FetchDue(now, batch=50)  // WHERE scheduled_at<=now AND status=pending
    for r in rows: svc.Deliver(&r)       // 内部走正常 SendMessage
  }
```

**多 pod 安全**：`MarkDelivered WHERE status=pending` 做乐观锁，两 pod 抢同一行只有一个成功，另一个 `RowsAffected=0` → `ErrNotFound` → log warn skip。

### 14.6 快捷回复（M2-H）

用户级 CRUD 模板，纯 DB，无 WS 事件。

---

## 15. 其他（收藏 / 文件 / 搜索 / 设置 / 资料）

| 接口 | 作用 | 备注 |
|------|------|------|
| `POST /api/favorites/:msgID` | 收藏消息 | 无推送 |
| `GET /api/favorites` | 列表 | |
| `DELETE /api/favorites/:id` | 取消 | |
| `POST /api/files` | 文件元信息 | 实体存外部 OSS，im 只管 metadata |
| `GET /api/files/:id` | 查元信息 | |
| `GET /api/messages/:id/files` | 消息关联文件 | |
| `GET /api/search?q=` | 消息搜索 | M1 用 PG ILIKE，大规模走外部 ES |
| `GET/PUT /api/settings` | 用户偏好 | |
| `PUT /api/users/me` | 改资料 | |

---

## 16. 极端场景补发与兜底

所有已知失败场景 + 恢复路径汇总见 [`diagrams/08-edge-cases-recovery.mmd`](./diagrams/08-edge-cases-recovery.mmd)。

### 16.1 一览

| 场景 | 直接后果 | 恢复路径 |
|------|---------|---------|
| A. Pulsar `producer.Send` 失败 | 该次推送丢；routing 未摘除（骨架位） | pong diff → sync |
| B. 客户端 push_ack 超时 3s | 服务端重试 1 次；仍失败 ACK Pulsar | pong diff → sync |
| C. WS send buffer 满 | `conn.Push` 返 false，close conn | 重连后 sync |
| D. pod crash / 重启 | WS conn 全失效；Deregister 可能没执行 | Redis TTL 45s 过期 |
| E. Redis 临时失联 | `routing.Lookup` error，该次推送全丢 | pong diff → sync |
| F. `/api/sync` 超时 | 客户端指数退避重试 | sync stateless 幂等 |
| G. routing stale（user 刚切 pod） | 发到旧 pod，找不到 conn，直接 ACK Pulsar | pong diff → sync |

### 16.2 总原则

**PostgreSQL 是最终真相**。Pulsar + WS + Redis 全部只是加速/广播层：
- Pulsar 挂了 → 延迟增大，不丢数据
- Redis 挂了 → 退化为本 pod only，不丢数据
- WS 挂了 → 靠 HTTP sync，不丢数据

**心跳 ping/pong 是所有补齐路径的发令枪**。只要 WS 健康 + ping 在跑，15s 内任何丢失都会被 pong diff 发现。

---

## 17. 易踩坑列表

1. **WS send 路径当前不跨 pod**
   - `ws_handler.go:322` 写的是 `h.hub.PushToUser` 而非 `xpod.dispatch`
   - 跨 pod 成员只能靠 pong diff
   - 压测显示 WS send 跨 pod 延迟大这里是根因

2. **`conn.knownSeq` 不持久化**
   - 重连后归零；必须立即发 sync 建基准
   - 不然第一次 pong 会 diff 出整个频道的 seq，客户端懵

3. **Pulsar 消费后无脑 ACK**
   - 不管客户端是否 ACK，服务端一律 ACK Pulsar
   - 防重投风暴；客户端 ACK 丢由 pong diff 兜底

4. **routing 两个 TTL**
   - `connKeyTTL = 2h`（Register 设）
   - `RoutingTTL = 45s`（Refresh 每次心跳设）
   - Refresh 覆盖 Register 的 2h，后续都按 45s 跑
   - 读 `repo/routing.go` 时别把两者混淆

5. ~~**msg_updated/deleted 扇出是 per-member**~~ **已优化（batched）**
   - 2026-04-24 重构：routing 改用 Redis Pipeline，Pulsar 改用 gatewayID 聚合
   - 500 人群 × 3 pod：从 500 次 Redis + 500 次 Pulsar Send → **1 次 Pipeline + 3 次 Send**
   - 见 `CrossPodBroadcast` / `Routing.LookupBatch` / `PulsarPushEnvelope`

6. **`client_msg_id` 空字符串**
   - 必须 Omit，让 Postgres 应用 NULL 默认
   - 不然唯一索引把多条空串当作重复（`repo/message.go:133-135`）

7. **`context.Background()` 在 goroutine 里**
   - `pushToMembers` / `BroadcastToMembers` 内部用 background ctx
   - 因为 HTTP request context 在响应后就 cancel，异步 goroutine 需要独立生命周期
   - 这是**例外**，普通业务代码禁止 `context.Background()`（CLAUDE.md §1.4）

8. **routing 更新是 Lua 原子 HSET+EXPIRE**
   - 非原子的话 HSET 后 key 可能立即被另一个线程的 EXPIRE 清掉
   - Lua 脚本在 `repo/routing.go:30-34`

---

## 18. 每条路径的主入口文件

| 路径 | 主入口 | 关键依赖 |
|------|--------|---------|
| WS 握手 + 读 pump | `gateway/ws_handler.go:82` | hub, routing, heartbeat |
| 心跳 ping 处理 | `gateway/ws_handler.go:162` | routing.Refresh |
| 心跳 pong 发送 | `gateway/heartbeat.go:22` | channelSt.GetMemberChannelSeqs |
| seq 分配 | `repo/message.go:110` `AllocSeqAndInsert` | ChannelRepo.IncrementSeq |
| HTTP 发消息 | `http/message.go:111` | svc.SendMessage + pushToMembers |
| WS 发消息 | `gateway/ws_handler.go:224` `handleSend` | msgStore.Send + hub.PushToUser（注意不跨 pod）|
| 跨 pod 推送 | `gateway/cross_pod_push.go:46` | routing + producerCache + Pulsar |
| 接收方 | `gateway/push_consumer.go:115` `Handle` | hub.ConnsForUser + deliverWithRetry |
| 已读 | `http/message.go:220` + `service/message.go:187` | ReadSyncer |
| 编辑/撤回 | `http/message.go:359/405` + `repo/message.go:165/201` | Broadcaster |
| 同步接口 | `http/sync.go:51` + `service/sync.go:111` | channels.GetMemberChannelSeqs |
| 创建群 | `service/channel.go:70` | ChannelEventPusher |
| 添加成员 | `service/channel.go:172` | ChannelEventPusher |
| 好友 | `service/friend.go` + `http/friend.go` | FriendEventPusher |
| 定时消息 worker | `service/scheduled_worker.go:56` | scheduled.FetchDue/Deliver |

---

## 19. 配套流程图索引

| 图号 | 文件 | 主题 |
|------|------|------|
| 01 | [`diagrams/01-cross-pod-push.mmd`](./diagrams/01-cross-pod-push.mmd) | 跨 pod 推送全链路（答 §2 疑问） |
| 02 | [`diagrams/02-ping-pong-seq.mmd`](./diagrams/02-ping-pong-seq.mmd) | ping-pong 携带 seq 发现差异 |
| 03 | [`diagrams/03-send-message-full.mmd`](./diagrams/03-send-message-full.mmd) | HTTP 发消息完整路径（seq 原子+扇出） |
| 04 | [`diagrams/04-mark-read-multi-device.mmd`](./diagrams/04-mark-read-multi-device.mmd) | 已读 + 多端 read_sync |
| 05 | [`diagrams/05-reconnect-sync.mmd`](./diagrams/05-reconnect-sync.mmd) | 重连 + /api/sync 补齐 |
| 06 | [`diagrams/06-create-group-update.mmd`](./diagrams/06-create-group-update.mmd) | 建群/改群/加减成员 4 阶段 |
| 07 | [`diagrams/07-friend-flow.mmd`](./diagrams/07-friend-flow.mmd) | 搜索/申请/接受/拒绝好友 |
| 08 | [`diagrams/08-edge-cases-recovery.mmd`](./diagrams/08-edge-cases-recovery.mmd) | 极端场景补发兜底全景 |
| 09 | [`diagrams/09-system-messages.mmd`](./diagrams/09-system-messages.mmd) | System Message 总览（msg_type=4 + props JSONB） |

---

## 20. 相关文档

| 用途 | 文档 |
|------|------|
| M1–M6 契约 + seq / routing 冻结字段 | `server/docs/BACKEND.md` |
| 前端适配 + WS 事件 V1/V2 表 | `server/docs/FRONTEND.md` |
| 目录 / 文件职能地图 | `docs/ARCHITECTURE.md` |
| 里程碑 + 硬约束 | `docs/GOAL.md` |
| 当前分支 / tag / 待决分叉 | `SESSION.md` |
| HTTP ↔ WS 事件对应矩阵 | `docs/HTTP_WS_MAP.md` |
| Go 并发规范 | `~/.claude/skills/go-concurrency-patterns/SKILL.md` |
