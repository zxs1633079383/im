# IM v2 — Grafana Dashboard

一份面向 `im-2.0` namespace 的"帅气"仪表盘：gateway / message / sync 三大子系统
的 RED 指标 + 队列 + 同步追赶信号，顶部全局栏 + 底部综合健康分。

- **Dashboard file**: `im-v2-dashboard.json`
- **UID**: `im-v2-main`
- **Target env**: 默认 `im-2.0`（pre 集群），变量 `$env` 可切换
- **Datasource UID**: `prometheus`（kube-prometheus-stack 默认）
- **Panel count**: 16（含 4 条 row 分组）

---

## 1. 环境探测结论（pre 集群）

| 组件 | Service | 端口 / NodePort | 说明 |
|------|---------|----------------|------|
| Grafana | `monitoring/prometheus-stack-grafana` | 80 / **30300** | 默认管理员见集群密钥 |
| Prometheus | `monitoring/prometheus-stack-kube-prom-prometheus` | 9090 / 30090 | datasource UID `prometheus` |
| Alertmanager | `monitoring/prometheus-stack-kube-prom-alertmanager` | 9093 | UID `alertmanager` |
| Loki | `monitoring/loki-pre` | 3100 / 31000 | 日志（本 dashboard 暂不使用） |
| Jaeger | `jaeger-cses/jaeger-v2-query` | 16686 / 32281 | trace drill-down 链接已内置 |
| Jaeger OTLP | `jaeger-cses/jaeger-v2-collector` | 4317/4318 | im-server 现用 trace sink |

**注意：`im-2.0` namespace 目前为空**（im 后端还未部署）。仪表盘已按未来部署后的
label schema 设计好，只要 im-server 起来并接通 OTel Collector，数据即刻点亮。

---

## 2. Dashboard 结构

### 顶部 Global (row id 100)
| Panel | 类型 | 作用 |
|------|------|------|
| Composite Health | gauge | 综合健康分 0–100（gateway 0.4 + message 0.3 + sync 0.3）|
| Pod Replicas | stat | 各 im deployment 可用副本数 |
| PG Pool Utilisation | gauge | PostgreSQL 连接池使用率 |
| Pulsar Producers | timeseries | 各 pod 的 Pulsar producer 数量 |

### A · Gateway (row id 200 · service `im-gateway`)
| Panel | 类型 | 说明 |
|------|------|------|
| WS Active Connections | gauge | 阈值 10k / 30k / 50k |
| HTTP QPS / P50 / P99 | timeseries | 按 route 分组；QPS 用暖色 bar，延迟用冷色线 |
| Cross-Pod Push Success Rate | stat (background) | Pulsar 推送成功占比 |
| Redis Routing Ops | timeseries | 按 op 分类堆叠柱状 |
| Pulsar Producer P99 Latency | timeseries | 冷色连续渐变 |

### B · Message (row id 300 · service `im-message`)
| Panel | 类型 | 说明 |
|------|------|------|
| SendMessage QPS + P99 | timeseries | 暖色 bar + 冷色线 |
| AllocSeqAndInsert Row-Lock Heatmap | heatmap | Turbo 配色，定位慢事务 |
| Fan-Out E2E Latency (HTTP→WS) | timeseries | P50 / P95 / P99 |
| Delete / Update Ops | stat | 背景色分类展示 |

### C · Sync (row id 400 · `POST /api/sync`)
| Panel | 类型 | 说明 |
|------|------|------|
| /api/sync QPS + P99 | timeseries | 同 SendMessage 样式 |
| Avg Channels / Messages per Sync | timeseries | 追赶信号 |
| `has_more=true` Ratio | gauge | >30% 告警（客户端长期落后） |
| Empty Sync Ratio | gauge | 低 → 客户端盲轮询 |

---

## 3. 全部 PromQL 清单

