# Benchmark Index — im pre 集群全链路压测

> 跨批次索引。每个 **batch stamp** 格式 `YYYY-MM-DD-HHMM`，对应一次 `benchmark-loop.sh` 跑的多个 VU 梯度。Solo 批次（单 pod 测试）stamp 后缀 `-solo`。

## 演进概览（关注 VU=300 代表指标）

| Batch | 镜像 | 关键改动 | action_ok | send P95 | http_failed | SLOW SQL | 结论 |
|-------|------|---------|---------:|---------:|------------:|---------:|:-----|
| `2026-04-24-1234` | `pre-2` | 基线 pool=20，Conn.Push race 未修 | 85.44% | 12.75 s | 69.5% | 每轮数十条 | 不可用于生产 |
| `2026-04-24-1321` | `pre-4` | pool=300，panic bug 未修 | 26% | 3.93 s | 98% | — | **race 导致 pod restart 24 次**，数据不可信 |
| `2026-04-24-1343` | `pre-5` | + Conn.Push `closeOnce + atomic.Bool + defer recover` | **100%** | **680 ms** | **0%** | 有（FindDM 3s） | 业务层首次 100% 可靠 |
| `2026-04-24-1407` | `pre-6` | + FindDM 反向索引 + EXISTS 重写 | **100%** | **375 ms** | **0%** | **0** | 🏆 当前最佳基线 |

## 批次文件清单

### 2026-04-24-1234（pre-2 基线，4 轮）
- `2026-04-24-1234-VU{100,300,800,1500}.md`
- `2026-04-24-1234-summary.md` — 综合分析 + 瓶颈定位 + 下轮建议
- 结论：HPA 扩容 3→11 pod 有效，但 action_ok 98%→73% 随 VU 下降，长尾严重

### 2026-04-24-1307-solo（pre-3 single-pod，4 轮）
- `2026-04-24-1307-solo-VU{50,100,200,400}.md`
- 原计划测单 pod 真实瓶颈，被中断后继续跑，仅参考

### 2026-04-24-1313 / 2026-04-24-1321（pre-4，race 未修的 pool=300 数据）
- **不要作为基线** — pod panic 8 次 × 3 replica = 24 次崩溃，数据失真

### 2026-04-24-1343（pre-5 race fix + pool=300，3 轮 + skip）
- `2026-04-24-1343-VU{100,300,800}.md`
- `2026-04-24-1343-summary.md` — pre-2 vs pre-5 对比 + race 根因分析
- 结论：全链路 **100% action_ok** 首次达成，`maxReplicas=20` 在 VU=800 触发 stop（HPA=17）

### 2026-04-24-1407（pre-6 + DM 反向索引，3 轮 + skip）
- `2026-04-24-1407-VU{100,300,800}.md`
- `2026-04-24-1407-summary.md` — pre-5 vs pre-6 对比，sync/markRead P95 减半
- 结论：**SLOW SQL 归零**，QPS 345→363，单 pod 稳态上限刷新到 40-45 VU

## 版本 → 代码锚点

| 镜像 Tag | 对应代码 commit | 关键文件 |
|---------|-----------------|---------|
| pre-2 | `0b185ef` (M1+M2 base) | — |
| pre-3 | `3d3e844` | `config.go` pool=50 |
| pre-4 | `815df1f` | `db.go` + `config.go` pool=300 HikariCP |
| pre-5 | `5dc95e5` | `gateway/conn.go` closeOnce + atomic.Bool + recover |
| pre-6 | `64fb356` | `repo/channel.go` FindDM EXISTS + migration 011 |

## 单 pod 基线（pre-6 推算）

- 稳态 **40-45 并发 VU / pod**，sync P95 < 500ms
- ~20-50 req/s per pod 稳态
- HPA `Percent=100%` + `stabilizationWindow=30s` 可在 ~60s 内 doubling，跟得上常规 ramp

## 重跑命令

```bash
# 前置：pre 集群 kubectl + port-forward gateway 到 localhost:38080
source scripts/pre-env.sh
kubectl -n im-v2 port-forward svc/im-gateway 38080:8080 &

# 首次跑（会 seed 1500 users）
VU_LEVELS="100 300 800 1500 2500" scripts/benchmark-loop.sh

# 重跑（复用已有 user 池）
SKIP_SEED=1 VU_LEVELS="100 300 800" scripts/benchmark-loop.sh
```

## 报告格式规范

每份单轮报告包含：
1. 元数据表（VU / ramp-soak-down / pods / CPU / stop-reason）
2. **k6 summary tail**（im_http_* / im_action_ok / http_req_* / iterations / ws_*）
3. **Raw log tail**（最后 40 行 k6 output，含 thresholds violations）

每份 summary 报告包含：
1. 结果矩阵（VU 梯度横向对比）
2. 与前一批次核心指标 diff
3. 根因分析（如有 bug 修复）
4. 下一步方向

## 阈值策略（`benchmark-loop.sh`）

自动停：
- HPA replicas ≥ 15（接近 maxReplicas=20，保护 pre 集群）
- gateway CPU 总和 ≥ 80% of cluster limit（目前 CPU 从不是瓶颈，实际很少触发）

**未来可加**：
- `im_action_ok < 0.95` → 业务退化即停
- `http_req_failed > 0.1` → 错误率兜底
