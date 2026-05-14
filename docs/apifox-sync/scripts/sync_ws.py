#!/usr/bin/env python3
"""Sync 22 WS message types + 1 connection endpoint to Apifox.

WS doesn't have a native Apifox type via /http-apis, so we register them as
http type with method=get and path=/ws#... convention. Name + description
clearly mark them as WS events. Schema lives in requestBody for completeness.
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
F_CONN = FOLDERS["sub"]["WebSocket/连接与心跳"]
F_C2S = FOLDERS["sub"]["WebSocket/客户端→服务端事件"]
F_S2C = FOLDERS["sub"]["WebSocket/服务端→客户端事件"]

COMMON_HEADER = [
    {"name": "cookieId"}, {"name": "userId"}, {"name": "companyId"},
]

def body_schema(props, required=None):
    return {
        "type": "object",
        "x-apifox-orders": list(props.keys()),
        "required": required or [],
        "properties": props,
    }

S = {"type": "string"}
I = {"type": "integer"}
I64 = {"type": "integer", "format": "int64"}
B = {"type": "boolean"}
def Sd(d): return {"type": "string", "description": d}
def Id(d): return {"type": "integer", "description": d}
def I64d(d): return {"type": "integer", "format": "int64", "description": d}
def Bd(d): return {"type": "boolean", "description": d}
def Arr(it, desc=""): return {"type": "array", "items": it, "description": desc}
def Obj(props, desc=""):
    return {"type": "object", "description": desc, "x-apifox-orders": list(props.keys()), "properties": props}

# WSFrame envelope: {type, payload}
def ws_frame(type_value, payload_props, payload_required=None):
    return {
        "type": "object",
        "x-apifox-orders": ["type", "payload"],
        "required": ["type", "payload"],
        "properties": {
            "type": {"type": "string", "enum": [type_value], "description": "WS 帧类型"},
            "payload": body_schema(payload_props, payload_required) if payload_props else
                       {"type": "object", "description": "无 payload"},
        },
    }

# ----- WS events -----
EVENTS = [
    # ===== 连接与心跳 =====
    {
        "folder": F_CONN, "name": "WS 连接握手 / Upgrade",
        "path": "/ws", "method": "get",
        "desc": "GET /ws 升级为 WebSocket。鉴权走 cookieId header（同 HTTP 接口）。\n"
                "连接成功后客户端立即应当发送 sync 帧上报本地 channel_seqs，\n"
                "之后保持 15s 一次 ping/pong 心跳。\n\n"
                "本地: ws://localhost:8080/ws  联调: ws://196.168.1.177:8080/ws",
        "body": None,
    },
    {
        "folder": F_CONN, "name": "ping → pong (心跳，client→server)",
        "path": "/ws#frame:ping", "method": "get",
        "desc": "客户端每 15s 发送 ping 帧。可选携带 channel_seqs，服务端在 pong 里只回 server_seq > client_seq 的 channel。",
        "frame_type": "ping",
        "payload": {
            "channel_seqs": Obj({}, "{ channel_id (string) → client_local_max_seq }，可选"),
        },
    },
    {
        "folder": F_CONN, "name": "pong (心跳应答，server→client)",
        "path": "/ws#frame:pong", "method": "get",
        "desc": "服务端响应 ping 的帧。server_time 用于客户端测延迟；channel_seqs 只包含需要追赶的 channel。",
        "frame_type": "pong",
        "payload": {
            "server_time": I64d("服务端 unix ms"),
            "channel_seqs": Obj({}, "{ channel_id (string) → server_seq }，仅含落后的"),
        },
    },

    # ===== 客户端→服务端 4 帧 =====
    {
        "folder": F_C2S, "name": "send (客户端发消息)",
        "path": "/ws#frame:send", "method": "get",
        "desc": "客户端通过 WS 直发消息（vs HTTP POST /api/channels/:id/messages，WS 路径延迟更低）。",
        "frame_type": "send",
        "payload": {
            "client_msg_id": Sd("客户端幂等 ID（UUIDv4）"),
            "channel_id": I64d("目标 channel"),
            "content": Sd("消息正文"),
            "msg_type": Id("1=text 2=image 3=file 4=system 99=phantom"),
            "visible_to": Arr(S, "可见用户 ID（私密）"),
        },
        "required": ["client_msg_id", "channel_id", "content"],
    },
    {
        "folder": F_C2S, "name": "push_ack (客户端 ACK 推送)",
        "path": "/ws#frame:push_ack", "method": "get",
        "desc": "客户端收到 push_msg 后回复 ACK，用 push_id 去重。",
        "frame_type": "push_ack",
        "payload": {"push_id": Sd("push_msg.push_id 原值")},
        "required": ["push_id"],
    },
    {
        "folder": F_C2S, "name": "sync (重连上报本地 seq)",
        "path": "/ws#frame:sync", "method": "get",
        "desc": "客户端连接 / 重连后立即发送，上报本地 channel_seqs 字典。服务端在 sync_resp 里返回需要追赶的消息。",
        "frame_type": "sync",
        "payload": {
            "channels": Arr(Obj({"id": I64, "seq": I64}), "[{id: channel_id, seq: client_local_max_seq}]"),
        },
        "required": ["channels"],
    },

    # ===== 服务端→客户端 18 帧 =====
    {
        "folder": F_S2C, "name": "push_msg (新消息推送)",
        "path": "/ws#frame:push_msg", "method": "get",
        "desc": "服务端推送新消息。客户端收到后必须回 push_ack。type 字段：'NOTICE' 表示系统消息（msg_type=4），需读 props.sys_type 分支。",
        "frame_type": "push_msg",
        "payload": {
            "push_id": Sd("幂等 ID"),
            "type": Sd("可选；'NOTICE' = 系统消息"),
            "channel_id": I64,
            "seq": I64,
            "server_msg_id": I64,
            "sender_id": Sd("mm UserID (24-hex)"),
            "content": S,
            "msg_type": Id("1=text 2=image 3=file 4=system 99=phantom"),
            "visible_to": Arr(S, "可见用户 ID 列表"),
            "props": Sd("JSONB 字符串，仅 msg_type=4 时有值"),
            "created_at": Sd("RFC3339"),
        },
    },
    {
        "folder": F_S2C, "name": "send_ack (服务端确认 send)",
        "path": "/ws#frame:send_ack", "method": "get",
        "desc": "对客户端 send 帧的确认。client_msg_id 回带方便客户端定位本地消息。",
        "frame_type": "send_ack",
        "payload": {
            "client_msg_id": S,
            "server_msg_id": I64,
            "seq": I64,
            "channel_id": I64,
        },
    },
    {
        "folder": F_S2C, "name": "sync_resp (sync 响应)",
        "path": "/ws#frame:sync_resp", "method": "get",
        "desc": "对客户端 sync 帧的响应。channels 数组里每个 entry 含 server_seq + 需要补的 messages。",
        "frame_type": "sync_resp",
        "payload": {
            "channels": Arr(Obj({
                "id": I64,
                "seq": I64,
                "messages": Arr({"type": "object"}, "Message[]"),
            })),
        },
    },
    {
        "folder": F_S2C, "name": "read_sync (同账号他设备已读同步)",
        "path": "/ws#frame:read_sync", "method": "get",
        "desc": "用户在 A 设备标记 channel 已读后，B/C 设备会收到此帧同步本地 unread 状态。",
        "frame_type": "read_sync",
        "payload": {"channel_id": I64, "read_seq": I64d("最新 last_read_seq")},
    },
    {
        "folder": F_S2C, "name": "friend_event (好友事件)",
        "path": "/ws#frame:friend_event", "method": "get",
        "desc": "好友申请 / 接受 / 拒绝事件。",
        "frame_type": "friend_event",
        "payload": {
            "event_type": Sd("request / accepted / rejected"),
            "from_user_id": Sd("触发事件的用户 mm UserID"),
        },
    },
    {
        "folder": F_S2C, "name": "channel_event (加入 channel 事件)",
        "path": "/ws#frame:channel_event", "method": "get",
        "desc": "用户被加入新 channel 时收到。客户端应主动把这个 channel 加到本地列表。",
        "frame_type": "channel_event",
        "payload": {
            "event_type": Sd("added"),
            "channel_id": I64,
            "name": Sd("channel 名"),
        },
    },
    {
        "folder": F_S2C, "name": "msg_updated (消息被编辑)",
        "path": "/ws#frame:msg_updated", "method": "get",
        "desc": "消息正文被发件人改动。客户端用 server_msg_id 定位本地消息并替换 content。",
        "frame_type": "msg_updated",
        "payload": {"server_msg_id": I64, "channel_id": I64, "content": Sd("新正文")},
    },
    {
        "folder": F_S2C, "name": "msg_deleted (消息被撤回)",
        "path": "/ws#frame:msg_deleted", "method": "get",
        "desc": "消息被发件人或管理员撤回（软删除）。客户端应隐藏或显示 '消息已撤回'。",
        "frame_type": "msg_deleted",
        "payload": {"server_msg_id": I64, "channel_id": I64},
    },
    {
        "folder": F_S2C, "name": "announcement_posted (新公告)",
        "path": "/ws#frame:announcement_posted", "method": "get",
        "desc": "channel 内新公告。客户端应弹出公告横幅。",
        "frame_type": "announcement_posted",
        "payload": {"announcement_id": I64, "channel_id": I64, "title": S, "content": S},
    },
    {
        "folder": F_S2C, "name": "urgent_posted (加急消息)",
        "path": "/ws#frame:urgent_posted", "method": "get",
        "desc": "新的加急消息到达。客户端应铃响 + 强制弹窗。",
        "frame_type": "urgent_posted",
        "payload": {"server_msg_id": I64, "channel_id": I64, "sender_id": S, "content": S},
    },
    {
        "folder": F_S2C, "name": "approval_updated (审批状态变更)",
        "path": "/ws#frame:approval_updated", "method": "get",
        "desc": "审批 create / approve / reject / cancel 都会推这条。客户端用 approval_id 拉详情。",
        "frame_type": "approval_updated",
        "payload": {
            "approval_id": I64,
            "status": Sd("pending / approved / rejected / canceled"),
            "actor_id": Sd("触发变更的用户"),
        },
    },
    {
        "folder": F_S2C, "name": "notification_received (新通知)",
        "path": "/ws#frame:notification_received", "method": "get",
        "desc": "通用通知中心新消息（业务系统调 POST /api/notifications 后触发）。",
        "frame_type": "notification_received",
        "payload": {"notification_id": I64, "title": S, "content": S, "biz_type": S},
    },
    {
        "folder": F_S2C, "name": "reaction_added (添加 emoji 反应)",
        "path": "/ws#frame:reaction_added", "method": "get",
        "desc": "他人对一条消息加了 emoji 反应。客户端用 server_msg_id + emoji 增量 +1。",
        "frame_type": "reaction_added",
        "payload": {"server_msg_id": I64, "channel_id": I64, "user_id": S, "emoji": S},
    },
    {
        "folder": F_S2C, "name": "reaction_removed (移除 emoji 反应)",
        "path": "/ws#frame:reaction_removed", "method": "get",
        "desc": "他人移除 emoji 反应。客户端做 -1 处理。",
        "frame_type": "reaction_removed",
        "payload": {"server_msg_id": I64, "channel_id": I64, "user_id": S, "emoji": S},
    },
    {
        "folder": F_S2C, "name": "channel_top_updated (channel 置顶状态)",
        "path": "/ws#frame:channel_top_updated", "method": "get",
        "desc": "用户在 A 设备置顶/取消置顶 channel，B/C 设备同步。per-user 状态。",
        "frame_type": "channel_top_updated",
        "payload": {"channel_id": I64, "is_top": B},
    },
    {
        "folder": F_S2C, "name": "channel_info_updated (channel 元信息变化)",
        "path": "/ws#frame:channel_info_updated", "method": "get",
        "desc": "channel 的 name / notice / purpose / orient / permission 变化时推到全员。",
        "frame_type": "channel_info_updated",
        "payload": {
            "channel_id": I64,
            "name": S, "notice": S, "purpose": S,
            "orient": Sd("permission orient: open / closed"),
        },
    },
    {
        "folder": F_S2C, "name": "channel_closed (channel 已解散)",
        "path": "/ws#frame:channel_closed", "method": "get",
        "desc": "owner 解散 channel（DELETE /api/channels/:id）。客户端按 deleted_at 渲染只读状态。",
        "frame_type": "channel_closed",
        "payload": {
            "channel_id": I64,
            "actor_id": Sd("解散人 mm UserID"),
            "deleted_at": Sd("RFC3339"),
        },
    },
    {
        "folder": F_S2C, "name": "channel_member_updated (成员变更)",
        "path": "/ws#frame:channel_member_updated", "method": "get",
        "desc": "change_type 区分 join / leave / kick / nickname 四种变更。members 字段为变更后的完整 roster，客户端一次性替换本地成员列表。",
        "frame_type": "channel_member_updated",
        "payload": {
            "channel_id": I64,
            "change_type": Sd("join / leave / kick / nickname"),
            "actor_id": Sd("发起人 mm UserID"),
            "target_id": Sd("被影响的成员 mm UserID"),
            "nick_name": Sd("仅 change_type=nickname 时有值"),
            "members": Arr(Obj({
                "user_id": S, "role": I,
                "nick_name": S, "is_top": B, "notify_pref": I,
            }), "变更后完整成员名单"),
        },
    },
    {
        "folder": F_S2C, "name": "schedule_created (定时消息已创建)",
        "path": "/ws#frame:schedule_created", "method": "get",
        "desc": "推到发件人其他设备，让 dialog.hasSchedulePost 同步翻 true。",
        "frame_type": "schedule_created",
        "payload": {"channel_id": I64, "scheduled_id": I64, "has_schedule_post": B},
    },
    {
        "folder": F_S2C, "name": "schedule_canceled (定时消息已取消)",
        "path": "/ws#frame:schedule_canceled", "method": "get",
        "desc": "定时消息被取消，同步 has_schedule_post = false。",
        "frame_type": "schedule_canceled",
        "payload": {"channel_id": I64, "scheduled_id": I64, "has_schedule_post": B},
    },
]


def build_payload(ev):
    is_conn = ev.get("payload") is None and ev.get("frame_type") is None
    # For frames: requestBody carries the full WSFrame envelope
    if not is_conn:
        frame_schema = ws_frame(ev["frame_type"], ev.get("payload"), ev.get("required"))
        req_body = {
            "type": "application/json",
            "parameters": [],
            "jsonSchema": frame_schema,
            "mediaType": "", "oasExtensions": "",
        }
    else:
        req_body = {
            "type": "none",
            "parameters": [],
            "jsonSchema": {"type": "object", "properties": {}},
            "mediaType": "", "oasExtensions": "",
        }

    return {
        "name": ev["name"],
        "description": ev["desc"],
        "operationId": "",
        "type": "http",
        "method": ev.get("method", "get"),
        "path": ev["path"],
        "folderId": ev["folder"],
        "tags": ["WebSocket"],
        "status": "released",
        "parameters": {"path": [], "query": [], "cookie": [], "header": []},
        "commonParameters": {
            "query": [], "body": [], "cookie": [], "header": COMMON_HEADER,
        },
        "auth": {},
        "requestBody": req_body,
        "responses": [{
            "name": "成功", "code": 200, "contentType": "json",
            "jsonSchema": {"type": "object", "description": "WS 帧无 HTTP 响应（持续帧流）"},
            "itemSchema": {}, "description": "", "mediaType": "", "headers": [], "oasExtensions": "",
        }],
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
for i, ev in enumerate(EVENTS, 1):
    payload = build_payload(ev)
    resp = req("POST", "/http-apis", payload)
    if "error" in resp:
        failed.append((ev, resp))
        print(f"  [{i:>2}/{len(EVENTS)}] ❌ {ev['name'][:50]} -> {resp['error']} {resp['body'][:160]}")
    else:
        aid = resp.get("data", {}).get("id")
        created.append((ev, aid))
        print(f"  [{i:>2}/{len(EVENTS)}] ✅ {ev['name'][:50]} -> api {aid}")
    if i % 10 == 0:
        time.sleep(0.5)

print(f"\n=== WS Summary: {len(created)} created, {len(failed)} failed ===")
json.dump({
    "created": [{"name": ev["name"], "id": aid} for (ev, aid) in created],
}, open("/tmp/im_apifox/ws_sync_result.json", "w"), ensure_ascii=False, indent=2)
sys.exit(0 if not failed else 1)
