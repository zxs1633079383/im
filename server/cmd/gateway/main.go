package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"im-server/internal/config"
	"im-server/internal/handler"
	"im-server/internal/middleware"
	"im-server/internal/store"
)

func main() {
	fmt.Println("gateway starting...")
	os.Exit(run())
}

func run() int {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgPath := os.Getenv("IM_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return 1
	}

	if cfg.Gateway.JWTSecret == "" {
		log.Error("gateway.jwt_secret must not be empty")
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := store.NewPGPool(ctx, cfg.PG.DSN, cfg.PG.MaxConns)
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer pool.Close()

	userStore := store.NewUserStore(pool)
	authHandler := handler.NewAuthHandler(userStore, cfg.Gateway.JWTSecret, log)
	jwtMiddleware := middleware.JWTAuth(cfg.Gateway.JWTSecret)

	mux := http.NewServeMux()

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login", authHandler.Login)

	// Protected routes
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(authHandler.Me)))

	// CORS middleware for development
	corsHandler := corsMiddleware(mux)

	srv := &http.Server{
		Addr:         cfg.Gateway.HTTPAddr,
		Handler:      corsHandler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info("HTTP server listening", "addr", cfg.Gateway.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	log.Info("shutting down...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "error", err)
		return 1
	}

	return 0
}

// corsMiddleware adds permissive CORS headers for local Tauri development.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
