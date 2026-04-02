# IM 消息可靠投递系统设计

## 概述

设计一套消息投递系统，保证在任何用户场景下不丢任何一条消息，并保持合理的性能。系统支持私聊与群聊（统一模型），群聊支持定向可见消息。

## 技术栈

- 后端: Go
- 前端: Tauri + Angular
- 数据库: PostgreSQL
- 消息队列: Apache Pulsar
- 缓存: Redis
- 搜索: Elasticsearch (通过 Debezium 同步，已有方案，不在本设计范围内)
- 部署: Kubernetes (分布式)

## 规模

- 1-50 万用户，千级别并发
- 协议: JSON + WebSocket

---

## 一、统一会话模型

私聊和群聊使用同一套数据模型，私聊视为 `member_count=2` 的 channel。

### 数据库表

```sql
-- 频道表（私聊 + 群聊统一）
CREATE TABLE channels (
    id         BIGINT PRIMARY KEY,
    type       SMALLINT NOT NULL,     -- 1=DM, 2=GROUP
    name       TEXT,                   -- DM 可为空
    seq        BIGINT DEFAULT 0,       -- 当前最新消息序列号
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 频道成员表
CREATE TABLE channel_members (
    user_id         BIGINT,
    channel_id      BIGINT,
    last_read_seq   BIGINT DEFAULT 0,    -- 用户最后已读位置
    phantom_count   BIGINT DEFAULT 0,    -- 累计收到的 phantom 数
    phantom_at_read BIGINT DEFAULT 0,    -- 上次已读时的 phantom_count 快照
    joined_at       TIMESTAMPTZ DEFAULT NOW(),
    PRIMARY KEY (user_id, channel_id)
);

-- 消息表
CREATE TABLE messages (
    id             BIGINT PRIMARY KEY,
    channel_id     BIGINT NOT NULL,
    seq            BIGINT NOT NULL,
    client_msg_id  TEXT,                  -- 客户端生成的 UUID，用于幂等去重
    sender_id      BIGINT NOT NULL,
    content        TEXT,
    visible_to     BIGINT[],              -- NULL 表示所有人可见，非 NULL 为可见用户列表
    created_at     TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE (channel_id, seq),
    UNIQUE (channel_id, client_msg_id)
);

CREATE INDEX idx_messages_channel_seq ON messages(channel_id, seq);
```

### DM 与 GROUP 的行为差异

| 行为 | DM (type=1) | GROUP (type=2) |
|---|---|---|
| 成员数 | 固定 2 人 | 2-N 人 |
| 退出 | 不可退出，只能删除会话 | 可退出 |
| 定向消息 | 不需要 | 支持 (phantom) |
| 创建方式 | 发消息时自动创建 | 手动创建 |
| 未读计算 | `channel.seq - last_read_seq` | `channel.seq - last_read_seq - (phantom_count - phantom_at_read)` |

---

## 二、序列号 (seq) 设计

### per-channel seq

每个 channel 维护独立的递增序列号。所有消息（包括定向消息）都分配 seq，保证 seq 连续。

### seq 分配

在 PG 事务中完成，保证原子性：

```sql
BEGIN;
  UPDATE channels SET seq = seq + 1 WHERE id = $channel_id RETURNING seq;
  INSERT INTO messages (channel_id, seq, sender_id, content, visible_to, client_msg_id, ...)
  VALUES ($channel_id, $seq, ...);
COMMIT;
```

- Pulsar 按 channel_id 做 partition key，同一 channel 的消息串行消费，不存在高并发抢锁
- 不同 channel 完全并行
- seq 分配和消息写入在同一事务，不会出现 seq 空洞或重复

### 为什么不用 Redis 分配 seq

Redis INCR 性能更高但有可靠性风险：崩溃/重启可能导致计数器回退，造成 seq 重复。消息写入本就要走 PG 事务，seq 放在同一事务中零额外开销，且 ACID 保证。

### Redis 的职责

- 缓存最近 N 条消息（加速拉取）
- 缓存 channel_members 状态（未读数等热数据）
- Gateway 路由表（user → gateway pod 映射）
- 推送 ACK 待确认队列

---

## 三、定向可见消息 (Phantom 方案)

