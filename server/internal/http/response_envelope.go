package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// responseEnvelope wraps every JSON response into the cses-shape standard
// envelope so the cses-client interceptor (messageHttp.service.ts:106) can
// stop branching on isWrappedResponse:
//
//	2xx → {"status":"success","data":<original-body>}
//	4xx/5xx → {"status":"error","error":"<original .error field or status text>"}
//
// Why a middleware instead of replacing 359 c.JSON calls in 22 handler files:
// zero-touch for handlers, single source of truth for the envelope shape,
// no regression risk in business logic. The trade-off — handlers still write
// `c.JSON(200, X)` style internally — is documented here so future readers
// know where the wrapping happens.
//
// Skipped paths:
//   - /healthz /readyz — k8s probes expect plain "ok"
//   - /metrics — Prometheus scrape format is text/plain
//
// Concurrency: each request gets its own envelopeWriter (no shared state).
// Body buffering is bounded by gin's max request size + handler discipline;
// no goroutines are spawned.
// ResponseEnvelope returns the responseEnvelope middleware. Exported so
// integration tests in tests/integration/ can wire the same envelope contract
// production uses (see router.go::r.Use(responseEnvelope())). Keeping the
// unexported responseEnvelope as the implementation lets us tighten / rewrite
// internals without breaking test imports.
//
// See docs/harness/C007 for the no-double-wrap contract.
func ResponseEnvelope() gin.HandlerFunc { return responseEnvelope() }

func responseEnvelope() gin.HandlerFunc {
	return func(c *gin.Context) {
		if shouldSkipEnvelope(c.Request.URL.Path) {
			c.Next()
			return
		}

		bw := &envelopeWriter{
			ResponseWriter: c.Writer,
			body:           bytes.NewBuffer(nil),
		}
		c.Writer = bw

		c.Next()

		status := bw.status
		if status == 0 {
			status = stdhttp.StatusOK
		}

		// Content-Type 后置检测：handler 走 c.DataFromReader 写二进制流
		// （如 GET /api/files/<id> 图片 / 文件下载），ContentType 是 image/* /
		// application/octet-stream / video/* 等非 JSON 类型。这种响应不能用
		// json.RawMessage 包装（silently fail body 变空）—— 原样字节流透传。
		// 4xx 错误路径走 c.JSON 设 ContentType=application/json 仍走 envelope。
		ct := bw.ResponseWriter.Header().Get("Content-Type")
		if ct != "" && !strings.HasPrefix(ct, "application/json") {
			bw.ResponseWriter.WriteHeader(status)
			_, _ = bw.ResponseWriter.Write(bw.body.Bytes())
			return
		}

		wrapped := wrapResponse(status, bw.body.Bytes())

		bw.ResponseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
		bw.ResponseWriter.WriteHeader(status)
		_, _ = bw.ResponseWriter.Write(wrapped)
	}
}

// shouldSkipEnvelope returns true for paths whose response shape is not
// JSON-wrapped (liveness / metrics / WebSocket).
//
// 二进制响应（如 GET /api/files/<id> 的 c.DataFromReader 流）通过中间件主流程
// 内的 Content-Type 后置检测分流，**不**在此处按 path 前缀 skip——因为同一路径
// 的 4xx 错误仍走 c.JSON 返回 application/json，需要 envelope wrap。
func shouldSkipEnvelope(path string) bool {
	switch path {
	case "/healthz", "/readyz", "/metrics":
		return true
	case "/ws":
		// WebSocket upgrade hijacks the underlying conn; the envelope writer's
		// buffered body would dead-end on a hijacked connection. Skip wrap so
		// the upgrade and ongoing frames flow straight through.
		return true
	}
	return false
}

// wrapResponse builds the envelope bytes from a captured handler body.
// For 2xx responses, body is embedded as JSON.RawMessage (preserves the
// original handler payload verbatim — array, object, or primitive).
// For non-2xx, the function tries to extract a top-level `error` string;
// if the body isn't a JSON object with an `error` field, the HTTP status
// text is used as a fallback.
func wrapResponse(status int, body []byte) []byte {
	if status >= 200 && status < 300 {
		raw := body
		if len(raw) == 0 {
			raw = []byte("null")
		}
		return mustMarshal(map[string]any{
			"status": "success",
			"data":   json.RawMessage(raw),
		})
	}

	msg := stdhttp.StatusText(status)
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(body, &parsed); err == nil {
		if e, ok := parsed["error"]; ok {
			var s string
			if json.Unmarshal(e, &s) == nil && s != "" {
				msg = s
			}
		}
	}
	return mustMarshal(map[string]any{
		"status": "error",
		"error":  msg,
	})
}

// mustMarshal returns the JSON encoding of v. Panics are impossible for
// the value shapes we feed it (map[string]any with string + json.RawMessage
// values), so swallowing the error is safe.
func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// envelopeWriter buffers handler output so the middleware can re-wrap it
// after c.Next() returns. It implements gin.ResponseWriter by embedding
// the underlying writer; only Write/WriteString/WriteHeader/Status/Size/
// Written are overridden because they touch buffered state.
type envelopeWriter struct {
	gin.ResponseWriter
	body   *bytes.Buffer
	status int
}

func (w *envelopeWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = stdhttp.StatusOK
	}
	return w.body.Write(b)
}

func (w *envelopeWriter) WriteString(s string) (int, error) {
	if w.status == 0 {
		w.status = stdhttp.StatusOK
	}
	return w.body.WriteString(s)
}

func (w *envelopeWriter) WriteHeader(status int) {
	w.status = status
}

func (w *envelopeWriter) Status() int {
	if w.status == 0 {
		return stdhttp.StatusOK
	}
	return w.status
}

func (w *envelopeWriter) Size() int {
	return w.body.Len()
}

func (w *envelopeWriter) Written() bool {
	return w.body.Len() > 0
}
