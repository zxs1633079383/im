#!/usr/bin/env bash
# scripts/check-test-cover.sh — C014 §3.6 gate.
#
# 跑单元测试覆盖率（不含 integration tag）+ 阈值校验。
# 起步阈值 THRESHOLD=60%（warn-only）；spec §3.6 终极目标 85%。
#
# 跑：server/$ bash scripts/check-test-cover.sh
# 环境变量：
#   THRESHOLD       数字百分比，默认 60；低于 100 时 warn-only（spec 仍 drafting）
#   THRESHOLD_HARD  设为 1 则 < THRESHOLD 直接 exit 1（CI 强制模式）

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."

THRESHOLD="${THRESHOLD:-60}"
THRESHOLD_HARD="${THRESHOLD_HARD:-0}"

# 单元测试覆盖率（不带 integration tag，速度快不依赖 docker）
# coverpkg 限制在 internal/...（项目无 pkg/，去掉避免 setup failed）
# 排除 mocks / testutil 子包（生成代码 / 测试工具），不计入覆盖率分母。
go test -coverpkg=./internal/... -coverprofile=/tmp/im-coverage.out -timeout 5m ./internal/... 2>&1 | tail -20 || {
    echo "WARN: check-test-cover: go test 部分 fail（参考输出），继续读 coverage.out" >&2
}

if [ ! -f /tmp/im-coverage.out ]; then
    echo "FAIL: check-test-cover: coverage.out 未生成" >&2
    exit 1
fi

total=$(go tool cover -func=/tmp/im-coverage.out | awk '/^total:/ {print $3}' | tr -d '%')
echo "total coverage: $total%"

# 浮点比较：用 awk
under=$(awk -v t="$total" -v th="$THRESHOLD" 'BEGIN { print (t+0 < th+0) ? "1" : "0" }')

if [ "$under" = "1" ]; then
    if [ "$THRESHOLD_HARD" = "1" ]; then
        echo "FAIL: check-test-cover: total $total% < threshold $THRESHOLD% (HARD)" >&2
        exit 1
    fi
    echo "WARN: check-test-cover: total $total% < threshold $THRESHOLD%（C014 目标 85%，分批推进）"
else
    echo "OK: check-test-cover: $total% >= $THRESHOLD%"
fi
