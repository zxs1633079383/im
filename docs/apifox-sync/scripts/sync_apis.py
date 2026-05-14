#!/usr/bin/env python3
"""
Bulk sync all im backend HTTP routes to Apifox project 8253466.
Strategy: each route is a dict with (method, path, folder, name, desc, body, data).
Folder ids loaded from /tmp/im_apifox/folders.json.
"""
import json, urllib.request, urllib.error, sys, time

TOKEN = "poQXm7YPU0W-89JdQaaoVqxM4DXworrA"
PID = 8253466
BASE = f"https://api.apifox.com/api/v1/projects/{PID}"
HEADERS = {
    "Authorization": f"Bearer {TOKEN}",
    "X-Project-Id": str(PID),
    "Content-Type": "application/json",
}
FOLDERS = json.load(open("/tmp/im_apifox/folders.json"))
F_TOP = FOLDERS["top"]
F_SUB = FOLDERS["sub"]
def fid(name):
    if "/" in name:
        return F_SUB[name]
    return F_TOP[name]

COMMON_HEADER = [
    {"name": "appType"}, {"name": "device"}, {"name": "language"},
    {"name": "deviceId"}, {"name": "appVersion"}, {"name": "timeZone"},
    {"name": "osVersion"}, {"name": "cookieId"}, {"name": "siteName"},
    {"name": "envType"}, {"name": "userId"}, {"name": "companyId"},
]

ENVELOPE_PROPS = {
    "status": {"type": "string", "enum": ["success", "error"], "description": "成功为 success，失败为 error"},
    "data": {"type": "object", "description": "成功时携带的业务数据"},
    "error": {"type": "string", "description": "失败时携带的错误信息"},
}

def envelope_schema(data_props=None, data_required=None):
    """Wrap data in {status, data, error} envelope."""
    if data_props is None:
        data_props = {}
    data_node = {
        "type": "object",
        "x-apifox-orders": list(data_props.keys()),
        "properties": data_props,
    }
    if data_required:
        data_node["required"] = data_required
    return {
        "type": "object",
        "x-apifox-orders": ["status", "data", "error"],
        "required": ["status"],
        "properties": {
            "status": ENVELOPE_PROPS["status"],
            "data": data_node,
            "error": ENVELOPE_PROPS["error"],
        },
    }

def body_schema(props, required=None):
    return {
        "type": "object",
        "x-apifox-orders": list(props.keys()),
        "required": required or [],
        "properties": props,
    }

# Type shortcuts
S = {"type": "string"}
I = {"type": "integer"}
I64 = {"type": "integer", "format": "int64"}
B = {"type": "boolean"}
def Sd(desc): return {"type": "string", "description": desc}
def Id(desc): return {"type": "integer", "description": desc}
def I64d(desc): return {"type": "integer", "format": "int64", "description": desc}
def Bd(desc): return {"type": "boolean", "description": desc}
def Arr(item, desc=""): return {"type": "array", "items": item, "description": desc}

