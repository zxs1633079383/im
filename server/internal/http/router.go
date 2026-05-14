package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"
)

// Config configures the Gin router built by New.
type Config struct {
	ServiceName string          // OTel span name prefix
	Legacy      stdhttp.Handler // optional: existing net/http.ServeMux to wrap; routes not matched by Gin fall through here
	Mode        string          // gin.ReleaseMode (default), DebugMode, TestMode
}

// New returns a *gin.Engine wired with Recovery, otelgin tracing,
// /healthz + /readyz, and (if cfg.Legacy != nil) a NoRoute fallthrough
// that delegates unmatched requests to the legacy handler.
//
// Usage during the Phase 4-7 strangler-fig migration: pass the existing
// net/http.ServeMux as Legacy; new Gin handlers register on the returned
// *gin.Engine; they take precedence and the rest stays on the mux.
func New(cfg Config) *gin.Engine {
	if cfg.Mode == "" {
		cfg.Mode = gin.ReleaseMode
	}
	gin.SetMode(cfg.Mode)

	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(otelgin.Middleware(cfg.ServiceName))
	r.Use(corsMiddleware())
	r.Use(responseEnvelope())

	r.GET("/healthz", func(c *gin.Context) { c.String(200, "ok") })
	r.GET("/readyz", func(c *gin.Context) { c.String(200, "ok") })

	if cfg.Legacy != nil {
		legacy := cfg.Legacy
		r.NoRoute(func(c *gin.Context) {
			// Reset the 404 status Gin pre-set in serveError so the legacy
			// handler controls the response code.
			c.Writer.WriteHeader(stdhttp.StatusOK)
			legacy.ServeHTTP(c.Writer, c.Request)
		})
	}
	return r
}

// corsMiddleware adds permissive CORS headers and short-circuits OPTIONS
// preflight requests. Permissive on purpose for local dev / Tauri origins;
// tighten the allowed origin list before exposing to the public internet.
//
// CORS contract notes (the part that bites webviews):
//   - Allow-Origin must reflect the actual Origin (not "*") whenever
//     Allow-Credentials is "true" — fetch spec rejects the wildcard pair.
//   - Allow-Headers must list every custom header the client sends; cses-client
//     attaches cookieId for auth + userId/companyId for tenant routing +
//     X-Request-Id/X-Request-Source for tracing. Missing any of those drops the
//     real request at preflight as status=0 ("Unknown Error" in Angular).
//   - Allow-Methods must include PATCH for v0.7.0+ endpoints
//     (e.g. PATCH /api/channels/:id/members/:user_id { is_top }).
//   - Vary: Origin so reverse proxies / caches don't serve a stale Allow-Origin
//     to a different webview origin.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Vary", "Origin")
		c.Header("Access-Control-Allow-Methods", "*")
		c.Header("Access-Control-Allow-Headers", "*")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "600")
		if c.Request.Method == stdhttp.MethodOptions {
			c.AbortWithStatus(stdhttp.StatusNoContent)
			return
		}
		c.Next()
	}
}
