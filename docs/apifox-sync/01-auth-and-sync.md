# 01 登录鉴权 + 同步

## 1. 端点

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/register` | **410 Gone** — v0.7.0 注册下线 |
| POST | `/api/auth/login` | **410 Gone** — v0.7.0 登录下线 |
| GET | `/api/auth/me` | 返回 cookieId 当前解析到的用户 (M4: 4 字段) |
| POST | `/api/sync` | 客户端重连后的全量/增量同步 |
| GET | `/api/messages/:id/after` | 单 channel 的增量拉取 |

## 2. /api/auth/me — use case 表

| use case | 触发 | 期望返回 |
|---|---|---|
| UC-AUTH-01 | App 启动后首次 | `{status:success, data:{id,mobile,name,userName}}` |
| UC-AUTH-02 | cookieId 没在 Redis | 401 `{status:error, error:"missing auth: cookieId header required"}` |
| UC-AUTH-03 | cookieId 在 Redis 但 id 字段空 | 同 UC-AUTH-02（ResolvedUserID="" 即视为未授权） |
| UC-AUTH-04 | 短时多次调用 | LRU 30s 命中，不打 Redis（看 `im.auth.cookie_cache.hit` 指标涨） |

请求最小集：

```bash
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     -H 'companyId: 6111fb0a202d425d221c53db' \
     http://localhost:8080/api/auth/me
```

实测响应：

```json
{
  "data": {
    "id": "676cc4ccfbbc501161d5cd65",
    "mobile": "17692704771",
    "name": "张立超",
    "userName": "张立超"
  },
  "status": "success"
}
```

## 3. /api/sync — 同步语义

客户端 reconnect / 切前台后调用，上报本地 channel→seq 字典，服务端只返超前 channel 的差量。

### 请求

```json
{
  "channels": [
    { "id": 1001, "seq": 423 },
    { "id": 1002, "seq": 0 }
  ]
}
```

### 响应

```json
{
  "status": "success",
  "data": {
    "channels": [
      {
        "id": 1001,
        "seq": 425,
        "messages": [
          { "id": 88001, "seq": 424, "content": "…", "msg_type": 1, ... },
          { "id": 88002, "seq": 425, "content": "…", "msg_type": 4, "props": "{...}" }
        ]
      }
    ]
  }
}
```

> 服务端只返**有差量**的 channel：本地 seq == server seq 的 channel 不出现在响应里。客户端要按 `id` 找差量更新，缺失即视为不变。

### use case 表

| use case | 触发 | 服务端行为 |
|---|---|---|
| UC-SYNC-01 | 冷启动（全空） | 返回每个用户加入的 channel + 最近 N 条 |
| UC-SYNC-02 | 短时重连（5min 内） | 只返 > local 的 seq 差量 |
| UC-SYNC-03 | 漏帧很多 (server_seq - client_seq > 50) | 返回最近 50 条 + 提示 channel 有 hole |
| UC-SYNC-04 | 客户端 seq 超前于 server（异常） | 服务端把它当 0 → 全量补 |

## 4. /api/messages/:id/after — 单 channel 增量

适用于客户端进入某 channel 时拉补。

```bash
GET /api/messages/12345/after?channel_id=1001&limit=50
```

返回 `{status, data: {messages: [...]}`。

### use case 表

| use case | 触发 | 备注 |
|---|---|---|
| UC-AFTER-01 | 进入 channel 详情页 | limit=20，按 seq 升序 |
| UC-AFTER-02 | 上拉历史 | 把上次最后 message_id 作为 :id 起点 |
| UC-AFTER-03 | 多次拉到空数组 | 视为「到 server head」，不再调 |

## 5. WS sync 帧

WS readPump **不 dispatch** TypeSync —— sync 只走 HTTP。客户端通过 WS 发 sync 帧会被静默忽略（服务端打 debug 日志 `unhandled ws frame type`）。

## 6. cookieId 验证脚本

```bash
# 本地 dev
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     -H 'companyId: 6111fb0a202d425d221c53db' \
     http://localhost:8080/api/auth/me

# 远程联调
curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
     -H 'companyId: 6111fb0a202d425d221c53db' \
     http://196.168.1.177:8080/api/auth/me
```

如果 401，按下表 troubleshoot：

| 现象 | 根因 | 修法 |
|---|---|---|
| `missing auth: cookieId header required` | Redis 里没有 UserData:<id> | `IM_REDIS=192.168.6.66:32379 bash server/scripts/seed-mm-cookies.sh` |
| `missing auth` 但 Redis 有 key | go-redis client 跑 single 模式但 Redis 是 cluster | 重启 gateway 加 `IM_REDIS_CLUSTER=true` |
| 200 但 data.id != cookieId | 业务侧 v0.7.4 还没切完 | 检查 ResolvedUserID 优先级（UserID > ID） |