# ----- ROUTE TABLE -----
ROUTES = [
    # ===== 健康检查 =====
    {"m": "GET", "p": "/healthz", "f": "健康检查", "n": "健康检查", "d": "liveness 探针，永远返回 200 ok"},
    {"m": "GET", "p": "/readyz", "f": "健康检查", "n": "就绪探针", "d": "readiness 探针，永远返回 200 ok"},
    {"m": "GET", "p": "/metrics", "f": "健康检查", "n": "Prometheus metrics", "d": "Prometheus 抓取端点（im_* gauge/counter/histogram）"},

    # ===== 登录鉴权 =====
    {"m": "POST", "p": "/api/register", "f": "登录鉴权", "n": "注册 (已下线 410)", "d": "v0.7.0 起注册走 cses Java，此端点固定返回 410 Gone"},
    {"m": "POST", "p": "/api/login", "f": "登录鉴权", "n": "登录 (已下线 410)", "d": "v0.7.0 起登录走 cses Java，此端点固定返回 410 Gone"},
    {"m": "GET",  "p": "/api/me", "f": "登录鉴权", "n": "当前登录用户", "d": "通过 cookieId header 解析当前用户。v0.7.4 起 cookieId == userId",
     "data": {"user_id": Sd("mm UserID (24-hex)"), "company_id": Sd("租户 ID")}},

    # ===== 消息收发：核心 =====
    {"m": "POST", "p": "/api/channels/:id/messages", "f": "消息收发", "n": "发送消息",
     "d": "向 channel 发送一条消息。client_msg_id 用于客户端幂等去重。",
     "body": {
         "content": Sd("消息正文（msg_type=1 为文本；2/3 时通常为附件描述）"),
         "client_msg_id": Sd("客户端幂等 ID，建议 UUIDv4"),
         "msg_type": Id("1=文本 2=图片 3=文件 4=系统 99=phantom"),
         "visible_to": Arr(S, "可见用户 ID 列表（私密消息）"),
         "reply_to": I64d("被回复的 server_msg_id（可空）"),
         "file_ids": Arr(I64, "附件文件 ID 列表"),
     }, "required": ["content", "client_msg_id"],
     "data": {"server_msg_id": I64, "seq": I64, "channel_id": I64, "created_at": Sd("RFC3339")}},
    {"m": "GET",  "p": "/api/channels/:id/messages", "f": "消息收发", "n": "拉取最近消息",
     "d": "按 seq 倒序拉取 channel 最近消息。支持 ?limit=&before_seq=。",
     "data": {"messages": Arr({"type": "object"}, "Message[]")}},
    {"m": "POST", "p": "/api/channels/:id/read", "f": "消息收发/已读统计", "n": "标记 channel 已读",
     "d": "客户端读完一段消息后调用，更新 channel_members.last_read_seq。会触发 read_sync WS 推到同用户其他设备。",
     "body": {"last_read_seq": I64d("最新读到的 seq")}, "required": ["last_read_seq"]},
    {"m": "GET",  "p": "/api/channels/:id/messages/around", "f": "消息收发", "n": "围绕某 seq 取上下文消息",
     "d": "?anchor_seq=&limit=（前后各取 N 条）。用于消息跳转、引用回溯。"},
    {"m": "GET",  "p": "/api/messages/:id/readers", "f": "消息收发/已读统计", "n": "消息已读人列表",
     "d": "返回已读该消息的用户清单（按 last_read_seq >= msg.seq 计算）。",
     "data": {"readers": Arr({"type": "object"}, "ReaderInfo[]"), "total": I}},
    {"m": "GET",  "p": "/api/messages/:id/replies", "f": "消息收发/消息回复", "n": "消息回复列表",
     "d": "返回某消息的全部直接回复（按 created_at 升序）。"},
    {"m": "GET",  "p": "/api/messages/:id/replies/branch", "f": "消息收发/消息回复", "n": "消息回复支线树",
     "d": "返回某消息的全部嵌套回复（树状）。用于会话支线展开。"},
    {"m": "PATCH","p": "/api/messages/:id", "f": "消息收发", "n": "编辑消息",
     "d": "修改自己的消息正文。会触发 msg_updated WS 推到 channel 全员。",
     "body": {"content": Sd("新正文")}, "required": ["content"]},
    {"m": "DELETE","p": "/api/messages/:id", "f": "消息收发", "n": "撤回消息",
     "d": "软删除消息（deleted_at = now）。触发 msg_deleted WS。"},
    {"m": "POST", "p": "/api/messages/forward", "f": "消息收发", "n": "转发消息到多个 channel",
     "d": "把一条 message 复制到 N 个 channel（每个目标都会触发 push_msg）。",
     "body": {"message_id": I64d("源消息 ID"), "target_channel_ids": Arr(I64, "目标 channel ID 列表")},
     "required": ["message_id", "target_channel_ids"]},
    {"m": "POST", "p": "/api/messages/batch", "f": "消息收发", "n": "批量发送消息到多个 channel",
     "d": "一次性向多个 channel 各发一条相同内容的消息。",
     "body": {"channel_ids": Arr(I64), "content": S, "msg_type": I, "client_msg_id": S},
     "required": ["channel_ids", "content", "client_msg_id"]},
    {"m": "GET",  "p": "/api/messages/:id/after", "f": "消息收发/同步相关", "n": "拉取某消息之后的新消息",
     "d": "?limit= 用于客户端按 last_seq 增量拉取。"},
    {"m": "POST", "p": "/api/messages/:id/received", "f": "消息收发/模板已收到", "n": "模板已收到回执",
     "d": "客户端点击模板消息的「已收到」按钮时调用。仅用于模板类消息（msg_type=4 sys_type=template）。"},

    # 消息加急
    {"m": "POST", "p": "/api/messages/urgent", "f": "消息收发/消息加急", "n": "发送加急消息",
     "d": "标记一条新消息为加急。触发 urgent_posted WS + 客户端铃声/弹窗。",
     "body": {"channel_id": I64, "content": S, "client_msg_id": S}, "required": ["channel_id", "content", "client_msg_id"]},
    {"m": "POST", "p": "/api/messages/:id/urgent/confirm", "f": "消息收发/消息加急", "n": "确认收到加急消息",
     "d": "收件人点「我知道了」后调用，记录到 urgent_confirmations 表。"},
    {"m": "POST", "p": "/api/messages/:id/urgent/cancel", "f": "消息收发/消息加急", "n": "撤销加急",
     "d": "发件人撤销加急标记。"},
    {"m": "GET",  "p": "/api/messages/:id/urgent/confirmations", "f": "消息收发/消息加急", "n": "加急消息确认人列表",
     "d": "查询某条加急消息的全部 confirm 记录。"},

    # 定时消息
    {"m": "POST", "p": "/api/messages/scheduled", "f": "消息收发/定时消息", "n": "创建定时消息",
     "d": "scheduled_at（unix ms / RFC3339）到点由 cron job 发出。会推 schedule_created 到发件人其他设备。",
     "body": {"channel_id": I64, "content": S, "scheduled_at": Sd("RFC3339")},
     "required": ["channel_id", "content", "scheduled_at"]},
    {"m": "DELETE","p": "/api/messages/scheduled/:id", "f": "消息收发/定时消息", "n": "取消定时消息",
     "d": "未到点前可取消。会推 schedule_canceled。"},
    {"m": "GET",  "p": "/api/messages/scheduled", "f": "消息收发/定时消息", "n": "我的待发定时消息列表"},

    # 同步
    {"m": "POST", "p": "/api/sync", "f": "消息收发/同步相关", "n": "全量 / 增量同步",
     "d": "客户端重连后调用：上报本地 channel_seqs 字典，服务端只返 server>local 的 channel 差量。",
     "body": {"channels": Arr({"type": "object"}, "[{id: channel_id, seq: client_local_max_seq}]")},
     "data": {"channels": Arr({"type": "object"}, "[{id, seq, messages: [...]}]")}},
    {"m": "GET",  "p": "/api/messages/read-stats", "f": "消息收发/已读统计", "n": "批量消息已读统计",
     "d": "?ids=1,2,3 一次性返回多条消息的 read_count + total（替代 v0.7.3 之前的多次 readers 调用）。"},

    # ===== 群聊管理 =====
    {"m": "POST", "p": "/api/channels", "f": "群聊管理", "n": "创建群聊",
     "body": {"name": S, "type": Sd("public / private"), "member_ids": Arr(S)},
     "required": ["name", "type"]},
    {"m": "POST", "p": "/api/channels/dm", "f": "群聊管理", "n": "创建 / 复用 DM 私聊",
     "d": "幂等：同两个 user 之间只会有一个 DM channel。",
     "body": {"peer_user_id": S}, "required": ["peer_user_id"]},
    {"m": "GET",  "p": "/api/channels", "f": "群聊管理", "n": "我的 channel 列表",
     "d": "返回当前用户加入的全部 channel + 各自的 last_read_seq / server_seq。"},
    {"m": "GET",  "p": "/api/channels/:id", "f": "群聊管理", "n": "channel 详情"},
    {"m": "PUT",  "p": "/api/channels/:id", "f": "群聊管理/群聊设置", "n": "更新 channel 基本信息",
     "body": {"name": S, "purpose": S, "header": S}},
    {"m": "PATCH","p": "/api/channels/:id", "f": "群聊管理/群聊设置", "n": "局部更新 channel (governance)",
     "d": "v0.7.0+ 引入。可单独改 notice / purpose / orient / permission，会推 channel_info_updated。"},
    {"m": "DELETE","p": "/api/channels/:id", "f": "群聊管理/群聊关闭", "n": "解散 channel (owner)",
     "d": "owner 软删除 channel（deleted_at = now）。会推 channel_closed 到全员。"},
    {"m": "POST", "p": "/api/channels/:id/leave", "f": "群聊管理/群聊成员管理", "n": "我离开 channel",
     "d": "成员主动离群。会推 channel_member_updated change_type=leave。"},
    {"m": "POST", "p": "/api/channels/:id/members", "f": "群聊管理/群聊成员管理", "n": "添加成员",
     "body": {"user_ids": Arr(S)}, "required": ["user_ids"]},
    {"m": "DELETE","p": "/api/channels/:id/members/:user_id", "f": "群聊管理/群聊成员管理", "n": "踢出成员"},
    {"m": "GET",  "p": "/api/channels/:id/members", "f": "群聊管理/群聊成员管理", "n": "成员列表"},
    {"m": "PATCH","p": "/api/channels/:id/members/:user_id", "f": "群聊管理/群聊成员管理", "n": "更新成员属性",
     "d": "v0.7.0: is_top / notify_pref 等单字段更新。会推 channel_top_updated 等。",
     "body": {"is_top": B, "notify_pref": I}},
    {"m": "PATCH","p": "/api/channels/:id/members/:user_id/nickname", "f": "群聊管理/群聊成员管理", "n": "成员群昵称",
     "body": {"nick_name": S}, "required": ["nick_name"]},
    {"m": "POST", "p": "/api/channels/:id/managers/:user_id", "f": "群聊管理/群聊设置", "n": "设置管理员"},
    {"m": "DELETE","p": "/api/channels/:id/managers/:user_id", "f": "群聊管理/群聊设置", "n": "取消管理员"},
    {"m": "GET",  "p": "/api/channels/:id/managers", "f": "群聊管理/群聊设置", "n": "管理员列表"},
    {"m": "POST", "p": "/api/channels/:id/pins/:message_id", "f": "群聊管理/群聊设置", "n": "置顶消息"},
    {"m": "DELETE","p": "/api/channels/:id/pins/:message_id", "f": "群聊管理/群聊设置", "n": "取消置顶消息"},
    {"m": "GET",  "p": "/api/channels/:id/pins", "f": "群聊管理/群聊设置", "n": "置顶消息列表"},
    {"m": "POST", "p": "/api/channels/:id/topics", "f": "群聊管理/群聊话题", "n": "创建话题"},
    {"m": "GET",  "p": "/api/channels/:id/topics", "f": "群聊管理/群聊话题", "n": "话题列表"},

    # ===== 公告 =====
    {"m": "POST", "p": "/api/announcements", "f": "公告", "n": "创建公告",
     "body": {"channel_id": I64, "content": S, "title": S}, "required": ["channel_id", "content"]},
    {"m": "POST", "p": "/api/announcements/:id/read", "f": "公告", "n": "标记公告已读"},
    {"m": "GET",  "p": "/api/announcements/:id/acks", "f": "公告", "n": "公告已读人列表"},
    {"m": "DELETE","p": "/api/announcements/:id", "f": "公告", "n": "删除公告"},
    {"m": "GET",  "p": "/api/channels/:id/announcements", "f": "公告", "n": "channel 公告列表"},
    {"m": "GET",  "p": "/api/announcements/:id", "f": "公告", "n": "公告详情"},

    # ===== 审批 =====
    {"m": "POST", "p": "/api/approvals", "f": "审批", "n": "创建审批",
     "body": {"channel_id": I64, "title": S, "content": S, "approver_ids": Arr(S)},
     "required": ["channel_id", "title", "approver_ids"]},
    {"m": "POST", "p": "/api/approvals/:id/approve", "f": "审批", "n": "通过审批"},
    {"m": "POST", "p": "/api/approvals/:id/reject", "f": "审批", "n": "拒绝审批"},
    {"m": "POST", "p": "/api/approvals/:id/cancel", "f": "审批", "n": "撤销审批"},
    {"m": "GET",  "p": "/api/approvals/pending", "f": "审批", "n": "我待处理的审批"},
    {"m": "GET",  "p": "/api/approvals/mine", "f": "审批", "n": "我发起的审批"},
    {"m": "GET",  "p": "/api/approvals/:id", "f": "审批", "n": "审批详情"},

    # ===== 通知 =====
    {"m": "POST", "p": "/api/notifications", "f": "通知中心", "n": "发送通知",
     "body": {"to_user_ids": Arr(S), "title": S, "content": S, "biz_type": S},
     "required": ["to_user_ids", "content"]},
    {"m": "GET",  "p": "/api/notifications/received", "f": "通知中心", "n": "我收到的通知"},
    {"m": "GET",  "p": "/api/notifications/sent", "f": "通知中心", "n": "我发出的通知"},
    {"m": "POST", "p": "/api/notifications/:id/read", "f": "通知中心", "n": "标记通知已读"},

    # ===== 反应 =====
    {"m": "POST", "p": "/api/messages/:id/reactions", "f": "反应表情", "n": "添加 emoji 反应",
     "body": {"emoji": Sd("如 👍 / :+1:")}, "required": ["emoji"]},
    {"m": "DELETE","p": "/api/messages/:id/reactions/:emoji", "f": "反应表情", "n": "移除 emoji 反应"},
    {"m": "GET",  "p": "/api/messages/:id/reactions", "f": "反应表情", "n": "消息全部 emoji 反应聚合"},

    # ===== 快捷回复 =====
    {"m": "POST", "p": "/api/quick-replies", "f": "快捷回复", "n": "新增快捷回复",
     "body": {"content": S, "shortcut": S}, "required": ["content"]},
    {"m": "GET",  "p": "/api/quick-replies", "f": "快捷回复", "n": "我的快捷回复列表"},
    {"m": "PATCH","p": "/api/quick-replies/:id", "f": "快捷回复", "n": "更新快捷回复"},
    {"m": "DELETE","p": "/api/quick-replies/:id", "f": "快捷回复", "n": "删除快捷回复"},

    # ===== 收藏 =====
    {"m": "POST", "p": "/api/favorites/:message_id", "f": "收藏", "n": "收藏消息"},
    {"m": "DELETE","p": "/api/favorites/:message_id", "f": "收藏", "n": "取消收藏"},
    {"m": "GET",  "p": "/api/favorites", "f": "收藏", "n": "我的收藏列表"},

    # ===== 好友 =====
    {"m": "POST", "p": "/api/friends/request", "f": "好友", "n": "发起好友申请",
     "body": {"to_user_id": S, "message": S}, "required": ["to_user_id"]},
    {"m": "POST", "p": "/api/friends/accept", "f": "好友", "n": "接受好友申请"},
    {"m": "POST", "p": "/api/friends/reject", "f": "好友", "n": "拒绝好友申请"},
    {"m": "GET",  "p": "/api/friends", "f": "好友", "n": "好友列表"},
    {"m": "GET",  "p": "/api/friends/pending", "f": "好友", "n": "待处理好友请求"},
    {"m": "POST", "p": "/api/friends/block", "f": "好友", "n": "拉黑用户"},

    # ===== 用户 =====
    {"m": "GET",  "p": "/api/users/search", "f": "用户", "n": "搜索用户",
     "d": "?keyword= 模糊搜用户名 / 邮箱。"},

    # ===== 文件 =====
    {"m": "POST", "p": "/api/files", "f": "文件", "n": "上传文件",
     "d": "multipart/form-data。返回 file_id 供后续发送消息时引用。"},
    {"m": "GET",  "p": "/api/files/:id", "f": "文件", "n": "下载 / 获取文件元数据"},
    {"m": "GET",  "p": "/api/messages/:id/attachments", "f": "文件", "n": "消息附件列表"},

    # ===== 在线状态 =====
    {"m": "GET",  "p": "/api/presence", "f": "在线状态", "n": "查询用户在线状态",
     "d": "?user_ids=u1,u2 返回每个用户的 online / offline / last_seen。"},
    {"m": "GET",  "p": "/api/channels/online-status", "f": "在线状态", "n": "channel 在线人数",
     "d": "?channel_ids= 批量返回每个 channel 当前在线成员数。"},

    # ===== 模块入口 =====
    {"m": "GET",  "p": "/api/modules", "f": "模块入口", "n": "客户端模块入口配置",
     "d": "返回当前公司 / 站点开放的入口模块（IM / Approval / Announcement 等）。"},

    # ===== 设置 =====
    {"m": "GET",  "p": "/api/settings", "f": "设置", "n": "查询用户设置"},
    {"m": "PUT",  "p": "/api/settings", "f": "设置", "n": "更新用户设置"},

    # ===== 搜索 =====
    {"m": "GET",  "p": "/api/search", "f": "搜索", "n": "全局消息 / channel / 用户搜索",
     "d": "?q=&scope=message|channel|user&limit="},
]

