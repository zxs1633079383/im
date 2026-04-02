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

	friendStore := store.NewFriendshipStore(pool)
	friendHandler := handler.NewFriendHandler(friendStore, userStore, log)

	channelStore := store.NewChannelStore(pool)
	channelHandler := handler.NewChannelHandler(channelStore, userStore, log)

	messageStore := store.NewMessageStore(pool)
	messageHandler := handler.NewMessageHandler(messageStore, channelStore, log)

	mux := http.NewServeMux()

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login", authHandler.Login)

	// Protected routes
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(authHandler.Me)))

	// Friend routes (JWT protected)
	mux.Handle("POST /api/friends/request", jwtMiddleware(http.HandlerFunc(friendHandler.SendRequest)))
	mux.Handle("POST /api/friends/accept", jwtMiddleware(http.HandlerFunc(friendHandler.AcceptRequest)))
	mux.Handle("POST /api/friends/reject", jwtMiddleware(http.HandlerFunc(friendHandler.RejectRequest)))
	mux.Handle("GET /api/friends", jwtMiddleware(http.HandlerFunc(friendHandler.ListFriends)))
	mux.Handle("GET /api/friends/pending", jwtMiddleware(http.HandlerFunc(friendHandler.ListPending)))
	mux.Handle("POST /api/friends/block", jwtMiddleware(http.HandlerFunc(friendHandler.Block)))

	// User search route (JWT protected)
	mux.Handle("GET /api/users/search", jwtMiddleware(http.HandlerFunc(friendHandler.SearchUsers)))

	// Channel routes (JWT protected)
	mux.Handle("POST /api/channels", jwtMiddleware(http.HandlerFunc(channelHandler.CreateGroup)))
	mux.Handle("POST /api/channels/dm", jwtMiddleware(http.HandlerFunc(channelHandler.CreateOrGetDM)))
	mux.Handle("GET /api/channels", jwtMiddleware(http.HandlerFunc(channelHandler.ListChannels)))
	mux.Handle("GET /api/channels/{id}", jwtMiddleware(http.HandlerFunc(channelHandler.GetChannel)))
	mux.Handle("PUT /api/channels/{id}", jwtMiddleware(http.HandlerFunc(channelHandler.UpdateChannel)))
	mux.Handle("POST /api/channels/{id}/members", jwtMiddleware(http.HandlerFunc(channelHandler.AddMember)))
	mux.Handle("DELETE /api/channels/{id}/members/{user_id}", jwtMiddleware(http.HandlerFunc(channelHandler.RemoveMember)))
	mux.Handle("GET /api/channels/{id}/members", jwtMiddleware(http.HandlerFunc(channelHandler.ListMembers)))
	mux.Handle("POST /api/channels/{id}/leave", jwtMiddleware(http.HandlerFunc(channelHandler.LeaveChannel)))

	// Message routes (JWT protected)
	mux.Handle("POST /api/channels/{id}/messages", jwtMiddleware(http.HandlerFunc(messageHandler.SendMessage)))
	mux.Handle("GET /api/channels/{id}/messages", jwtMiddleware(http.HandlerFunc(messageHandler.FetchMessages)))
	mux.Handle("POST /api/channels/{id}/read", jwtMiddleware(http.HandlerFunc(messageHandler.MarkRead)))

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
