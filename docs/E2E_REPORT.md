# im pre 集群 E2E 报告 — 2026-04-24

## 环境

| 项 | 值 |
|---|---|
| k8s context | `kubernetes-admin@kubernetes` (pre) |
| im namespace | `im-v2` |
| gateway image | `harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-2` |
| 副本 | 3/3 Running |
| PG | `postgresql-cses-pre-cnpg-rw.postgres-cses.svc`，库 `im_pre`，10 migration 已全跑 |
| Redis | `redis-cses-pre-redis-cluster-headless.redis-cses.svc`，Cluster 模式，key 前缀 `im-new:*` |
| Pulsar | tenant `im`，namespace `im/push-pre`，broker `pulsar-cses-broker.pulsar-cses.svc` |
| Grafana Dashboard | `prometheus-stack-grafana.monitoring`，uid `im-v2-main`，NodePort 30300 |

## E2E 链路闭环 (scripts/e2e-pre.mjs)

**13/13 PASS**

| 组 | 链路 | 状态 |
|---|---|---|
| setup.login | 注册/登录 alice + bob | ✅ |
| setup.ws_connected | 2 路长连 WS | ✅ |
| setup.channel | 建 group (alice owner + bob member) | ✅ |
| G1 | `POST /messages` → WS `push_msg` seq 对齐 | ✅ |
| G2 | `POST /read` 200 (read_sync 是同用户多设备事件，单设备 harness 仅验 HTTP) | ✅ |
| G3 | `DELETE /messages/:id` → WS `msg_deleted` msg_id 对齐 | ✅ |
| G4 | `PATCH /messages/:id` → WS `msg_updated` content 对齐 | ✅ |
| G5 | `POST /sync` 旧 cursor → 返回 delta | ✅ |
| G6 | `POST /sync` 最新 cursor → `channels:[]` | ✅ |
| G7 | `PUT /channels/:id` → WS `channel_event` | ✅ |
| G8 | `POST /channels/:id/members` → 201 (被加者收 channel_event) | ✅ |
| G9 | `POST /friends/request` → WS `friend_event` | ✅ |
| G10 | `GET /friends` → 200 | ✅ |

## 过程中发现并修复的缺陷

1. **push_consumer.go 订阅 topic 硬编码短字符串** → 默认落到 `persistent://public/default/*` 与发送侧 `persistent://im/push-pre/*` 不一致，跨 pod 推送全部丢失。改为走 `PushTopicFor(gatewayID, env)`。commit `e0ecb32`。
2. **deployment env 名** `IM_GATEWAY_JWT_SECRET` 与 config override 名 `IM_JWT_SECRET` 不一致，导致 JWT 加载空字符串启动失败。
3. **runAsNonRoot + distroless nonroot 符号 UID** → kubelet verify 失败。加 `runAsUser: 65532`。
4. **k8s namespace 不允许点** → `im-2.0` 改 `im-v2`（文档同步更新）。

## 清脏（teardown）

`scripts/e2e-teardown.sh` 三步：
- PG `im_pre` 库：动态 `TRUNCATE` 所有非 `schema_migrations` 表 + RESTART IDENTITY CASCADE
- Redis Cluster：按 prefix `im-new:*` 逐节点 DEL
- Pulsar `im/push-pre` namespace：删掉所有 topic

## 下一步建议

- 跑 k6 压测（`scripts/v4-load.js`，目标 150k WS + 10k msg/s）— Task 4 延展
- cses-client 切换 message.service.ts 37 个 mattermost URI 到 ImApiAdapter (Task 5 继续)
- `im.fanout.e2e.duration` 目前近似 = HTTP handler 总耗时；后续升级到"消息入 send channel 的最后时刻"更精确