# Method needs to be lowercase in payload
def build_payload(r):
    method = r["m"].lower()
    path = r["p"]
    folder_id = fid(r["f"])
    name = r["n"]
    desc = r.get("d", "")
    body_props = r.get("body")
    body_required = r.get("required", [])
    data_props = r.get("data")

    # Path params
    path_params = []
    import re
    for m in re.finditer(r":(\w+)", path):
        path_params.append({"name": m.group(1), "type": "string", "required": True, "description": ""})

    # Request body
    req_body = {
        "type": "none",
        "parameters": [],
        "jsonSchema": {"type": "object", "properties": {}},
        "mediaType": "",
        "oasExtensions": "",
    }
    if body_props and method in ("post", "put", "patch", "delete"):
        req_body = {
            "type": "application/json",
            "parameters": [],
            "jsonSchema": body_schema(body_props, body_required),
            "mediaType": "",
            "oasExtensions": "",
        }

    return {
        "name": name,
        "description": desc,
        "operationId": "",
        "type": "http",
        "method": method,
        "path": path,
        "folderId": folder_id,
        "tags": [],
        "status": "released",
        "parameters": {
            "path": path_params,
            "query": [],
            "cookie": [],
            "header": [],
        },
        "commonParameters": {
            "query": [], "body": [], "cookie": [],
            "header": COMMON_HEADER,
        },
        "auth": {},
        "requestBody": req_body,
        "responses": [
            {
                "name": "成功",
                "code": 200,
                "contentType": "json",
                "jsonSchema": envelope_schema(data_props),
                "itemSchema": {},
                "description": "",
                "mediaType": "",
                "headers": [],
                "oasExtensions": "",
            }
        ],
        "responseExamples": [], "codeSamples": [],
        "preProcessors": [], "postProcessors": [],
        "inheritPreProcessors": {}, "inheritPostProcessors": {},
        "serverId": "", "sourceUrl": "", "responsibleId": 0,
        "visibility": "INHERITED", "customApiFields": {}, "oasExtensions": "",
    }


