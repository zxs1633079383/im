#!/usr/bin/env bash
# run-all-dev.sh — 一键起本地 dev 服务（默认 gateway + message 两个真实进程）。
#
# 服务说明（详见 docs/对接流程/01 §0.1）：
#   gateway        :8080  HTTP+WS 业务入口，sync 业务（POST /api/sync）也活在这里
#   message         -     Pulsar consumer，处理 incoming-message → 落库 + 推送
#   sync(stub)      -     cmd/sync/main.go 16 行预留入口，跑起来立即退出
#                         默认 **不启**；用 --with-sync-stub 显式启用（仅验证编译）
#
# 两种模式：
#   aggregated  服务 stdout/stderr 合并到当前终端，按服务 ANSI 颜色 + 前缀打 tag
#               Ctrl+C 一次性结束所有子进程
#   windows     macOS 下 tmux session 多窗口（回退 Terminal.app）
#
# Usage:
#   ./scripts/run-all-dev.sh aggregated                     # gateway + message
#   ./scripts/run-all-dev.sh aggregated --with-sync-stub    # 加 sync stub
#   ./scripts/run-all-dev.sh windows
#   ./scripts/run-all-dev.sh windows    --with-sync-stub
#
# 共享前置：本脚本假设你已经在 server/ 跑过 `make migrate-up`，且仓库根的
# docker-compose（PG/Redis/Pulsar）已经 up。脚本不会替你启依赖。
#
# 调试：所有服务输出同时落到 server/logs/dev/{svc}.log，方便事后 grep。

set -uo pipefail

MODE="${1:-aggregated}"
WITH_SYNC_STUB=0
for arg in "$@"; do
  [[ "$arg" == "--with-sync-stub" ]] && WITH_SYNC_STUB=1
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
LOG_DIR="$SERVER_DIR/logs/dev"
mkdir -p "$LOG_DIR"

# 三服务共用的 dev 环境变量（与 Makefile run-dev target 对齐）
# IM_REDIS_CLUSTER=true 必须强制 override：consul-pre 配 cluster: false 但实际是
# Redis Cluster；不 override → go-redis 跑 single 模式 → 拿不到 UserData:<id> → 401。
# 详见 harness/C010 §4.6 + docs/apifox-sync/01-auth-and-sync.md §6 troubleshoot。
COMMON_ENV=(
  "IM_ENV=dev"
  "IM_CONSUL_URL=http://consul-pre.jinqidongli.com"
  "IM_CONSUL_KEY=im-go/dev/config.yaml"
  "IM_REDIS_CLUSTER=true"
  "OTEL_EXPORTER_OTLP_ENDPOINT=http://192.168.6.66:32317"
  "OTEL_EXPORTER_OTLP_INSECURE=true"
)

# 各服务专属环境 + go run 命令
gateway_cmd() {
  env "${COMMON_ENV[@]}" \
      OTEL_SERVICE_NAME=im-gateway-local \
      go run ./cmd/gateway
}
message_cmd() {
  env "${COMMON_ENV[@]}" \
      OTEL_SERVICE_NAME=im-message-local \
      go run ./cmd/message
}
sync_cmd() {
  env "${COMMON_ENV[@]}" \
      OTEL_SERVICE_NAME=im-sync-local \
      go run ./cmd/sync
}

# ----------------------------------------------------------------------------
# 模式 1：aggregated
# ----------------------------------------------------------------------------
run_aggregated() {
  cd "$SERVER_DIR"
  echo "==> aggregated mode  logs → $LOG_DIR/{gateway,message[,sync]}.log"
  if [[ $WITH_SYNC_STUB -eq 1 ]]; then
    echo "    + sync stub (cmd/sync/main.go 16 行预留入口；启动后会立即退出，仅验证 binary 可编译)"
  else
    echo "    sync 业务在 gateway 进程内（POST /api/sync），无需单独启；--with-sync-stub 可启 cmd/sync 预留入口"
  fi
  echo "    Ctrl+C 一次性结束所有子进程"
  echo

  local PIDS=()

  # 前缀 + 颜色：gateway=绿(32) message=青(36) sync=黄(33)
  # tee 落本地日志文件，同时 sed 加 ANSI tag 输出到当前窗口
  start_svc() {
    local name="$1" color="$2"; shift 2
    local cmd_fn="$1"

    # subshell 里跑：stdout+stderr → tee 日志文件 → sed 加 ANSI tag → 主窗口
    (
      "$cmd_fn" 2>&1 \
        | tee "$LOG_DIR/${name}.log" \
        | sed -u "s/^/$(printf '\033[%sm[%s]\033[0m ' "$color" "$name")/"
    ) &

    PIDS+=("$!")
    echo "  started $name pid=$!"
  }

  start_svc gateway 32 gateway_cmd
  start_svc message 36 message_cmd
  [[ $WITH_SYNC_STUB -eq 1 ]] && start_svc sync 33 sync_cmd

  cleanup() {
    echo
    echo "==> trapping signal, killing children: ${PIDS[*]}"
    # 杀进程组防止 `go run` 留下的真正 gateway/message 进程成为孤儿
    for pid in "${PIDS[@]}"; do
      pkill -P "$pid" 2>/dev/null || true
      kill "$pid" 2>/dev/null || true
    done
    wait 2>/dev/null
    echo "==> done"
    exit 0
  }
  trap cleanup INT TERM

  # 等所有子进程结束（理论上需 Ctrl+C 才会走到这里；个别 svc 自然退出不影响其余）
  wait
}