### Global
```promql
# Composite Health (0-100)
100 * (
  0.4 * clamp_max((sum(rate(http_server_request_duration_seconds_count{service_name="im-gateway",http_response_status_code=~"2.."}[5m])) or vector(0))
               / (sum(rate(http_server_request_duration_seconds_count{service_name="im-gateway"}[5m])) > 0 or vector(1)), 1)
  + 0.3 * clamp_max((sum(rate(http_server_request_duration_seconds_count{service_name="im-message",http_response_status_code=~"2.."}[5m])) or vector(0))
               / (sum(rate(http_server_request_duration_seconds_count{service_name="im-message"}[5m])) > 0 or vector(1)), 1)
  + 0.3 * clamp_max((sum(rate(http_server_request_duration_seconds_count{service_name="im-message",http_route=~".*/api/sync.*",http_response_status_code=~"2.."}[5m])) or vector(0))
               / (sum(rate(http_server_request_duration_seconds_count{service_name="im-message",http_route=~".*/api/sync.*"}[5m])) > 0 or vector(1)), 1)
)

# Pod replicas
kube_deployment_status_replicas_available{namespace="$env", deployment=~"im-.+"}

# PG pool util
sum(go_sql_db_open_connections{service_name="im-message",status="in_use"})
  / clamp_min(sum(go_sql_db_open_connections{service_name="im-message",status="max_open"}), 1)

# Pulsar producers
sum by (pod) (im_pulsar_producer_active{namespace="$env"})
```

### Gateway (A)
```promql
# WS active connections (already emitted today)
sum(im_ws_active_connections{namespace="$env", pod=~"$pod"})

# HTTP QPS per route
sum by (http_route) (rate(http_server_request_duration_seconds_count{service_name="im-gateway", http_route=~"$route"}[$__rate_interval]))

# HTTP P50 per route
histogram_quantile(0.50, sum by (http_route, le) (rate(http_server_request_duration_seconds_bucket{service_name="im-gateway", http_route=~"$route"}[$__rate_interval])))

# HTTP P99 per route
histogram_quantile(0.99, sum by (http_route, le) (rate(http_server_request_duration_seconds_bucket{service_name="im-gateway", http_route=~"$route"}[$__rate_interval])))

# Cross-pod push success rate
sum(rate(im_push_pulsar_send_total{status="ok",namespace="$env"}[1m]))
  / clamp_min(sum(rate(im_push_pulsar_send_total{namespace="$env"}[1m])), 1e-9)

# Redis routing ops
sum by (op) (rate(im_routing_redis_ops_total{namespace="$env"}[$__rate_interval]))

# Pulsar producer Send() P99
histogram_quantile(0.99, sum by (topic, le) (rate(im_pulsar_producer_send_duration_seconds_bucket{namespace="$env"}[$__rate_interval])))
```

### Message (B)
```promql
# SendMessage QPS
sum(rate(http_server_request_duration_seconds_count{service_name="im-message", http_route=~".*/messages$", http_request_method="POST"}[$__rate_interval]))

# SendMessage P99
histogram_quantile(0.99, sum by (le) (rate(http_server_request_duration_seconds_bucket{service_name="im-message", http_route=~".*/messages$", http_request_method="POST"}[$__rate_interval])))

# AllocSeqAndInsert heatmap
sum by (le) (rate(im_message_alloc_seq_duration_seconds_bucket{namespace="$env"}[$__rate_interval]))

# Fan-out E2E (P50/P95/P99)
histogram_quantile(0.50, sum by (le) (rate(im_fanout_e2e_duration_seconds_bucket{namespace="$env"}[$__rate_interval])))
histogram_quantile(0.95, sum by (le) (rate(im_fanout_e2e_duration_seconds_bucket{namespace="$env"}[$__rate_interval])))
histogram_quantile(0.99, sum by (le) (rate(im_fanout_e2e_duration_seconds_bucket{namespace="$env"}[$__rate_interval])))

# Delete ops
sum(rate(http_server_request_duration_seconds_count{service_name="im-message", http_route=~".*/messages/:id$", http_request_method="DELETE"}[$__rate_interval]))

# Update ops
sum(rate(http_server_request_duration_seconds_count{service_name="im-message", http_route=~".*/messages/:id$", http_request_method="PATCH"}[$__rate_interval]))
```

