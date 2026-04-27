# pre-7d benchmark — M4.5 cookie-auth baseline

**Date:** 2026-04-27
**Image:** `harbor.jinqidongli.com/x9-go/im/im-gateway:v1.0.0-pre-7d`
**Image digest:** `sha256:b3369402adb3ea0512b46a42ba3eb6b62c917e4bf9582492e3e2a13e9f9620db`
**Tag:** `v0.6.2-m4-perf-baseline` (pending)

## Setup

- pre 集群 namespace `im-v2`，HPA min=3 max=20，pre-7d image
- 300 个 synthetic cookie 在 cses Redis HASH "User"（`server/scripts/seed-mm-cookies-bulk.sh` N=300）
- k6 ramping VU `0 → 300 → 0` over `60+120+20s`，`SEND_PER_ITER=3`
- Driver script: `scripts/v4-load-m4.js`

## Results

| Metric | pre-7d | pre-6 baseline | Delta |
|---|---:|---:|---:|
| `im_send_ms` p95 | **211ms** | 375ms | **−164ms (−44%)** |
| `im_send_ms` avg | 95ms | 200ms | −105ms |
| `im_me_ms` p95 | 10ms | n/a (JWT path) | new |
| `im_action_ok` | 100% | 100% | — |
| `im_send_ok` | 100% | 100% | — |
| HPA peak pods | 12 / 20 | ~17 / 20 | better headroom |
| `im_sync_ms` p95 | 5411ms | n/a | test artefact (DM 数随时间爆炸到 14k+) |

## Cookie cache hit rate (sampled one pod, post-test)

| Counter | Value |
|---|---:|
| `im_auth_cookie_cache_hit_total` | 12931 |
| `im_auth_cookie_cache_miss_total` | 293 |
| **hit_rate** | **97.78%** (target ≥ 90% ✅) |

## SLO 闸门

| 维度 | 闸门 | 通过 |
|---|---|---|
| send P95 ≤ 400ms（pre-6 + 25ms LRU 容差） | 211ms ≤ 400ms | ✅ |
| action_ok ≥ 99% | 100% | ✅ |
| cookie_cache hit_rate ≥ 90% | 97.78% | ✅ |
| HPA 不撞 max | peak 12 / 20 | ✅ |

## 结论

M4 cookie 单栈对比 pre-6 (M3 JWT 双栈) 有显著性能改善：
- LRU 30s TTL × 10k cap：97.78% hit_rate 把 99% 的 cookie 解析路径变成内存查
  询；只有 cold 启动 / TTL 过期时回 Redis HGet。
- send P95 −44% 推测主因：M3 era 走 JWT 解析 + lazy-upsert shadow user 双
  redis 操作，M4 单 LRU + cookie identity 路径 cleaner。
- HPA 没像 pre-5/6 那样撞 17 pod 顶 — 单 pod 容量随之提升。

## 下一步

- v0.6.2 tag 候选打到 `73e6c69` (Prometheus exporter) 之后的 SESSION
  doc commit。
- Grafana panel 加 `im_auth_cookie_cache_hit_total / (hit+miss)` ratio + size
  gauge；spec §11.5 闸门用同一公式。
- e2e-pre.mjs cookie 化（13/13 重跑）。
