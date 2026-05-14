#!/usr/bin/env bash
# scripts/check-route-coverage.sh — C014 §4.1 gate（C008 升级）.
#
# 每条 authed.METHOD("/path") 路由必须至少有一个 TestM4* 集成测试 mention。
# 简化覆盖率指标：tests >= routes（每路由至少 1 个测试）。
#
# 跑：server/$ bash scripts/check-route-coverage.sh
# 退出码：0 = green；非零 = 集成测试缺失。

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."

# 列所有 authed.METHOD("/path") 路由（去重）
grep -hEn 'authed\.(GET|POST|PUT|PATCH|DELETE)\("/' internal/http/*.go cmd/gateway/main.go cmd/message/main.go 2>/dev/null | \
  sed -E 's/.*authed\.(GET|POST|PUT|PATCH|DELETE)\("(\/[^"]*)".*/\1 \2/' | \
  sort -u > /tmp/im-routes.txt || true

# 列所有 TestM4* 集成测试函数
grep -rhE '^func TestM4[A-Za-z_]+' tests/integration/ --include='*.go' 2>/dev/null | \
  sed -E 's/^func (TestM4[A-Za-z_]+).*/\1/' | \
  sort -u > /tmp/im-tests.txt || true

routes=$(wc -l < /tmp/im-routes.txt | tr -d ' ')
tests=$(wc -l < /tmp/im-tests.txt | tr -d ' ')

echo "routes: $routes"
echo "tests:  $tests"

if [ "$tests" -lt "$routes" ]; then
    echo "FAIL: check-route-coverage: 集成测试 $tests < 路由数 $routes" >&2
    echo "（参考 /tmp/im-routes.txt 与 /tmp/im-tests.txt）" >&2
    exit 1
fi
echo "OK: check-route-coverage: $tests TestM4* >= $routes 路由"
