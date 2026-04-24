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
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			origin = "*"
		}
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", "600")
		if c.Request.Method == stdhttp.MethodOptions {
			c.AbortWithStatus(stdhttp.StatusNoContent)
			return
		}
		c.Next()
	}
}
