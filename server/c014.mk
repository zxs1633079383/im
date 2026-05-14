# C014 — 测试覆盖 + CI gate target（独立 makefile，避免污染主 Makefile）
# Spec: docs/harness/C014-test-coverage-100-percent.md
#
# 用法：
#   cd server
#   make -f c014.mk check-c014-all          # 跑 4 件套
#   make -f c014.mk check-id-types          # 单跑 C012 三件套
#   make -f c014.mk check-route-coverage    # 路由 vs 集成测试
#   make -f c014.mk check-svc-coverage      # service method vs 单测
#   make -f c014.mk check-test-cover        # go test -cover 阈值

.PHONY: check-id-types check-route-coverage check-svc-coverage check-test-cover check-c014-all

check-id-types:
	@bash scripts/check-id-types.sh

check-route-coverage:
	@bash scripts/check-route-coverage.sh

check-svc-coverage:
	@bash scripts/check-svc-coverage.sh

check-test-cover:
	@bash scripts/check-test-cover.sh

check-c014-all: check-id-types check-route-coverage check-svc-coverage check-test-cover
	@echo "OK: C014 4 件套全过"