### 核心机制

群聊中发送者可以指定 `visible_to` 列表。不在列表中的用户收到 phantom 占位符，只含 `{seq, type: "phantom"}`，无内容、无发送者信息。

### 推送

```
消息写入: channel_id=ch1, seq=521, visible_to=[user_A, user_B]

推送给 user_A, user_B:
  {seq:521, sender_id:xxx, content:"secret", visible_to:[A,B], ...}

推送给其他成员:
  {seq:521, type:"phantom"}
```

### 拉取

服务端拉取接口同样返回 phantom 占位：

```sql
SELECT 
  CASE WHEN visible_to IS NULL OR $user_id = ANY(visible_to)
    THEN row_to_json(m.*)
    ELSE json_build_object('seq', m.seq, 'type', 'phantom')
  END
FROM messages m
WHERE m.channel_id = $channel_id AND m.seq > $after_seq
ORDER BY m.seq
LIMIT $limit;
```

### 好处

- seq 对所有用户天然连续，空洞检测零特殊处理
- 消息只存一份，无写放大
- 分布式环境下不需要额外的计数器协调

### 安全性

用户能感知到"某个 seq 位置有一条定向消息"，但无法获知内容、发送者或可见人列表。客户端 UI 不展示任何 phantom 痕迹。

---

## 四、未读数计算

### 核心原则：减法计算，不做写放大

### 普通消息（绝大多数情况）

发消息时只更新一行：

```sql
UPDATE channels SET seq = seq + 1 WHERE id = $channel_id;
```

未读数 = `channel.seq - member.last_read_seq`

### 定向消息

额外更新不可见用户的 phantom_count：

```sql
UPDATE channel_members SET phantom_count = phantom_count + 1
WHERE channel_id = $channel_id
AND user_id != ALL($visible_users);
```

未读数 = `(channel.seq - last_read_seq) - (phantom_count - phantom_at_read)`

由于定向消息不常用，此 O(N) 更新是偶发冷路径。

### 标记已读

```sql
UPDATE channel_members
SET last_read_seq = $channel_current_seq,
    phantom_at_read = phantom_count
WHERE user_id = $user_id AND channel_id = $channel_id;
```

### 计算验证

```
初始: channel.seq=100, last_read_seq=100, phantom_count=5, phantom_at_read=5
  → unread = (100-100) - (5-5) = 0

3条普通消息: channel.seq=103
  → unread = (103-100) - (5-5) = 3

1条定向(我不可见): channel.seq=104, phantom_count=6
  → unread = (104-100) - (6-5) = 3  (phantom 不算未读)

2条普通消息: channel.seq=106
  → unread = (106-100) - (6-5) = 5

标记已读: last_read_seq=106, phantom_at_read=6
  → unread = (106-106) - (6-6) = 0

1条定向(我可见): channel.seq=107, phantom_count 不变
  → unread = (107-106) - (6-6) = 1
```

---

## 五、系统架构

### 分层

```
┌────────────────────────────────────────────────────────────┐
│                   Client (Tauri + Angular)                  │
│  WS连接层 │ 消息同步层 │ 本地SQLite存储 │ 空洞检测/渲染       │
└────────────────────────────────────────────────────────────┘
                        │ WebSocket (JSON)
                        ▼
┌────────────────────────────────────────────────────────────┐
│                  Gateway Layer (Go, 多Pod)                  │
│  WebSocket 连接管理、心跳、用户路由、消息收发中转              │
│  无业务逻辑，无状态（会话路由存 Redis）                       │
└────────────────────────────────────────────────────────────┘
        │ Pulsar                           │ Pulsar
        ▼                                  ▼
┌────────────────────┐         ┌─────────────────────────────┐
│  Message Service   │         │      Sync Service           │
│  消息持久化          │         │  增量同步、离线消息拉取        │
│  seq 分配           │         │  空洞补齐、多设备已读同步      │
│  phantom 生成       │         │                             │
│  未读数更新          │         │                             │
└────────────────────┘         └─────────────────────────────┘
        │                                  │
        ▼                                  ▼
┌────────────────────────────────────────────────────────────┐
│                     Storage Layer                          │
│        PG (持久化)  │  Redis (缓存/路由)  │  Pulsar (队列)   │
└────────────────────────────────────────────────────────────┘
```

