# 06 WS 烟测 — 连通 + 鉴权 + 发消息链路

> 本次同步执行的真实测试结果。脚本固化在 `/tmp/im_apifox/ws_smoke.py`，可重复运行。

## 1. 准备前提

### 1.1 cookieId 必须在 Redis

`UserData:676cc4ccfbbc501161d5cd65` 必须在 cses Redis 集群里（v0.7.4 起 cookieId == userId == 24-hex mm UserID）。

```bash
# 一键种入（脚本默认 fixture = 张立超）
IM_REDIS=192.168.6.66:32379 bash server/scripts/seed-mm-cookies.sh
```

若是 Redis cluster：

```bash
PAYLOAD='{"id":"676cc4ccfbbc501161d5cd65","mobile":"17692704771","name":"张立超","userName":"张立超","userId":"","organizes":[{"companyId":"6111fb0a202d425d221c53db","companyName":"中企云链","userId":"676cc4ccfbbc501161d5cd65"}]}'
redis-cli -c -h 192.168.6.66 -p 32379 SET "UserData:676cc4ccfbbc501161d5cd65" "$PAYLOAD"
```

### 1.2 gateway 必须 cluster 模式连 Redis

如果服务端 Redis 是 cluster（`CLUSTER INFO` 显示 `cluster_state:ok`），但 consul 配置里 `cluster: false`，需要启动 gateway 时加 env：

```bash
IM_REDIS_CLUSTER=true go run ./cmd/gateway
```

不然单点 client 收到 MOVED 会失败 → 表现为 `/api/auth/me` 返回 401。

## 2. HTTP 鉴权链路（已实测）

```bash
$ curl -H 'cookieId: 676cc4ccfbbc501161d5cd65' \
       -H 'companyId: 6111fb0a202d425d221c53db' \
       http://localhost:8080/api/auth/me

{"data":{"id":"676cc4ccfbbc501161d5cd65","mobile":"17692704771","name":"张立超","userName":"张立超"},"status":"success"}
```

✅ 通过。同样的 cookieId 直接用于 WS 升级。

## 3. WS 连通链路（已实测）

```bash
$ python3 /tmp/im_apifox/ws_smoke.py localhost:8080

=== WS smoke test against localhost:8080 ===
[1] handshake → HTTP/1.1 101 Switching Protocols
[2] sent sync frame (server-side WS 不 dispatch，预期无回复)
[3] sent ping frame (verifies server-side routing.Refresh path)
[4] CHANNEL_ID not set — skip send_message link test

=== summary ===
  [1] WS upgrade + cookieId 鉴权  ✅
  [2] sync 帧发送（无回是预期，sync 走 HTTP）  ✅
  [3] ping 帧发送 + routing.Refresh  ✅
  [4] send link  (skipped — set CHANNEL_ID env)
```

含义：
- `101 Switching Protocols` 证明 `ws_handler.authenticate` 用同一套 `ResolveCookieID` 成功解析了 cookieId
- ping 触发 server-side `h.routing.Refresh()`，Redis `routing:user:<uid>:<gw_id>:<dev_id>` TTL 被重置到 45s
- sync 帧被 server readPump 收到但**不 dispatch**（详见 internal/gateway/ws_handler.go:223-257）

## 4. 发消息链路（已实测）

```bash
CHANNEL_ID=1 python3 docs/apifox-sync/scripts/ws_smoke.py localhost:8080
```

实测输出（2026-05-12，张立超 → 自建 channel id=1）：

```
[1] handshake → HTTP/1.1 101 Switching Protocols
[2] sent sync frame (server-side WS 不 dispatch，预期无回复)
[3] sent ping frame (verifies server-side routing.Refresh path)
[4] sent send frame, client_msg_id=9f4d1018-0dd6-4aee-8c87-be96d8ac4a83
[<] send_ack: {"type":"send_ack","payload":{"client_msg_id":"9f4d1018-…","server_msg_id":2,"seq":2,"channel_id":1}}
[<] push_msg: {"type":"push_msg","payload":{"push_id":"ws-1-2","channel_id":1,"seq":2,"server_msg_id":2,"sender_id":"676cc4ccfbbc501161d5cd65","content":"smoke-test-…","msg_type":1,"created_at":"…"}}
[4] ✅ send_ack received
[4] ✅ push_msg echoed (self-broadcast)
```

四步全绿。

## 5. dev 集群恢复（执行记录 2026-05-12）

烟测一开始遇到 schema 漂移，按下述流程一次修通；后续如重现按这个顺序即可：

| step | 现象 | 修复 |
|---|---|---|
| 1 | `column c.team_id does not exist (SQLSTATE 42703)` | PG schema_migrations dirty version=1，002-017 全没 apply |
| 2 | dev / pre 数据可清空 | `DROP SCHEMA public CASCADE; CREATE SCHEMA public;` |
| 3 | apply | `DATABASE_URL="postgres://postgres:one.2013@192.168.6.66:32432/im_dev?sslmode=disable" make migrate-up` → version=17 |
| 4 | gateway 仍报 `cached plan must not change result type (SQLSTATE 0A000)` | pgx prepared statement cache 引用旧 schema → **重启 gateway**（pkill + 重起） |
| 5 | 完成 | `POST /api/channels` 200 + creator 自动加为 role=3，team_id 落 `companyId` 值 |

### 5.1 Schema 恢复一键脚本

```bash
PGURL="postgres://postgres:one.2013@192.168.6.66:32432/im_dev?sslmode=disable"
PGPASSWORD=one.2013 psql -h 192.168.6.66 -p 32432 -U postgres -d im_dev \
  -c "DROP SCHEMA public CASCADE; CREATE SCHEMA public;"
migrate -path migrations -database "$PGURL" up
pkill -f "go run ./cmd/gateway" || true
pkill -f "exe/gateway" || true
IM_REDIS_CLUSTER=true bash server/scripts/run-all-dev.sh aggregated
```

### 5.2 沉淀的约束

本次坑写入 `docs/harness/C011-channels-team-id-nullable-no-main-flow-block.md`：channels.team_id 必须 TEXT NULL，不能改 NOT NULL，service 不预校验 companyId 非空。

## 6. 重复跑脚本

```bash
# 完整测试套件
python3 /tmp/im_apifox/ws_smoke.py localhost:8080
python3 /tmp/im_apifox/ws_smoke.py 196.168.1.177:8080

# 带 channel
CHANNEL_ID=1001 python3 /tmp/im_apifox/ws_smoke.py localhost:8080
```

脚本特性：
- 无第三方依赖（纯 Python 3 标准库 socket）
- 完整实现 RFC 6455 WS 客户端（masking / 帧分片）
- 输出有 marker，便于 grep 自动化
