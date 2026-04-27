package http

import (
	"errors"

	"github.com/gin-gonic/gin"

	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// registerReq is the JSON body for POST /api/auth/register.
type registerReq struct {
	Username    string `json:"username"     binding:"required,min=3,max=32"`
	Email       string `json:"email"        binding:"required,email"`
	Password    string `json:"password"     binding:"required,min=8"`
	DisplayName string `json:"display_name"`
}

// loginReq is the JSON body for POST /api/auth/login. Login is a username or
// email; the service layer disambiguates on the '@' character.
type loginReq struct {
	Login    string `json:"login"    binding:"required"`
	Password string `json:"password" binding:"required"`
}

// authResponse mirrors the legacy net/http handler shape exactly.
type authResponse struct {
	Token string     `json:"token"`
	User  *repo.User `json:"user"`
}

// RegisterAuthRoutes wires the three auth endpoints onto r:
//
//   - POST /api/auth/register
//   - POST /api/auth/login
//   - GET  /api/auth/me  (JWT-protected)
//
// jwtSecret is needed both by the service (token signing) and the /me
// middleware. userRepo is needed by /me to look up the current user record.
//
// authedExtra (optional) lets the caller pre-attach middlewares to the /me
// route — typically MattermostCookieAuth so cookie-only callers can resolve
// /api/auth/me without a Bearer JWT. Pass nil to keep JWT-only behaviour.
func RegisterAuthRoutes(r *gin.Engine, svc *service.AuthService, userRepo repo.UserRepo, jwtSecret string, authedExtra ...gin.HandlerFunc) {
	pub := r.Group("/api/auth")

	pub.POST("/register", func(c *gin.Context) {
		var in registerReq
		if err := c.ShouldBindJSON(&in); err != nil {
			// Match the legacy handler: validation failures are 422.
			c.JSON(422, gin.H{"error": err.Error()})
			return
		}
		u, tok, err := svc.Register(c.Request.Context(), in.Username, in.Email, in.Password, in.DisplayName)
		switch {
		case errors.Is(err, service.ErrUserExists):
			c.JSON(409, gin.H{"error": "username or email already taken"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(201, authResponse{Token: tok, User: u})
		}
	})

	pub.POST("/login", func(c *gin.Context) {
		var in loginReq
		if err := c.ShouldBindJSON(&in); err != nil {
			// Match the legacy handler: missing/invalid fields → 422.
			c.JSON(422, gin.H{"error": "login and password are required"})
			return
		}
		u, tok, err := svc.Login(c.Request.Context(), in.Login, in.Password)
		switch {
		case errors.Is(err, service.ErrBadCreds):
			c.JSON(401, gin.H{"error": "invalid credentials"})
		case err != nil:
			c.JSON(500, gin.H{"error": "internal error"})
		default:
			c.JSON(200, authResponse{Token: tok, User: u})
		}
	})

	// Authenticated /me — Gin JWT middleware sets UserIDKey on the context.
	// Cookie middleware (when supplied via authedExtra) runs first so it can
	// inject UserIDKey, then JWTOrCookie accepts either path.
	authed := r.Group("/api/auth")
	for _, mw := range authedExtra {
		authed.Use(mw)
	}
	authed.Use(middleware.JWTOrCookie(jwtSecret))
	authed.GET("/me", func(c *gin.Context) {
		uidAny, ok := c.Get(middleware.UserIDKey)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		uid, ok := uidAny.(int64)
		if !ok {
			c.JSON(401, gin.H{"error": "unauthorized"})
			return
		}
		u, err := userRepo.GetByID(c.Request.Context(), uid)
		if err != nil {
			c.JSON(404, gin.H{"error": "user not found"})
			return
		}
		// Legacy handler returns the bare user object — preserve that shape.
		c.JSON(200, u)
	})
}
