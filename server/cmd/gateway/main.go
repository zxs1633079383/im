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
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/observability"
	imPulsar "im-server/internal/pulsar"
	"im-server/internal/repo"
	"im-server/internal/service"

	"github.com/gin-gonic/gin"
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

	// Open the GORM-backed Postgres connection. The repo package owns the
	// pool; we close the underlying *sql.DB on shutdown.
	gormDB, err := repo.Open(repo.Config{DSN: cfg.PG.DSN, MaxOpen: cfg.PG.MaxConns})
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer func() {
		if sqlDB, e := gormDB.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()

	// Construct repositories. UserSettingsRepo is split out from UserRepo
	// in the new repo package (the legacy store.UserStore conflated both).
	userRepo := repo.NewUserRepo(gormDB)
	userSettingsRepo := repo.NewUserSettingsRepo(gormDB)
	channelRepo := repo.NewChannelRepo(gormDB)
	messageRepo := repo.NewMessageRepo(gormDB, channelRepo)
	friendRepo := repo.NewFriendshipRepo(gormDB)
	favoriteRepo := repo.NewFavoriteRepo(gormDB)
	fileRepo := repo.NewFileRepo(gormDB)
	searchRepo := repo.NewSearchRepo(gormDB)

	jwtMiddleware := middleware.JWTAuth(cfg.Gateway.JWTSecret)

	messageHandler := handler.NewMessageHandler(messageRepo, channelRepo, log)
	favoriteHandler := handler.NewFavoriteHandler(favoriteRepo, log)
	fileHandler := handler.NewFileHandler(fileRepo, cfg.Gateway.UploadDir, log)
	syncHandler := handler.NewSyncHandler(channelRepo, messageRepo, log)

	// Redis connection for routing.
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rdb, err := repo.OpenRedis(redisCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
	redisCancel()
	if err != nil {
		log.Error("connect to redis", "error", err)
		return 1
	}

	// Hub and routing.
	hub := gateway.NewHub()
	messageHandler.WithReadSyncer(&hubReadSyncer{hub: hub})
	messageHandler.WithAttachments(fileRepo)
	messageHandler.WithPusher(channelRepo, &hubMessagePusher{hub: hub})
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

	// WsHandler wires hub, routing, channelRepo, and JWT secret together.
	wsHandler := gateway.NewWsHandler(hub, routing, cfg.Gateway.JWTSecret, gatewayID, channelRepo, log)
	wsHandler.WithSendSupport(messageRepo, channelRepo)

	mux := http.NewServeMux()

	// WebSocket route.
	mux.Handle("GET /ws", wsHandler)

	// Profile + Settings routes are served by Gin in Phase 7.1 (see below).
	// Friend + user-search routes are served by Gin in Phase 7.2 (see below).
	// Channel routes are served by Gin in Phase 7.3 (see below).

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

	// Search route (JWT protected). SearchRepo satisfies all three handler
	// search interfaces; pass it three times.
	searchHandler := handler.NewSearchHandler(searchRepo, searchRepo, searchRepo, log)
	mux.Handle("GET /api/search", jwtMiddleware(http.HandlerFunc(searchHandler.Search)))

	// CORS middleware for development
	corsHandler := corsMiddleware(mux)

	// Wrap the legacy mux (with CORS) in a Gin engine that adds /healthz, /readyz,
	// otelgin tracing, and acts as the entry point for new Gin-native handlers
	// added in Phase 6+. Unmatched routes fall through to corsHandler -> mux.
	engine := imhttp.New(imhttp.Config{
		ServiceName: "im-gateway",
		Legacy:      corsHandler,
		Mode:        gin.ReleaseMode,
	})

	// Phase 6 cut-over: auth endpoints now run on Gin, in front of the legacy
	// mux fallthrough. /api/auth/{register,login,me} are served by Gin.
	authSvc := service.NewAuthService(userRepo, cfg.Gateway.JWTSecret)
	imhttp.RegisterAuthRoutes(engine, authSvc, userRepo, cfg.Gateway.JWTSecret)

	// Phase 7.1 cut-over: profile + settings endpoints. These share a single
	// JWT-protected /api group so the middleware is constructed once.
	profileSvc := service.NewProfileService(userRepo)
	settingsSvc := service.NewSettingsService(userSettingsRepo)
	authedAPI := engine.Group("/api")
	authedAPI.Use(middleware.JWTGin(cfg.Gateway.JWTSecret))
	imhttp.RegisterProfileRoutes(authedAPI, profileSvc)
	imhttp.RegisterSettingsRoutes(authedAPI, settingsSvc)

	// Phase 7.2 cut-over: friend + user-search endpoints. The pusher hook is
	// preserved (legacy WithEventPusher) so the addressee still receives a
	// real-time WebSocket notification on a new friend request.
	friendSvc := service.NewFriendService(friendRepo, userRepo)
	imhttp.RegisterFriendRoutes(authedAPI, friendSvc, &hubFriendEventPusher{hub: hub})

	// Phase 7.3 cut-over: channel endpoints. The pusher hook is preserved
	// (legacy WithEventPusher) so newly added members still receive a
	// real-time WebSocket "added" event.
	channelSvc := service.NewChannelService(channelRepo, userRepo)
	imhttp.RegisterChannelRoutes(authedAPI, channelSvc, &hubChannelEventPusher{hub: hub})

	srv := &http.Server{
		Addr:         cfg.Gateway.HTTPAddr,
		Handler:      engine,
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

func (p *hubMessagePusher) PushMessage(userID int64, msg *repo.Message) { //nolint:unused
	payload := gateway.PushMsgPayload{
		PushID:    fmt.Sprintf("http-%d-%d", msg.ChannelID, msg.Seq),
		ChannelID: msg.ChannelID,
		Seq:       msg.Seq,
		ServerID:  msg.ID,
		SenderID:  msg.SenderID,
		Content:   msg.Content,
		MsgType:   msg.MsgType,
		VisibleTo: []int64(msg.VisibleTo),
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

// hubFriendEventPusher adapts *gateway.Hub to imhttp.FriendEventPusher.
type hubFriendEventPusher struct {
	hub *gateway.Hub
}

func (p *hubFriendEventPusher) PushFriendEvent(targetUserID int64, eventType string, fromUserID int64) {
	p.hub.PushToUser(targetUserID, gateway.TypeFriendEvent, gateway.FriendEventPayload{
		EventType:  eventType,
		FromUserID: fromUserID,
	})
}

// hubChannelEventPusher adapts *gateway.Hub to imhttp.ChannelEventPusher.
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
