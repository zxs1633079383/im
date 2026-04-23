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
	"im-server/internal/gateway"
	"im-server/internal/handler"
	"im-server/internal/middleware"
	"im-server/internal/model"
	"im-server/internal/observability"
	imPulsar "im-server/internal/pulsar"
	"im-server/internal/store"
)

func main() {
	fmt.Println("gateway starting...")
	os.Exit(run())
}

func run() int {
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(observability.NewTraceHandler(baseHandler))
	slog.SetDefault(log)

	otelShutdown, err := observability.Init(context.Background(), observability.Config{
		ServiceName:    "im-gateway",
		ServiceVersion: "dev",
		Disabled:       os.Getenv("OTEL_DISABLED") == "true",
	})
	if err != nil {
		log.Error("otel init", "error", err)
		return 1
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(shutCtx)
	}()

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

	// Resolve gateway ID (from config, HOSTNAME env, or random UUID).
	gatewayID := config.ResolveGatewayID(cfg)
	log.Info("gateway id resolved", "gateway_id", gatewayID)

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
	profileHandler := handler.NewProfileHandler(userStore, log)
	settingsHandler := handler.NewSettingsHandler(userStore, log)
	jwtMiddleware := middleware.JWTAuth(cfg.Gateway.JWTSecret)

	friendStore := store.NewFriendshipStore(pool)
	friendHandler := handler.NewFriendHandler(friendStore, userStore, log)
	// Note: eventPusher wired below after hub creation.

	channelStore := store.NewChannelStore(pool)
	channelHandler := handler.NewChannelHandler(channelStore, userStore, log)
	// Note: channelEventPusher wired below after hub creation.

	messageStore := store.NewMessageStore(pool)
	messageHandler := handler.NewMessageHandler(messageStore, channelStore, log)

	favoriteStore := store.NewFavoriteStore(pool)
	favoriteHandler := handler.NewFavoriteHandler(favoriteStore, log)

	fileStore := store.NewFileStore(pool)
	fileHandler := handler.NewFileHandler(fileStore, cfg.Gateway.UploadDir, log)

	syncHandler := handler.NewSyncHandler(channelStore, messageStore, log)

	// Redis connection for routing.
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rdb, err := store.NewRedisClient(redisCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	redisCancel()
	if err != nil {
		log.Error("connect to redis", "error", err)
		return 1
	}

	// Hub and routing.
	hub := gateway.NewHub()
	messageHandler.WithReadSyncer(&hubReadSyncer{hub: hub})
	messageHandler.WithAttachments(fileStore)
	messageHandler.WithPusher(channelStore, &hubMessagePusher{hub: hub})
	friendHandler.WithEventPusher(&hubFriendEventPusher{hub: hub})
	channelHandler.WithEventPusher(&hubChannelEventPusher{hub: hub})
	routing := gateway.NewRouting(rdb, gatewayID)

	// Pulsar client.
	pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
	if err != nil {
		log.Error("connect to pulsar", "error", err)
		return 1
	}
	defer pulsarClient.Close()

	// Push consumer subscribes to msg.push.{gatewayID}.
	pushConsumer := gateway.NewPushConsumer(hub, gatewayID, log)

	// WsHandler wires hub, routing, channelStore, and JWT secret together.
	wsHandler := gateway.NewWsHandler(hub, routing, cfg.Gateway.JWTSecret, gatewayID, channelStore, log)
	wsHandler.WithSendSupport(messageStore, channelStore)

	mux := http.NewServeMux()

	// WebSocket route.
	mux.Handle("GET /ws", wsHandler)

	// Public auth routes
	mux.HandleFunc("POST /api/auth/register", authHandler.Register)
	mux.HandleFunc("POST /api/auth/login", authHandler.Login)

	// Protected routes
	mux.Handle("GET /api/auth/me", jwtMiddleware(http.HandlerFunc(authHandler.Me)))

	// Profile route (JWT protected)
	mux.Handle("PUT /api/users/me", jwtMiddleware(http.HandlerFunc(profileHandler.UpdateMe)))

	// Settings routes (JWT protected)
	mux.Handle("GET /api/settings", jwtMiddleware(http.HandlerFunc(settingsHandler.GetSettings)))
	mux.Handle("PUT /api/settings", jwtMiddleware(http.HandlerFunc(settingsHandler.UpdateSettings)))

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

	// Forward route (JWT protected)
	mux.Handle("POST /api/messages/forward", jwtMiddleware(http.HandlerFunc(messageHandler.ForwardMessages)))

	// Favorite routes (JWT protected)
	mux.Handle("POST /api/favorites/{message_id}", jwtMiddleware(http.HandlerFunc(favoriteHandler.AddFavorite)))
	mux.Handle("DELETE /api/favorites/{message_id}", jwtMiddleware(http.HandlerFunc(favoriteHandler.RemoveFavorite)))
	mux.Handle("GET /api/favorites", jwtMiddleware(http.HandlerFunc(favoriteHandler.ListFavorites)))

	// File routes (JWT protected)
	mux.Handle("POST /api/files", jwtMiddleware(http.HandlerFunc(fileHandler.Upload)))
	mux.Handle("GET /api/files/{id}", jwtMiddleware(http.HandlerFunc(fileHandler.Download)))
	mux.Handle("GET /api/messages/{id}/attachments", jwtMiddleware(http.HandlerFunc(fileHandler.ListAttachments)))

	// Sync route (JWT protected)
	mux.Handle("POST /api/sync", jwtMiddleware(http.HandlerFunc(syncHandler.Sync)))

	// Search route (JWT protected)
	searchStore := store.NewSearchStore(pool)
	searchHandler := handler.NewSearchHandler(searchStore, searchStore, searchStore, log)
	mux.Handle("GET /api/search", jwtMiddleware(http.HandlerFunc(searchHandler.Search)))

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

	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()

	// Start Pulsar push consumer in a goroutine.
	if err := pushConsumer.Start(runCtx, pulsarClient); err != nil {
		log.Error("start push consumer", "error", err)
		return 1
	}
	log.Info("push consumer started", "topic", pushConsumer.Topic())

	go func() {
		log.Info("HTTP server listening", "addr", cfg.Gateway.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	log.Info("shutting down...")
	runCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown error", "error", err)
		return 1
	}

	return 0
}

// hubMessagePusher adapts *gateway.Hub to handler.MessagePusher.
type hubMessagePusher struct {
	hub *gateway.Hub
}

func (p *hubMessagePusher) PushMessage(userID int64, msg *model.Message) { //nolint:unused
	payload := gateway.PushMsgPayload{
		PushID:    fmt.Sprintf("http-%d-%d", msg.ChannelID, msg.Seq),
		ChannelID: msg.ChannelID,
		Seq:       msg.Seq,
		ServerID:  msg.ID,
		SenderID:  msg.SenderID,
		Content:   msg.Content,
		MsgType:   int16(msg.MsgType),
		VisibleTo: msg.VisibleTo,
		CreatedAt: msg.CreatedAt,
	}
	p.hub.PushToUser(userID, gateway.TypePushMsg, payload)
}

// hubReadSyncer adapts *gateway.Hub to handler.ReadSyncPusher.
type hubReadSyncer struct {
	hub *gateway.Hub
}

func (s *hubReadSyncer) PushReadSync(userID, channelID, readSeq int64) {
	s.hub.PushToUser(userID, gateway.TypeReadSync, gateway.ReadSyncPayload{
		ChannelID: channelID,
		ReadSeq:   readSeq,
	})
}

// hubFriendEventPusher adapts *gateway.Hub to handler.FriendEventPusher.
type hubFriendEventPusher struct {
	hub *gateway.Hub
}

func (p *hubFriendEventPusher) PushFriendEvent(targetUserID int64, eventType string, fromUserID int64) {
	p.hub.PushToUser(targetUserID, gateway.TypeFriendEvent, gateway.FriendEventPayload{
		EventType:  eventType,
		FromUserID: fromUserID,
	})
}

// hubChannelEventPusher adapts *gateway.Hub to handler.ChannelEventPusher.
type hubChannelEventPusher struct {
	hub *gateway.Hub
}

func (p *hubChannelEventPusher) PushChannelEvent(targetUserID int64, eventType string, channelID int64, name string) {
	p.hub.PushToUser(targetUserID, gateway.TypeChannelEvent, gateway.ChannelEventPayload{
		EventType: eventType,
		ChannelID: channelID,
		Name:      name,
	})
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
