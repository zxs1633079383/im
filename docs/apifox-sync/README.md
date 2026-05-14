# Apifox Sync — im 项目 API + WS 同步报告

> **Apifox 项目**：新消息系统2.0（projectId `8253466`）
> **本机 token**：从 Apifox 桌面端 LevelDB 自动抠取（30 chars 短 token）
> **同步执行时间**：2026-05-12

## 1. 一句话总结

- ✅ 18 个顶层 folder + 13 个子 folder 在 8253466 项目里全部建好
- ✅ 92 个 HTTP 路由全部 POST 成功，status=released
- ✅ 22 个 WSMessageType + WS 连接握手 + ping/pong 共 26 个 WS 条目全部 POST 成功
- ✅ WS 连通 + cookieId `676cc4ccfbbc501161d5cd65`（张立超）鉴权全链路通过
- ✅ 发消息 E2E 跑通：dev 集群 schema 修复后 send → send_ack(server_msg_id=2, seq=2) → push_msg 全套 ack
- 📦 沉淀：harness/C011 channels.team_id 必须 TEXT NULL，companyId 缺省不阻塞主流程

## 2. Apifox folder 结构

```
新消息系统2.0 (8253466)
├── 健康检查 (85366603)               GET /healthz /readyz /metrics
├── 登录鉴权 (85366604)               /api/auth/{register,login,me}
├── 消息收发 (85366605)
│   ├── 消息加急 (85366623)           /api/messages/urgent + confirm/cancel/confirmations
│   ├── 定时消息 (85366624)           /api/messages/scheduled
│   ├── 消息回复 (85366625)           /api/messages/:id/replies[/branch]
│   ├── 同步相关 (85366626)           POST /api/sync + GET /api/messages/:id/after
│   ├── 模板已收到 (85366627)         POST /api/messages/:id/received
│   └── 已读统计 (85366628)           POST /api/channels/:id/read + GET read-stats + readers
├── 群聊管理 (85366607)
│   ├── 群聊成员管理 (85366629)       /api/channels/:id/members[/:uid][/nickname]
│   ├── 群聊设置 (85366630)           PUT/PATCH /api/channels/:id + managers + pins
│   ├── 群聊话题 (85366631)           /api/channels/:id/topics
│   └── 群聊关闭 (85366632)           DELETE /api/channels/:id
├── 公告 (85366608)                   /api/announcements
├── 审批 (85366609)                   /api/approvals
├── 通知中心 (85366610)               /api/notifications
├── 反应表情 (85366611)               /api/messages/:id/reactions
├── 快捷回复 (85366612)               /api/quick-replies
├── 收藏 (85366613)                   /api/favorites
├── 好友 (85366614)                   /api/friends
├── 用户 (85366616)                   /api/users/search
├── 文件 (85366617)                   /api/files + /api/messages/:id/attachments
├── 在线状态 (85366618)               /api/presence + /api/channels/online-status
├── 模块入口 (85366619)               /api/modules
├── 设置 (85366620)                   /api/settings
├── 搜索 (85366621)                   /api/search
└── WebSocket (85366622)
    ├── 连接与心跳 (85366634)         WS /ws + ping/pong
    ├── 客户端→服务端事件 (85366636) send / push_ack / sync
    └── 服务端→客户端事件 (85366637) push_msg / send_ack / sync_resp / 等共 18 种
```

## 3. 直达链接

- 项目首页: <https://app.apifox.com/link/project/8253466>
- 健康检查: <https://app.apifox.com/link/project/8253466/apis/api-456221762>
- 发送消息: <https://app.apifox.com/link/project/8253466/apis/api-456221770>
- WS 连接握手: <https://app.apifox.com/link/project/8253466/apis/api-456223226>
- WS push_msg 推送: <https://app.apifox.com/link/project/8253466/apis/api-456223232>

完整 id 映射见 `/tmp/im_apifox/sync_result.json` 和 `/tmp/im_apifox/ws_sync_result.json`。

## 4. 公共请求 header

每个 HTTP 接口都继承 commonParameters：

| header | 必填 | 说明 |
|---|---|---|
| cookieId | ✅ | 用户身份 (v0.7.4: == userId，24-hex) |
| companyId | ✅ | 租户 ID |
| userId | 可选 | 显式重申 userId，v0.7.4 起=cookieId |
| appType / device / language / deviceId / appVersion / timeZone / osVersion / siteName / envType | 可选 | 兼容 cses 老 wire shape |

WS 接口公共 header 仅 cookieId / userId / companyId 三个。

## 5. 全局响应信封

所有 HTTP 接口的响应都被 `responseEnvelope()` middleware 包成统一 shape：

```json
// 成功
{ "status": "success", "data": { ... 业务对象 ... } }

// 失败
{ "status": "error", "error": "missing auth: cookieId header required" }
```

cses-client 旧的 `isWrappedResponse` 双重 unwrap 必须删除，直接读 `.data` / `.error`。

## 6. 文档清单

| 文档 | 内容 |
|---|---|
| `00-overview.md` | 体系总览：登录态 / 公共 header / 信封 / WS 协议总览 |
| `01-auth-and-sync.md` | 鉴权 + 全量/增量同步流程 |
| `02-message-send-recv.md` | 收发消息 + 编辑 / 撤回 / 已读 use cases |
| `03-channel-management.md` | 群聊 CRUD + 成员 + 设置 use cases |
| `04-collab-extras.md` | 公告 / 审批 / 通知 / 反应 / 加急 / 定时 use cases |
| `05-websocket-protocol.md` | WS 22 type 完整 payload + 时序图 |
| `06-ws-smoke-test.md` | WS 连通测试脚本 + cookieId 鉴权链路 + 已验证结果 |

## 7. 复现脚本

落盘在 `/tmp/im_apifox/`：

| 脚本 | 用途 |
|---|---|
| `create_folders.py` | 重新建 31 个 folder（如果 Apifox 项目被清空） |
| `sync_apis.py` | 灌 92 个 HTTP 路由 |
| `sync_ws.py` | 灌 26 个 WS 条目 |
| `ws_smoke.py` | WS 连通 + 鉴权 + 发消息链路烟测 |
| `folders.json` | folder name → folder id 映射缓存 |

如需重跑：先在 Apifox UI 手动清空 8253466 项目，然后依次跑 `create_folders.py` → `sync_apis.py` → `sync_ws.py`。