### Sync (C)
```promql
# /api/sync QPS
sum(rate(http_server_request_duration_seconds_count{service_name="im-message", http_route=~".*/api/sync.*"}[$__rate_interval]))

# /api/sync P99
histogram_quantile(0.99, sum by (le) (rate(http_server_request_duration_seconds_bucket{service_name="im-message", http_route=~".*/api/sync.*"}[$__rate_interval])))

# Avg channels per sync
sum(rate(im_sync_response_channels_sum{namespace="$env"}[$__rate_interval]))
  / clamp_min(sum(rate(im_sync_response_channels_count{namespace="$env"}[$__rate_interval])), 1e-9)

# Avg messages per sync
sum(rate(im_sync_response_messages_sum{namespace="$env"}[$__rate_interval]))
  / clamp_min(sum(rate(im_sync_response_messages_count{namespace="$env"}[$__rate_interval])), 1e-9)

# has_more ratio
sum(rate(im_sync_response_total{has_more="true",namespace="$env"}[$__rate_interval]))
  / clamp_min(sum(rate(im_sync_response_total{namespace="$env"}[$__rate_interval])), 1e-9)

# empty sync ratio
sum(rate(im_sync_response_total{empty="true",namespace="$env"}[$__rate_interval]))
  / clamp_min(sum(rate(im_sync_response_total{namespace="$env"}[$__rate_interval])), 1e-9)
```

---

## 4. Metric 命名前提（im-server 需要 emit 哪些）

### 已在点亮的 metric（代码已有）
| Prom 名（自动下划线） | OTel 名 | 来源 |
|-----------------------|---------|------|
| `im_ws_active_connections` | `im.ws.active_connections` | `server/internal/gateway/hub.go:registerMetrics` |
| `http_server_request_duration_seconds_*` | otelgin 标准 | `server/internal/http/router.go` (`otelgin.Middleware`) |
| `go_*` / `process_*` | OTel Go runtime | observability 默认开启 |

### 需要新增的 metric（按 panel 优先级）

> **命名约定**：OTel instrument name 用点号，Collector 会自动转为 Prom 下划线。
> 所有 im 业务 metric 前缀 `im.`，在 dashboard 中写作 `im_`。

#### 区 A · Gateway
```go
// internal/gateway/hub.go 或新 metrics.go
meter := otel.Meter("im-gateway")

pushSendTotal, _ := meter.Int64Counter(
    "im.push.pulsar.send",
    metric.WithDescription("Cross-pod push messages sent via Pulsar, labelled by status (ok/err)"),
)
// 调用点：gateway.CrossPodPush() 的 send 成功 / 失败分支
// 打标签：attribute.String("status", "ok"|"err"), attribute.String("topic", topic)

routingOpsTotal, _ := meter.Int64Counter(
    "im.routing.redis.ops",
    metric.WithDescription("Redis routing key ops (SET/GET/DEL)"),
)
// 调用点：user→pod route 的 SET/GET/DEL/EXPIRE
// 标签：attribute.String("op", "set"|"get"|"del"|"expire")

pulsarSendDur, _ := meter.Float64Histogram(
    "im.pulsar.producer.send.duration",
    metric.WithUnit("s"),
    metric.WithDescription("Pulsar producer Send() latency"),
)
// 标签：attribute.String("topic", topic)
// 建议 bucket：[.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5]

pulsarProdActive, _ := meter.Int64UpDownCounter(
    "im.pulsar.producer.active",
    metric.WithDescription("Count of live Pulsar producers in this pod"),
)
// 每次 NewProducer 成功 +1，Close -1
```

#### 区 B · Message
```go
// internal/service/message.go 或 internal/repo/message.go
meter := otel.Meter("im-message")

allocSeqDur, _ := meter.Float64Histogram(
    "im.message.alloc_seq.duration",
    metric.WithUnit("s"),
    metric.WithDescription("AllocSeqAndInsert end-to-end duration (includes row-lock wait)"),
)
// 调用点：repo.MessageRepo.AllocSeqAndInsert 包一层 time.Since
// 建议 bucket：[.001, .005, .01, .025, .05, .1, .25, .5, 1]

fanoutE2E, _ := meter.Float64Histogram(
    "im.fanout.e2e.duration",
    metric.WithUnit("s"),
    metric.WithDescription("HTTP SendMessage → last subscriber WS deliver end-to-end latency"),
)
// 调用点：gateway 侧在 WS out 时计算 now - msg.CreatedAt
// 标签：attribute.Int("subscribers_bucket", ...) 可选
```