def req(method, path, body=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, headers=HEADERS, method=method)
    try:
        with urllib.request.urlopen(r, timeout=30) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        return {"error": e.code, "body": e.read().decode("utf-8", "ignore")}


created = []
failed = []
for i, r in enumerate(ROUTES, 1):
    payload = build_payload(r)
    resp = req("POST", "/http-apis", payload)
    if "error" in resp:
        failed.append((r, resp))
        print(f"  [{i:>2}/{len(ROUTES)}] ❌ {r['m']:6} {r['p']:50} -> {resp['error']} {resp['body'][:160]}")
    else:
        aid = resp.get("data", {}).get("id")
        created.append((r, aid))
        print(f"  [{i:>2}/{len(ROUTES)}] ✅ {r['m']:6} {r['p']:50} -> api {aid}")
    # gentle throttle
    if i % 10 == 0:
        time.sleep(0.5)

print(f"\n=== Summary: {len(created)} created, {len(failed)} failed (out of {len(ROUTES)}) ===")
json.dump({
    "created": [{"path": r["p"], "method": r["m"], "id": aid} for (r, aid) in created],
    "failed": [{"path": r["p"], "method": r["m"], "error": e} for (r, e) in failed],
}, open("/tmp/im_apifox/sync_result.json", "w"), ensure_ascii=False, indent=2)
sys.exit(0 if not failed else 1)
