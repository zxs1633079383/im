#!/usr/bin/env bash
# scripts/check-id-types.sh — C014 §4.1 gate (C012 §4.1 三件套).
#
# 1. Handler 不能再用 strconv.ParseInt(c.Param("id"))
# 2. Repo struct 不能再有 `ID int64` / `ChannelID int64` / `MessageID int64` / `AnnouncementID int64`
# 3. Migration 019 必须有 ALTER COLUMN id TYPE TEXT（活体 schema 不能回退）
#
# 跑：server/$ bash scripts/check-id-types.sh
# 退出码：0 = green；非零 = 回退检出。

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."

fail=0

# Gate 1: handler ParseInt on c.Param
# 注意：grep 0 匹配时返回 1；pipeline + set -e + pipefail 会中断，所以用 || true。
matches=$(grep -rEn 'strconv\.ParseInt.*c\.Param' internal/http/ --include='*.go' 2>/dev/null | grep -v _test.go || true)
n=$(printf '%s' "$matches" | grep -c '.' || true)
if [ "$n" != "0" ]; then
    echo "FAIL: check-id-types: handler 残留 strconv.ParseInt(c.Param) $n 处:" >&2
    printf '%s\n' "$matches" >&2
    fail=1
fi

# Gate 2: repo struct ID int64
# 注意：排除 c.Set 等不相关 int64；只看 struct field 行（首列空白 + 字段名）
matches=$(grep -rEn '^[[:space:]]+(ID|ChannelID|MessageID|AnnouncementID|UserID|MemberID|ReplyToID|ParentID)[[:space:]]+int64' internal/repo/ --include='*.go' 2>/dev/null | grep -v _test.go || true)
n=$(printf '%s' "$matches" | grep -c '.' || true)
if [ "$n" != "0" ]; then
    echo "FAIL: check-id-types: repo struct ID int64 $n 处:" >&2
    printf '%s\n' "$matches" >&2
    fail=1
fi

# Gate 3: migration 019 必须包含 ALTER COLUMN id TYPE TEXT
if [ ! -f migrations/019_id_string_alter_columns.up.sql ]; then
    echo "FAIL: check-id-types: migration 019 文件丢失" >&2
    fail=1
elif ! grep -q "ALTER COLUMN id TYPE TEXT" migrations/019_id_string_alter_columns.up.sql; then
    echo "FAIL: check-id-types: migration 019 缺 ALTER COLUMN id TYPE TEXT" >&2
    fail=1
fi

if [ "$fail" = "0" ]; then
    echo "OK: check-id-types green."
fi
exit $fail