#### 区 C · Sync
```go
// internal/service/sync.go
meter := otel.Meter("im-message")

syncRespTotal, _ := meter.Int64Counter(
    "im.sync.response",
    metric.WithDescription("Total /api/sync responses, labelled by has_more / empty"),
)
// 调用点：handler 返回前，根据 response 内容打 bool label
// 标签：attribute.Bool("has_more", resp.HasMore), attribute.Bool("empty", len(resp.Channels)==0)

syncChannels, _ := meter.Int64Histogram(
    "im.sync.response.channels",
    metric.WithDescription("Number of channels returned per /api/sync response"),
)
// 建议 bucket：[0, 1, 5, 10, 25, 50, 100, 250, 500]

syncMessages, _ := meter.Int64Histogram(
    "im.sync.response.messages",
    metric.WithDescription("Number of messages returned per /api/sync response"),
)
// 建议 bucket：[0, 1, 10, 50, 100, 500, 1000, 5000]
```

### Label 注意事项
- **namespace**：来自 OTel Collector K8s processor，会自动注入 `k8s.namespace.name` →
  Prom 标签名通常是 `namespace`（kube-prometheus-stack 默认）。如果你的 Collector
  配置转成 `k8s_namespace_name`，需要在 dashboard JSON 里 replace-all 一次。
- **pod**：同理，来自 K8s processor → `pod`。
- **service_name**：OTel resource attr `service.name` → Prom label `service_name`
  （`prometheus` exporter 默认行为）。
- **http_route / http_request_method / http_response_status_code**：otelgin 标配。

---

## 5. 如何 Import

### 方案 A · Grafana UI
1. `kubectl -n monitoring port-forward svc/prometheus-stack-grafana 3000:80`
2. 浏览器开 `http://localhost:3000`，用管理员账号登录
3. 左侧 `Dashboards` → `New` → `Import` → 粘贴 `im-v2-dashboard.json` 内容
4. 确认 datasource 匹配 `Prometheus`（UID 已写死 `prometheus`，直接点 Import）

### 方案 B · Grafana HTTP API（CI/CD）
```bash
GRAFANA_URL="http://localhost:3000"
GRAFANA_TOKEN="<service-account-token>"

curl -sS -X POST "${GRAFANA_URL}/api/dashboards/db" \
  -H "Authorization: Bearer ${GRAFANA_TOKEN}" \
  -H "Content-Type: application/json" \
  -d @- <<EOF
{
  "dashboard": $(cat deploy/grafana/im-v2-dashboard.json),
  "folderUid": "",
  "overwrite": true,
  "message": "im-v2 dashboard import via CI"
}
EOF
```

### 方案 C · ConfigMap sidecar（GitOps 推荐）
kube-prometheus-stack 默认启用 `sidecar.dashboards.enabled`，扫描带
`grafana_dashboard: "1"` label 的 ConfigMap：
```bash
kubectl -n monitoring create configmap im-v2-dashboard \
  --from-file=im-v2.json=deploy/grafana/im-v2-dashboard.json

kubectl -n monitoring label configmap im-v2-dashboard grafana_dashboard=1
```
sidecar 会在 30s 内自动热加载。

---

## 6. 样式设计说明（"帅气"清单）

- **深色主题**固定（`style: dark`）
- **暖色暖事**：QPS / 吞吐类（bars）统一 `#FF7F50` / `#FF8C00` / YlOrRd 连续渐变
- **冷色冷静**：延迟 / P50-P99（lines）统一 `#6495ED` / `#9370DB` / BlYlRd
- **双轴对齐**：QPS 在左轴，延迟在右轴，用 `axisPlacement` override 强制
- **P99 加粗**：`lineWidth: 3`，一眼抓到尾延迟异常
- **阈值染色**：所有 gauge 都走 absolute thresholds，红黄绿语义一致
- **时间联动**：`graphTooltip: 1`（Shared crosshair）让三区 panel 光标同步
- **变量 cascade**：`$env` → `$pod`（query 依赖）→ `$route` 独立
- **注解**：Pod restart 自动标红线，异常事件一眼看到
- **快捷跳转**：顶部 link 直达 Jaeger `im-gateway` service 查询页

---

## 7. 后续迭代方向

- [ ] 加 Loki datasource + error log panel（区 A 右下角）
- [ ] 加 Tempo link，支持从 P99 trace-id 直接 drill-down
- [ ] 做一个「按用户消息量 TOP 10」table（需要 `im.message.sent{user_id}` counter，但注意高基数问题）
- [ ] 加 alert rules（`alert_rules.yaml`，同目录）——当前只有 dashboard，没绑 alert
- [ ] 把 `$env` 扩展为 `im-pre` / `im-prod`，把 namespace label 的 regex 参数化

---

**设计于 2026-04-24，目标 pre 集群 `im-2.0` namespace。**
