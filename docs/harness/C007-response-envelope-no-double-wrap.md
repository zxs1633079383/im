# C007 — 全局 responseEnvelope 中间件已生效，handler 内**禁止**再写 `gin.H{"status":"success",...}`

```yaml
---
id: C007
title: handler 内只写 c.JSON(code, businessData)；禁止重复 wrap status/data/error 字段
status: active
created: 2026-05-07
last_recurred: 2026-05-01
recurrence_count: 1
source_logs:
  - logs/2026-05-01.json#L120
applies_to:
  - server/internal/http/**/*.go
inline_target: server/internal/http/response_envelope.go  # 中间件实现已是 SoT
---
```

## 1. 触发场景（Trigger）

任何修改 / 新增 `server/internal/http/*.go` handler 的 PR：

- 任何 `authed.{GET,POST,PUT,PATCH,DELETE}(...)` 路由内部
- 任何 `c.JSON(...)` / `c.AbortWithStatusJSON(...)` 调用
- 关键词 grep：`c.JSON` / `gin.H{"status"` / `"success"` / `"error":` 在 handler 文件中

## 2. 错误模式（Anti-Pattern）

```go
// ❌ 错误 #1：handler 自己 wrap 了 status:success（会被中间件二次 wrap）
authed.POST("/messages/:id/received", func(c *gin.Context) {
    msg, err := svc.MarkTemplateReceived(...)
    if err != nil {
        c.JSON(500, gin.H{"status": "error", "error": err.Error()})  // ❌
        return
    }
    c.JSON(200, gin.H{"status": "success", "data": msg})  // ❌ 重复 wrap
})
// 客户端最终收到：{"status":"success","data":{"status":"success","data":{...}}}
// → 双层嵌套，前端 interceptor isWrappedResponse 判断错乱

// ❌ 错误 #2：handler 写 error 字段但走 c.JSON 不是 c.AbortWithStatusJSON
c.JSON(400, gin.H{"error": "invalid id"})
// 中间件看到 4xx → 想 wrap 成 {"status":"error","error":"invalid id"}
// 但 body 已经有 "error" 字段 → 解析逻辑兼容性陷阱

// ❌ 错误 #3：errors.Is 分支返回时直接写中文 message（不抽 sentinel）
if errors.Is(err, repo.ErrNotFound) {
    c.JSON(404, gin.H{"error": "未找到"})  // 硬编码 message，i18n 难做
}
```

**后果**：
1. **双层 envelope**：前端 interceptor `isWrappedResponse` 收到 `{"status":"success","data":{"status":"success",...}}` → 数据嵌套两层，业务字段访问失败
2. **错误码与 body 分裂**：`c.JSON(400, ...)` 不 abort，后续中间件继续跑，可能把 200 OK envelope 套到 4xx body 上
3. **i18n / runbook 灾难**：错误 message 散落 22 个文件硬编码，统一改文案 / 加 trace_id 时改不动

事故链路：
- 2026-05-01 Phase 1 加 `POST /messages/:id/received` handler 时，作者复制旧 handler 模板带了 `gin.H{"status":"success","data":msg}`，中间件二次 wrap → 集成测试 `TestM4TemplateReceived_HappyPath` 红色，定位 15 分钟（commit `66c2a67` 修）

## 3. 正确做法（Required）

**首选 A — handler 只关心业务数据**：

```go
// ✅ 正确：直接传 business object，中间件统一 wrap
authed.POST("/messages/:id/received", func(c *gin.Context) {
    uid, _ := userIDFromCtx(c)
    msgID, _ := pathInt64(c, "id")

    msg, err := svc.MarkTemplateReceived(c.Request.Context(), msgID, uid)
    switch {
    case errors.Is(err, repo.ErrNotFound):
        c.AbortWithStatusJSON(404, gin.H{"error": "message not found"})
        return
    case errors.Is(err, repo.ErrForbidden):
        c.AbortWithStatusJSON(403, gin.H{"error": "not a template message"})
        return
    case err != nil:
        log.Error(...)
        c.AbortWithStatusJSON(500, gin.H{"error": "internal error"})
        return
    }
    c.JSON(200, msg)  // ← 中间件会 wrap 成 {"status":"success","data":msg}
})
```

**首选 B — 错误返回必须 `AbortWithStatusJSON`**：

错误路径**必须** abort 才不再继续后续 middleware；中间件只看 `gin.H{"error": "..."}` 这一种 shape。

**首选 C — sentinel error**：

```go
// ✅ 错误对外暴露的 message 集中定义
var (
    ErrNotChannelMember = errors.New("not channel member")
    ErrInvalidPayload   = errors.New("invalid payload")
)
```

**绝对禁止 D**：
- ❌ handler 内写 `gin.H{"status": "success", ...}` / `gin.H{"status": "error", ...}` —— 字段名 `status` 是中间件占用
- ❌ 在 handler 同时设 `data` 字段和 `error` 字段（任何一个）—— 由 status code 推断，不要双写
- ❌ 错误路径用 `c.JSON(40x, ...)` 而非 `c.AbortWithStatusJSON(40x, ...)`
- ❌ 在 handler 内 `c.Header("Content-Type", "application/json; charset=utf-8")` —— 中间件已设置

