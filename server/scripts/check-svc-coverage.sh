#!/usr/bin/env bash
# scripts/check-svc-coverage.sh — C014 §4.1 gate.
#
# 列 internal/service/*.go 中 public method（receiver *Service）vs
# 列 internal/service/*_test.go 中 Test{Service}_{Method} 单测。
# 输出缺失（diff）但 **不 fail**（spec §4.2 标注 "警告"），只是把缺测的 method
# 名打印出来；真正硬性覆盖率检查由 check-test-cover.sh 接管。
#
# 跑：server/$ bash scripts/check-svc-coverage.sh
# 退出码：恒 0（informational gate）。

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$script_dir/.."

# 列 service public method（首字母大写 receiver type + 首字母大写 method）
grep -rhE '^func \([a-z]+ \*?[A-Z][a-zA-Z]*Service\) [A-Z]' internal/service/*.go 2>/dev/null | \
  grep -v _test.go | \
  sed -E 's/^func \([a-z]+ \*?([A-Z][a-zA-Z]*Service)\) ([A-Z][a-zA-Z]*).*/\1.\2/' | \
  sort -u > /tmp/im-svc-methods.txt || true

# 列 service 单测（TestXxxService_Yyy 模式）
grep -rhE '^func Test[A-Z][a-zA-Z]*Service_' internal/service/*_test.go 2>/dev/null | \
  sed -E 's/^func Test([A-Z][a-zA-Z]*Service)_([A-Z][a-zA-Z]*).*/\1.\2/' | \
  sort -u > /tmp/im-svc-tests.txt || true

methods=$(wc -l < /tmp/im-svc-methods.txt | tr -d ' ')
tests=$(wc -l < /tmp/im-svc-tests.txt | tr -d ' ')

echo "service methods: $methods"
echo "service tests:   $tests"

# 缺失 diff（method - test）
missing=$(comm -23 /tmp/im-svc-methods.txt /tmp/im-svc-tests.txt | wc -l | tr -d ' ')
if [ "$missing" -gt "0" ]; then
    echo "WARN: check-svc-coverage: $missing service method 无对应单测（前 30 条）:"
    comm -23 /tmp/im-svc-methods.txt /tmp/im-svc-tests.txt | head -30
fi

echo "OK: check-svc-coverage list done（informational, no exit 1）."