### 消息写入路径

```
Client → Gateway → Pulsar(msg.incoming) → MessageService → PG + Redis → ACK 给发送者
```

### 消息推送路径

```
MessageService → Pulsar(msg.push.{gateway_id}) → Gateway → WebSocket → Client → ACK
```

### Gateway 路由

```
用户连接时:
  Redis HSET user:connections {user_id} → [{device_id, gateway_id}, ...]

推送时:
  1. 从 Redis 查目标用户连接的 Gateway
  2. 发到对应 Gateway 的 Pulsar topic
  3. Gateway 消费后推给对应 WebSocket 连接
```

---

## 六、三层消息可靠性保障

### 第一层：写入保障

消息一旦被服务端确认就不丢失。

- 客户端发送后等待服务端 ACK，超时重试
- 用 `client_msg_id` 幂等去重，防止重复消息
- Gateway 不做持久化，通过 Pulsar 投递给 MessageService
- MessageService 在 PG 事务中完成 seq 分配 + 消息写入

```json
// 发送
→ {type:"send", client_msg_id:"uuid-123", channel_id:"ch1", content:"hello"}

// 确认
← {type:"send_ack", client_msg_id:"uuid-123", seq:521, server_msg_id:"msg-789"}
```

### 第二层：推送保障

在线客户端尽力实时收到消息。

- 消息推送后等待客户端 ACK
- 3 秒未 ACK → 标记 pending → 5 秒后重试一次
- 仍失败 → 放弃推送，依赖第三层拉取兜底
- 不做无限重试（用户可能已断线但未感知）

### 第三层：拉取保障（最终兜底）

客户端最终一定能通过拉取获得所有消息。触发时机：

1. **重连时**：全量 sync，发送所有 channel 的 `(id, local_max_seq)`，服务端返回增量
2. **心跳 pong**：附带 channel seq 变更摘要，客户端对比发现差异后主动拉取
3. **打开 channel 时**：主动拉一次最新消息
4. **空洞检测触发**：翻页时本地数据不足，向服务端补齐

### 心跳机制

```
客户端: 每 15 秒发 ping，30 秒无 pong 认为断线 → 重连
服务端: 收到 ping 回 pong + channel seq diff，45 秒无数据 → 关闭连接

pong 附带信息:
{
  type: "pong",
  channel_seqs: {"ch_1": 525, "ch_5": 203}  // 只返回有变化的
}
```

Gateway 为每个连接维护内存中的 `known_seq` map（最后成功推送的 seq），pong 时对比生成 diff。

### 故障场景覆盖

| 场景 | 写入 | 推送 | 拉取 | 结果 |
|---|---|---|---|---|
| 正常在线 | OK | OK | 不需要 | 实时收到 |
| 网络瞬断 | OK | ACK 超时 | pong 发现 diff | 最多延迟 15 秒 |
| 进电梯 5 分钟 | OK | 连接已死 | 重连后 sync | 出电梯后几秒收到 |
| 长时间离线 | OK | 不推送 | 上线后分批 sync | 全部拉到 |
| 服务端 Pod 重启 | 已在 PG | 连接断开 | 重连到其他 Pod | 几秒恢复 |
| 服务端崩溃 | 已在 PG | 连接断开 | 重连到其他 Pod | 几秒恢复 |
| 客户端发送时断网 | 未进入系统 | - | - | 客户端标记"失败"→ 恢复后重试 |
| Pulsar 积压 | 在 Pulsar 中 | 延迟 | 消费后补推 | 延迟但不丢 |

---

## 七、客户端同步协议

### 重连同步

```
1. 客户端从本地 SQLite 计算状态:
   SELECT channel_id, MAX(seq) FROM local_messages GROUP BY channel_id

2. 发送 sync 请求:
   {type:"sync", channels: [{id:"ch1", seq:520}, {id:"ch2", seq:100}, ...]}

3. 服务端对比，返回增量:
   {
     channels: [
       {id:"ch1", server_seq:525, unread:3, messages:[...]},
       {id:"ch2", server_seq:100, unread:0},
       {id:"dm5", server_seq:10, unread:2, messages:[...]},
     ]
   }

4. 差距过大的 channel (>100条): 只返回最新 N 条 + has_more 标记
5. 新加入的 channel（客户端没有的）: 返回 channel 信息 + 最新 N 条消息
```