# ----------------------------------------------------------------------------
# 模式 2：windows
# ----------------------------------------------------------------------------
run_windows_tmux() {
  local sess="im-dev"
  if tmux has-session -t "$sess" 2>/dev/null; then
    echo "==> tmux session '$sess' 已存在，先 kill 重建"
    tmux kill-session -t "$sess"
  fi
  echo "==> 启 tmux session: $sess"

  tmux new-session  -d -s "$sess" -n gateway -c "$SERVER_DIR" \
    "echo '=== gateway ==='; env $(printf '%s ' "${COMMON_ENV[@]}") OTEL_SERVICE_NAME=im-gateway-local go run ./cmd/gateway 2>&1 | tee $LOG_DIR/gateway.log"
  tmux new-window   -t "$sess:" -n message -c "$SERVER_DIR" \
    "echo '=== message ==='; env $(printf '%s ' "${COMMON_ENV[@]}") OTEL_SERVICE_NAME=im-message-local go run ./cmd/message 2>&1 | tee $LOG_DIR/message.log"

  if [[ $WITH_SYNC_STUB -eq 1 ]]; then
    tmux new-window -t "$sess:" -n sync -c "$SERVER_DIR" \
      "echo '=== sync stub (会立即退出，预留入口，业务在 gateway) ==='; env $(printf '%s ' "${COMMON_ENV[@]}") OTEL_SERVICE_NAME=im-sync-local go run ./cmd/sync 2>&1 | tee $LOG_DIR/sync.log; echo; echo '(sync stub exited — 这是设计，回车关闭)'; read"
  fi

  echo
  echo "进入查看："
  echo "  tmux attach -t $sess              # 进入 session"
  if [[ $WITH_SYNC_STUB -eq 1 ]]; then
    echo "  Ctrl+B 0/1/2                       # 切 gateway/message/sync 窗口"
  else
    echo "  Ctrl+B 0/1                         # 切 gateway/message 窗口"
    echo "  （sync 业务在 gateway 进程内：POST /api/sync）"
  fi
  echo "  tmux kill-session -t $sess         # 整体停掉"
}

run_windows_terminal_app() {
  echo "==> tmux 未安装；改用 Terminal.app 开多个窗口"
  local svcs=("gateway" "message")
  [[ $WITH_SYNC_STUB -eq 1 ]] && svcs+=("sync")

  for svc in "${svcs[@]}"; do
    /usr/bin/osascript <<EOF
tell application "Terminal"
  activate
  do script "cd \"$SERVER_DIR\" && echo '=== $svc ===' && env $(printf '%s ' "${COMMON_ENV[@]}") OTEL_SERVICE_NAME=im-${svc}-local go run ./cmd/${svc} 2>&1 | tee $LOG_DIR/${svc}.log"
end tell
EOF
  done
  echo "已打开 ${#svcs[@]} 个 Terminal 窗口；关闭对应窗口或 Ctrl+C 即可停服务。"
  [[ $WITH_SYNC_STUB -ne 1 ]] && echo "（sync 业务在 gateway 进程内：POST /api/sync）"
}

run_windows() {
  cd "$SERVER_DIR"
  if command -v tmux >/dev/null 2>&1; then
    run_windows_tmux
  elif command -v osascript >/dev/null 2>&1; then
    run_windows_terminal_app
  else
    echo "❌ 既没 tmux 也没 osascript，windows 模式不可用"
    echo "   请安装 tmux：brew install tmux"
    exit 1
  fi
}

# ----------------------------------------------------------------------------
case "$MODE" in
  aggregated) run_aggregated ;;
  windows)    run_windows ;;
  *)
    cat <<EOF
Usage: $0 {aggregated|windows} [--with-sync-stub]

模式：
  aggregated   服务合并到当前终端（推荐：日常开发）
  windows      tmux 多窗口 / Terminal.app 多窗口（推荐：长时间盯单个 svc 日志）

可选参数：
  --with-sync-stub   额外启动 cmd/sync（16 行预留入口，会立即退出，仅验证编译）
                     **默认不启**——sync 业务在 gateway 进程内 (POST /api/sync)

示例：
  $0 aggregated                      # gateway + message
  $0 aggregated --with-sync-stub     # gateway + message + sync stub
  $0 windows                          # tmux 2 个窗口
  $0 windows --with-sync-stub         # tmux 3 个窗口
EOF
    exit 2
    ;;
esac
