#!/usr/bin/env python3
import json, urllib.request, urllib.error, sys

TOKEN = "poQXm7YPU0W-89JdQaaoVqxM4DXworrA"
PID = 8253466
BASE = f"https://api.apifox.com/api/v1/projects/{PID}"
HEADERS = {
    "Authorization": f"Bearer {TOKEN}",
    "X-Project-Id": str(PID),
    "Content-Type": "application/json",
}

# Top-level folders (parentId=0) and sub-folders below
TOP = [
    "健康检查",
    "登录鉴权",
    "消息收发",
    "群聊管理",
    "公告",
    "审批",
    "通知中心",
    "反应表情",
    "快捷回复",
    "收藏",
    "好友",
    "用户",
    "文件",
    "在线状态",
    "模块入口",
    "设置",
    "搜索",
    "WebSocket",
]

# Sub-folders: (parent_name, [children])
SUB = {
    "消息收发": ["消息加急", "定时消息", "消息回复", "同步相关", "模板已收到", "已读统计"],
    "群聊管理": ["群聊成员管理", "群聊设置", "群聊话题", "群聊关闭"],
    "WebSocket": ["连接与心跳", "客户端→服务端事件", "服务端→客户端事件"],
}


def req(method, path, body=None):
    url = BASE + path
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(url, data=data, headers=HEADERS, method=method)
    try:
        with urllib.request.urlopen(r, timeout=15) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        return {"error": e.code, "body": e.read().decode("utf-8", "ignore")}


def create_folder(name, parent_id=0):
    body = {
        "name": name,
        "parentId": parent_id,
        "type": "apiDetailFolder",
    }
    r = req("POST", "/api-folders", body)
    if "error" in r:
        print(f"  ❌ {name}: {r['error']} {r['body'][:200]}")
        return None
    fid = r.get("data", {}).get("id")
    print(f"  ✅ {name} -> folderId={fid}")
    return fid


# 1) Create top-level
top_ids = {}
print("=== top-level folders ===")
for n in TOP:
    fid = create_folder(n, parent_id=0)
    if fid:
        top_ids[n] = fid

# 2) Create sub-folders
print("=== sub folders ===")
sub_ids = {}
for parent, children in SUB.items():
    pid = top_ids.get(parent)
    if pid is None:
        print(f"  ⚠️ parent {parent} not found, skip")
        continue
    for ch in children:
        fid = create_folder(ch, parent_id=pid)
        if fid:
            sub_ids[f"{parent}/{ch}"] = fid

# 3) Dump map
result = {"top": top_ids, "sub": sub_ids}
with open("/tmp/im_apifox/folders.json", "w") as f:
    json.dump(result, f, ensure_ascii=False, indent=2)
print("=== written /tmp/im_apifox/folders.json ===")
print(json.dumps(result, ensure_ascii=False, indent=2))