### 进入 channel

```
1. 查本地消息是否足够渲染
2. 不足 → 向服务端拉取: {type:"fetch", channel_id, before_seq, limit:50}
3. 标记已读 → 服务端更新 → 多设备已读同步
```

### 消息跳转（搜索/@）

```
1. 查本地是否有目标 seq 附近的消息
2. 没有 → {type:"fetch", channel_id, around_seq:50, limit:50}
3. 服务端返回 seq 25-75 的消息（含 phantom）
4. 存入本地，渲染并定位到目标 seq
5. 从此位置上下翻页时正常空洞检测
```

---

## 八、客户端空洞检测

### 核心方案：count 检测法

不维护 sync_ranges 表，直接利用 local_messages 表检测：

```sql
-- 向上翻页，需要 30 条可见消息
SELECT * FROM local_messages
WHERE channel_id = ? AND seq < ? AND visible = 1
ORDER BY seq DESC LIMIT 30;
```

不足 30 条且未到 channel 首条消息 → 存在空洞 → 向服务端拉取。

### seq 连续性检查

除了数量不足，seq 不连续也说明有洞：

```
查出 30 条: seq = [490, 489, 488, ..., 465, 430, 429, ...]
            465 和 430 之间跳了 → seq 464-431 缺失 → 空洞
取断点 seq=465，向服务端拉取 before_seq=465 的消息
```

### phantom 对空洞检测的影响

phantom 占据 seq 位，保证 seq 对所有用户连续。空洞检测逻辑无需特殊处理：

- 检测空洞: 查所有消息（含 phantom），检查 seq 连续性
- 渲染展示: 查 `visible=1` 的消息

```sql
-- 空洞检测（含 phantom）
SELECT COUNT(*) FROM local_messages
WHERE channel_id = ? AND seq BETWEEN ? AND ?;

-- 可见消息渲染
SELECT * FROM local_messages
WHERE channel_id = ? AND seq < ? AND visible = 1
ORDER BY seq DESC LIMIT 30;
```

---

## 九、多设备已读状态同步

### 已读事件传播

```
设备A 标记已读 → 服务端更新 channel_members → 推送给同用户其他设备
```

### 已读同步不需要 ACK

已读状态丢失不会造成数据问题。最坏情况：某设备显示几条多余未读，用户点进去即纠正。兜底：pong 携带的 channel 状态、打开 channel 时的主动查询。

### 多设备发消息同步

发送者的所有设备都是 channel 成员，自然收到推送：
- 发送设备: 匹配 `client_msg_id` → 将"发送中"改为"已发送"
- 其他设备: 作为新消息正常存储

---

## 十、客户端本地存储 (SQLite)

```sql
CREATE TABLE local_channels (
    id              TEXT PRIMARY KEY,
    type            INTEGER,          -- 1=DM, 2=GROUP
    name            TEXT,
    server_seq      INTEGER,          -- 服务端最新 seq
    unread_count    INTEGER DEFAULT 0,
    last_msg_preview TEXT,
    last_msg_time   INTEGER,
    updated_at      INTEGER
);

CREATE TABLE local_messages (
    channel_id  TEXT,
    seq         INTEGER,
    server_id   TEXT,
    client_id   TEXT,                  -- UUID，发送去重
    sender_id   TEXT,
    content     TEXT,
    msg_type    INTEGER,               -- 1=normal, 2=phantom
    visible     INTEGER DEFAULT 1,     -- phantom 为 0
    created_at  INTEGER,
    PRIMARY KEY (channel_id, seq)
);

CREATE TABLE local_outbox (
    client_id   TEXT PRIMARY KEY,
    channel_id  TEXT,
    content     TEXT,
    retry_count INTEGER DEFAULT 0,
    created_at  INTEGER
);

CREATE INDEX idx_msg_visible ON local_messages(channel_id, seq) WHERE visible = 1;
```
