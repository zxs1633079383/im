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
	routing := gateway.NewRouting(rdb, gatewayID)

	// Pulsar client.
	pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
	if err != nil {
		log.Error("connect to pulsar", "error", err)
		return 1
	}
	defer pulsarClient.Close()

	// Per-topic producer cache for cross-pod push fan-out.
	producerCache := gateway.NewProducerCache(pulsarClient)
	defer producerCache.Close()

	// Environment controls Pulsar tenant/namespace for push topics.
	// prod / pre get dedicated namespaces; everything else falls into the
	// per-developer local bucket (see gateway.PushTopicFor).
	env := os.Getenv("IM_ENV")
	if env == "" {
		env = "local"
	}
	log.Info("gateway environment", "env", env)

	// Push consumer subscribes to msg.push.{gatewayID}.
	pushConsumer := gateway.NewPushConsumer(hub, gatewayID, log)

	// WsHandler wires hub, routing, channelRepo, and JWT secret together.
	wsHandler := gateway.NewWsHandler(hub, routing, cfg.Gateway.JWTSecret, gatewayID, channelRepo, log)
	wsHandler.WithSendSupport(messageRepo, channelRepo)

	mux := http.NewServeMux()

	// WebSocket route.
	mux.Handle("GET /ws", wsHandler)

	// All HTTP API endpoints are now served by Gin (Phase 6 + Phase 7.1–7.8).
	// Profile + Settings: Phase 7.1. Friend + user-search: Phase 7.2.
	// Channel: Phase 7.3. Message + forward: Phase 7.4. Sync: Phase 7.5.
	// Search: Phase 7.6. File: Phase 7.7. Favorites: Phase 7.8.
	// The legacy mux retains only the WebSocket route.

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

	// Shared cross-pod push dependencies for every hook below. Each pusher
	// struct holds the same set of deps so any fan-out (friend / channel /
	// message / read-sync) can reach a user attached to another pod via
	// Pulsar when the target isn't local.
	xpod := crossPodDeps{
		hub:       hub,
		routing:   routing,
		cache:     producerCache,
		gatewayID: gatewayID,
		env:       env,
		log:       log,
	}

	// Phase 7.2 cut-over: friend + user-search endpoints. The pusher hook is
	// preserved (legacy WithEventPusher) so the addressee still receives a
	// real-time WebSocket notification on a new friend request.
	friendSvc := service.NewFriendService(friendRepo, userRepo)
	imhttp.RegisterFriendRoutes(authedAPI, friendSvc, &hubFriendEventPusher{xpod: xpod})

	// Phase 7.3 cut-over: channel endpoints. The pusher hook is preserved
	// (legacy WithEventPusher) so newly added members still receive a
	// real-time WebSocket "added" event.
	channelSvc := service.NewChannelService(channelRepo, userRepo)
	imhttp.RegisterChannelRoutes(authedAPI, channelSvc, &hubChannelEventPusher{xpod: xpod})

	// M2-A: fine-grained channel governance (patch, managers, pins, role/notify).
	governanceRepo := repo.NewChannelGovernanceRepo(gormDB)
	governanceSvc := service.NewChannelGovernanceService(channelRepo, governanceRepo, userRepo)
	imhttp.RegisterChannelGovernanceRoutes(authedAPI, governanceSvc, &hubChannelEventPusher{xpod: xpod})

	// Phase 7.4 cut-over: message endpoints. All three legacy hooks are
	// preserved — Pusher fans new messages out to online members, ReadSyncer
	// echoes read receipts to other devices of the same user, and the file
	// repo handles attachment linkage on send.
	messageSvc := service.NewMessageService(messageRepo, channelRepo, fileRepo)
	msgBroadcaster := &hubEventBroadcaster{xpod: xpod, svc: messageSvc}
	imhttp.RegisterMessageRoutes(authedAPI, messageSvc, imhttp.MessageRouteOpts{
		Pusher:      &hubMessagePusher{xpod: xpod},
		ReadSyncer:  &hubReadSyncer{xpod: xpod},
		Broadcaster: msgBroadcaster,
		Logger:      log,
	})

	// M2-B: channel announcements. Re-uses the message broadcaster so
	// announcement_posted fans out to the same member set as msg_updated.
	announcementRepo := repo.NewAnnouncementRepo(gormDB)
	announcementSvc := service.NewAnnouncementService(announcementRepo, channelRepo, governanceSvc)
	imhttp.RegisterAnnouncementRoutes(authedAPI, announcementSvc, msgBroadcaster)

	// M2-C: urgent messages (send/confirm/cancel + list confirmations).
	urgentRepo := repo.NewUrgentRepo(gormDB)
	urgentSvc := service.NewUrgentService(urgentRepo, messageRepo, channelRepo, messageSvc, governanceSvc)
	imhttp.RegisterUrgentRoutes(authedAPI, urgentSvc, msgBroadcaster)

	// M2-D: approvals (request / approve / reject / cancel + list).
	// The user-push adapter delivers approval_updated events to the
	// requester + approver via the same cross-pod dispatch path used by
	// friend / channel events.
	approvalRepo := repo.NewApprovalRepo(gormDB)
	approvalSvc := service.NewApprovalService(approvalRepo, channelRepo, governanceSvc)
	userPusher := &hubUserEventPusher{xpod: xpod}
	imhttp.RegisterApprovalRoutes(authedAPI, approvalSvc, userPusher)

	// M2-E: notifications — per-user inbox/outbox + mark-read.
	notificationRepo := repo.NewNotificationRepo(gormDB)
	notificationSvc := service.NewNotificationService(notificationRepo, userRepo)
	imhttp.RegisterNotificationRoutes(authedAPI, notificationSvc, userPusher)

	// Phase 7.5 cut-over: batch incremental sync. No real-time hooks — sync is
	// pure pull, the algorithm + response shape are preserved verbatim from
	// the legacy SyncHandler.
	syncSvc := service.NewSyncService(channelRepo, messageRepo)
	imhttp.RegisterSyncRoutes(authedAPI, syncSvc, log)

	// Phase 7.6 cut-over: multi-type search (messages/users/channels). No
	// real-time hooks — search is pure read, the per-type fan-out + response
	// shape are preserved verbatim from the legacy SearchHandler.
	searchSvc := service.NewSearchService(searchRepo)
	imhttp.RegisterSearchRoutes(authedAPI, searchSvc, log)

	// Phase 7.7 cut-over: file upload/download/list-attachments. The service
	// owns disk writes (uploadDir layout preserved verbatim) and metadata
	// inserts. No real-time hooks — file routes are pure CRUD over storage.
	fileSvc := service.NewFileService(fileRepo, cfg.Gateway.UploadDir)
	imhttp.RegisterFileRoutes(authedAPI, fileSvc, log)

	// Phase 7.8 cut-over: favorites add/remove/list. With this slice the
	// internal/handler package is fully retired — every HTTP API endpoint
	// now runs on Gin.
	favoriteSvc := service.NewFavoriteService(favoriteRepo)
	imhttp.RegisterFavoriteRoutes(authedAPI, favoriteSvc)

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