**实施约束**：
- 中间件实现：`server/internal/http/response_envelope.go::responseEnvelope()`
- 已注册位置：`server/internal/http/router.go::r.Use(responseEnvelope())`
- 跳过路径（`shouldSkipEnvelope`）：`/healthz` / `/readyz` / `/metrics`
- handler 责任：**只**返回 `c.JSON(code, businessData)` 或 `c.AbortWithStatusJSON(40x/5xx, gin.H{"error": "..."})`

## 4. 检查方法（Verification）

### 4.1 自动 grep（必须返回 0 条）

```bash
# ① handler 内写了 "status":"success" / "status":"error"
grep -rEn 'gin\.H\{[^}]*"status"\s*:\s*"(success|error)"' \
  server/internal/http/ --include='*.go' | grep -v '_test.go' \
  | grep -v 'response_envelope.go'

# ② handler 写 "data" 字段（应只在 envelope 中间件 wrap 后出现）
grep -rEn 'gin\.H\{[^}]*"data"\s*:' server/internal/http/ --include='*.go' \
  | grep -v '_test.go' | grep -v 'response_envelope.go'

# ③ 错误路径用 c.JSON 而非 c.AbortWithStatusJSON（启发式：c.JSON(4xx 或 5xx 字面量)
grep -rEn 'c\.JSON\((4[0-9][0-9]|5[0-9][0-9])\s*,' server/internal/http/ --include='*.go' \
  | grep -v 'response_envelope.go' | grep -v '_test.go'

# ④ handler 自己设 Content-Type
grep -rEn 'c\.Header\("Content-Type"' server/internal/http/ --include='*.go' \
  | grep -v 'response_envelope.go' | grep -v '_test.go'
```

### 4.2 CI Gate

- `verify-all` 加上 §4.1 4 条 grep
- handler 单测必须断言 envelope 后的最终 body：

```go
// 错误模板：直接断言原始 body
expect.POST("/api/messages/123/received").
    WithHeader(...).Expect().Status(200).
    JSON().Object().Value("messageId").IsEqual(123)  // ❌ 没经 envelope

// ✅ 正确模板
expect.POST("/api/messages/123/received").
    WithHeader(...).Expect().Status(200).
    JSON().Object().
    Value("status").IsEqual("success").                  // 中间件 wrap 字段
    Value("data").Object().Value("messageId").IsEqual(123)
```

### 4.3 单测（白盒，已 100% 覆盖）

- 路径：`server/internal/http/response_envelope_test.go`
- 已实现 8 个用例（成功 / 4xx 错误 / 5xx 错误 / skip 路径 / 已 wrap body 不二次 wrap / 等）
- 覆盖率：`response_envelope.go` 全 10 函数 100%

### 4.4 集成测试（22 handler 文件统一断言模板）

- 集成测试 helper 提供：`expect.JSON().Object().Value("status").IsEqual("success").Value("data")...`
- 任何新增集成测试**必须**断言 envelope 字段（C008 §3 集成测试模板已含）

### 4.5 失败 case 的契约

| HTTP code | 中间件最终 body |
|---|---|
| 2xx | `{"status":"success","data":<handler body>}` |
| 4xx / 5xx | `{"status":"error","error":<handler body 的 error 字段 或 status text>}` |

## 5. 复现历史（Recurrence Log）

| # | 日期       | 触发场景                                                                            | 引用日志                  | 处置                                                                  |
|---|------------|-------------------------------------------------------------------------------------|---------------------------|-----------------------------------------------------------------------|
| 1 | 2026-05-01 | Phase 1 加 `/messages/:id/received` handler 复制旧模板带了双层 wrap，集成测试红     | logs/2026-05-01.json#L120| 删除 handler 内的 `"status":"success"` wrap（commit `66c2a67`）        |

## 6. 反例与边界（Don't Over-Apply）

- ✅ **`/healthz` / `/readyz`**：中间件 `shouldSkipEnvelope` 已跳过，handler 返回 `c.String(200, "ok")` 不受约束
- ✅ **`/metrics`**：Prometheus 文本格式，跳过 envelope
- ✅ **streaming 响应**（如未来加文件下载）：在 middleware 加跳过条件，不受 envelope 约束
- ✅ **测试 fixture**：`tests/integration/*.go` 内部构造的 mock response 不经过 middleware，可以自由 wrap
- ❌ **不要**因为想"看着像旧 cses 的 shape"在 service 层返回 `{"status":"success","data":...}` —— service 层只返业务对象
- ❌ **不要**为了一个特例 endpoint（如 callback URL）禁掉中间件 —— 应在 middleware skip list 加路径

## 7. 升级 / 弃用条件（Lifecycle）

**晋升 → merged**：
- 30 天零新复现 + §4.1 grep 在 CI 接管
- §4.3 单测保持 100% 覆盖
- inline 进 `server/docs/BACKEND.md §响应契约`（待新建该节）

**弃用 → deprecated**：
- 协议改 protobuf / gRPC（不再有 JSON envelope 概念）
- envelope 协议演进到 V2（如加 `trace_id` / `request_id` 字段）→ 字段扩展兼容，本条不变
- 中间件被替换（如改 `gin-jwt` 风格的 ResponseFormatter）→ 新建 C{NNN}-replacement，本条 deprecated