// crossPodDeps bundles the shared state every HTTP-side pusher needs to fan
// events out across pods via Pulsar. A single value is built in run() and
// copied (by value — all fields are pointers) into each pusher struct.
type crossPodDeps struct {
	hub       *gateway.Hub
	routing   *gateway.Routing
	cache     *gateway.ProducerCache
	gatewayID string
	env       string
	log       *slog.Logger
}

// dispatch delivers (msgType, payload) to userID via the local hub when
// possible, and falls back to cross-pod Pulsar fan-out otherwise.
func (x crossPodDeps) dispatch(userID int64, msgType gateway.WSMessageType, payload any) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	x.hub.CrossPodPush(ctx, userID, msgType, payload,
		x.routing, x.cache, x.gatewayID, x.env, x.log)
}

// hubMessagePusher adapts crossPodDeps to imhttp.MessagePusher.
type hubMessagePusher struct {
	xpod crossPodDeps
}

func (p *hubMessagePusher) PushMessage(userID int64, msg *repo.Message) {
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
	p.xpod.dispatch(userID, gateway.TypePushMsg, payload)
}

// hubReadSyncer adapts crossPodDeps to imhttp.ReadSyncPusher.
type hubReadSyncer struct {
	xpod crossPodDeps
}

func (s *hubReadSyncer) PushReadSync(userID, channelID, readSeq int64) {
	s.xpod.dispatch(userID, gateway.TypeReadSync, gateway.ReadSyncPayload{
		ChannelID: channelID,
		ReadSeq:   readSeq,
	})
}

// hubFriendEventPusher adapts crossPodDeps to imhttp.FriendEventPusher.
type hubFriendEventPusher struct {
	xpod crossPodDeps
}

func (p *hubFriendEventPusher) PushFriendEvent(targetUserID int64, eventType string, fromUserID int64) {
	p.xpod.dispatch(targetUserID, gateway.TypeFriendEvent, gateway.FriendEventPayload{
		EventType:  eventType,
		FromUserID: fromUserID,
	})
}

// hubChannelEventPusher adapts crossPodDeps to imhttp.ChannelEventPusher.
type hubChannelEventPusher struct {
	xpod crossPodDeps
}

func (p *hubChannelEventPusher) PushChannelEvent(targetUserID int64, eventType string, channelID int64, name string) {
	p.xpod.dispatch(targetUserID, gateway.TypeChannelEvent, gateway.ChannelEventPayload{
		EventType: eventType,
		ChannelID: channelID,
		Name:      name,
	})
}

// hubUserEventPusher implements imhttp.UserEventPusher by dispatching a WS
// event to a single user via the shared cross-pod hub. Used by M2-D (approval)
// and M2-E (notification) where audiences are explicit user IDs rather than
// channel members.
type hubUserEventPusher struct {
	xpod crossPodDeps
}

func (p *hubUserEventPusher) PushToUser(userID int64, eventType imhttp.MessageEventType, payload any) {
	p.xpod.dispatch(userID, gateway.WSMessageType(eventType), payload)
}

// hubEventBroadcaster implements imhttp.MessageEventBroadcaster. It fans
// arbitrary WS events (msg_updated / msg_deleted) to every member of a
// channel by enumerating via the message service and pushing through the
// shared cross-pod dispatch.
type hubEventBroadcaster struct {
	xpod crossPodDeps
	svc  *service.MessageService
}

func (b *hubEventBroadcaster) BroadcastToMembers(channelID int64, eventType imhttp.MessageEventType, payload any) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	members, err := b.svc.ListMembers(ctx, channelID)
	if err != nil {
		b.xpod.log.Warn("broadcast: list members failed", "error", err, "channel_id", channelID)
		return
	}
	for _, m := range members {
		b.xpod.dispatch(m.UserID, gateway.WSMessageType(eventType), payload)
	}
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
