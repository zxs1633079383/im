//go:build integration

// Package integration — M4 happy-path harness.
//
// Each test gets its own Postgres + Redis testcontainers (so subtests can
// run -parallel without cross-talk) and a fully wired Gin engine that
// mirrors cmd/gateway/main.go's HTTP surface — minus WebSocket, scheduled
// worker, Pulsar — none of which the happy paths exercise.
//
// Auth runs entirely on Mattermost cookieId. seedUser registers a fresh
// fixture in Redis and returns the cookieId header value the caller should
// stamp on each request. RealCookieID + RealUserID + RealCompanyID is the
// canonical 张立超 fixture defined in internal/testutil/cookie_fixture.go.
package integration

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/gavv/httpexpect/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"im-server/internal/gateway"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

// m4env bundles the per-test plumbing. Tests reach into repos / svcs only
// when they need to assert on persisted state directly; HTTP behaviour is
// asserted via expect.
type m4env struct {
	t          *testing.T
	db         *gorm.DB
	rdb        redis.UniversalClient
	engine     *gin.Engine
	expect     *httpexpect.Expect
	channels   repo.ChannelRepo
	messages   repo.MessageRepo
	friends    repo.FriendshipRepo
	favorites  repo.FavoriteRepo
	urgents    repo.UrgentRepo
	governance repo.ChannelGovernanceRepo
	hub        *gateway.Hub
	routing    *gateway.Routing
}

// newM4Env builds a fresh environment. Container creation is the dominant
// cost (~6-10s for postgres + redis), so prefer one env per top-level test
// function and exercise multiple cases as subtests.
func newM4Env(t *testing.T) *m4env {
	t.Helper()

	db := openTestPostgres(t)
	rdb := openTestRedis(t)

	channelRepo := repo.NewChannelRepo(db)
	messageRepo := repo.NewMessageRepo(db, channelRepo)
	friendRepo := repo.NewFriendshipRepo(db)
	fileRepo := repo.NewFileRepo(db)
	favoriteRepo := repo.NewFavoriteRepo(db)
	urgentRepo := repo.NewUrgentRepo(db)
	governanceRepo := repo.NewChannelGovernanceRepo(db)
	announcementRepo := repo.NewAnnouncementRepo(db)
	approvalRepo := repo.NewApprovalRepo(db)
	notificationRepo := repo.NewNotificationRepo(db)
	quickReplyRepo := repo.NewQuickReplyRepo(db)
	reactionRepo := repo.NewReactionRepo(db)
	scheduledRepo := repo.NewScheduledRepo(db)

	hub := gateway.NewHub()
	routing := repo.NewRouting(rdb, "test-gw")

	engine := buildEngine(buildEngineDeps{
		db:            db,
		rdb:           rdb,
		channels:      channelRepo,
		messages:      messageRepo,
		friends:       friendRepo,
		files:         fileRepo,
		favorites:     favoriteRepo,
		urgents:       urgentRepo,
		governance:    governanceRepo,
		announcements: announcementRepo,
		approvals:     approvalRepo,
		notifications: notificationRepo,
		quickReplies:  quickReplyRepo,
		reactions:     reactionRepo,
		scheduledMsgs: scheduledRepo,
		hub:           hub,
		routing:       routing,
	})

	return &m4env{
		t:          t,
		db:         db,
		rdb:        rdb,
		engine:     engine,
		expect:     testutil.NewExpect(t, engine),
		channels:   channelRepo,
		messages:   messageRepo,
		friends:    friendRepo,
		favorites:  favoriteRepo,
		urgents:    urgentRepo,
		governance: governanceRepo,
		hub:        hub,
		routing:    routing,
	}
}

// openTestPostgres spins up a Postgres testcontainer with every migration
// (including 014) applied and returns the wired *gorm.DB. Cleanup is
// registered with t.
func openTestPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// openTestRedis spins up a Redis testcontainer and returns a UniversalClient
// wired against it. Cleanup is registered with t.
func openTestRedis(t *testing.T) redis.UniversalClient {
	t.Helper()
	uri := containers.StartRedis(t)
	opts, err := redis.ParseURL(uri)
	require.NoError(t, err)
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { _ = rdb.Close() })

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, rdb.Ping(pingCtx).Err())
	return rdb
}

// buildEngineDeps bundles every repo m4 harness needs to wire. Add fields
// here when a new endpoint family joins Batch-B/C/D/E so test files stay
// untouched.
type buildEngineDeps struct {
	db            *gorm.DB // 2026-05-17 Phase P4 cleanup: SyncService 需 channel_event repo
	rdb           redis.UniversalClient
	channels      repo.ChannelRepo
	messages      repo.MessageRepo
	friends       repo.FriendshipRepo
	files         repo.FileRepo
	favorites     repo.FavoriteRepo
	urgents       repo.UrgentRepo
	governance    repo.ChannelGovernanceRepo
	announcements repo.AnnouncementRepo
	approvals     repo.ApprovalRepo
	notifications repo.NotificationRepo
	quickReplies  repo.QuickReplyRepo
	reactions     repo.ReactionRepo
	scheduledMsgs repo.ScheduledRepo
	hub           *gateway.Hub
	routing       *gateway.Routing
}

// buildEngine wires the Gin handler tree exactly the way cmd/gateway/main.go
// does for the routes the M4 happy paths + Batch-B exercise: auth, channels,
// messages (incl. template-received + read-stats), sync, friends, channel
// governance, favorites, urgent. Real-time pushers / broadcasters are nil —
// happy paths read response bodies + DB state, not WebSocket fan-out.
func buildEngine(d buildEngineDeps) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	log := slog.Default()

	// Mirror cmd/gateway/main.go::buildRouter — every JSON response gets
	// wrapped into the cses-shape {status,data?,error?} envelope so test
	// assertions exercise the same body shape the cses-client receives in
	// production. See docs/harness/C007 §4.2 (handler 单测必须断言 envelope
	// 后的最终 body) and internal/http/response_envelope.go for the contract.
	engine.Use(imhttp.ResponseEnvelope())

	imhttp.RegisterAuthRoutes(engine, middleware.MattermostCookieResolve(d.rdb, log))

	authed := engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(d.rdb, log))
	authed.Use(middleware.CookieRequired())

	// WS push adapters: every Batch-D event reaches the connected wsClient
	// through these. Single-pod harness — no Pulsar fan-out.
	msgBroadcaster := &localBroadcaster{hub: d.hub, channels: d.channels}
	userPusher := &localUserEventPusher{hub: d.hub}
	channelPusher := &localChannelEventPusher{hub: d.hub}
	friendPusher := &localFriendEventPusher{hub: d.hub}
	reactionPusher := &localReactionPusher{hub: d.hub, channels: d.channels}

	channelSvc := service.NewChannelService(d.channels, d.messages)
	imhttp.RegisterChannelRoutes(authed, channelSvc, channelPusher)
	// v0.7.3 gap #4 / C013: wire the channel_member_updated broadcaster so
	// AddMember / RemoveMember / LeaveChannel / TransferOwner fan WS frames.
	channelSvc.AttachMemberBroadcaster(msgBroadcaster)
	// C013: owner-transfer endpoint. Service-side broadcaster is attached above.
	imhttp.RegisterChannelTransferOwnerRoute(authed, channelSvc, log)

	messageSvc := service.NewMessageService(d.messages, d.channels, d.files)
	imhttp.RegisterMessageRoutes(authed, messageSvc, imhttp.MessageRouteOpts{
		Broadcaster: msgBroadcaster,
		Pusher:      &localMessagePusher{hub: d.hub},
		ReadSyncer:  &localReadSyncPusher{hub: d.hub},
	})

	// Phase P4 cleanup (2026-05-17): NewSyncService 砍掉 v1 fallback 后
	// 必须传 channel_event repo。集成测试 fixtures 暂未生产 channel_event
	// 行，但 GetMemberChannelEventSeqs 返回空 map 时 Sync 自然走 0 channels 出
	// 参；body 形状从 v1 (server_seq / messages) 变成 v2 (server_event_seq
	// / events)，会让 TestM4Sync_HappyPath / TestM4MessageSendThenSync 红 ——
	// 留给主对话或后续 Phase 重写断言。当前 fix 仅保证 build 绿。
	channelEvents := repo.NewChannelEventRepo(d.db)
	syncSvc := service.NewSyncService(d.channels, d.messages, channelEvents)
	imhttp.RegisterSyncRoutes(authed, syncSvc, log)

	friendSvc := service.NewFriendService(d.friends)
	imhttp.RegisterFriendRoutes(authed, friendSvc, friendPusher)

	governanceSvc := service.NewChannelGovernanceService(d.channels, d.governance)
	imhttp.RegisterChannelGovernanceRoutes(authed, governanceSvc, channelPusher, msgBroadcaster, userPusher)

	favoriteSvc := service.NewFavoriteService(d.favorites)
	imhttp.RegisterFavoriteRoutes(authed, favoriteSvc)

	urgentSvc := service.NewUrgentService(d.urgents, d.messages, d.channels, messageSvc, governanceSvc)
	imhttp.RegisterUrgentRoutes(authed, urgentSvc, msgBroadcaster)

	// Batch-C: announcement / approval / notification / quick_reply / reaction / scheduled
	announcementSvc := service.NewAnnouncementService(d.announcements, d.channels, governanceSvc)
	imhttp.RegisterAnnouncementRoutes(authed, announcementSvc, msgBroadcaster)

	approvalSvc := service.NewApprovalService(d.approvals, d.channels, governanceSvc)
	imhttp.RegisterApprovalRoutes(authed, approvalSvc, userPusher)

	notificationSvc := service.NewNotificationService(d.notifications)
	imhttp.RegisterNotificationRoutes(authed, notificationSvc, userPusher)

	quickReplySvc := service.NewQuickReplyService(d.quickReplies)
	imhttp.RegisterQuickReplyRoutes(authed, quickReplySvc)

	reactionSvc := service.NewReactionService(d.reactions, d.messages, d.channels)
	imhttp.RegisterReactionRoutes(authed, reactionSvc, reactionPusher)

	scheduledSvc := service.NewScheduledService(d.scheduledMsgs, d.channels, messageSvc)
	imhttp.RegisterScheduledRoutes(authed, scheduledSvc)

	// Batch-D: WebSocket handler. Mirrors cmd/gateway/main.go::buildRouter
	// with cookie auth + send support enabled. JWT secret left blank since
	// the M4 cookieId path is the production wire shape.
	wsHandler := gateway.NewWsHandler(d.hub, d.routing, "", "test-gw", d.channels, log)
	wsHandler = wsHandler.WithCookieAuth(d.rdb).
		WithSendSupport(d.messages, d.channels)
	engine.GET("/ws", gin.WrapH(wsHandler))

	return engine
}

// seedUser registers a deterministic test identity in the env's Redis and
// returns (cookieId, userId). Use distinct seeds per top-level test so the
// process-global cookie LRU never serves stale data across tests.
func (e *m4env) seedUser(seed int) (cookieID, userID string) {
	e.t.Helper()
	cookieID = testutil.MakeCookieID(seed)
	userID = testutil.HexUserID(seed)
	testutil.CookieFixture(e.t, e.rdb, cookieID, userID, testutil.RealCompanyID)
	return cookieID, userID
}

// seedRealUser registers the canonical 张立超 fixture and returns its
// cookieId. Useful for the auth smoke that mirrors the manual e2e replay.
func (e *m4env) seedRealUser() string {
	e.t.Helper()
	return testutil.CookieFixture(e.t, e.rdb,
		testutil.RealCookieID, testutil.RealUserID, testutil.RealCompanyID)
}

// ---- Batch-B shared seed helpers --------------------------------------------
//
// These are the canonical fixtures the Batch-B integration tests use.  Every
// helper opens a 5s context internally, mirrors how production handlers run,
// and surfaces failures via require.NoError so tests fail fast on infra glitches
// (testcontainer DNS, Redis flake, etc.) rather than mis-attributing them to
// business bugs.

// seedDM creates a DM between owner and peer (both already seedUser-ed) and
// returns the channel id. Drills through the success envelope wrapper.
//
// C012 P-D: id is now TEXT (string) — read as String() rather than Number().
//
// v0.7.4: companyId moved from the Redis MattermostUser payload to the
// `companyId` request header (see middleware.MMTeamHeader). Without this
// header the DM row's team_id stays nil and downstream message inserts skip
// team_id denormalisation — breaking any caller that asserts on team_id.
// All Batch-B+ fixtures use RealCompanyID so we hard-code it here; tests
// that need a non-default team build their own POST.
func (e *m4env) seedDM(ownerCookie, peerID string) string {
	e.t.Helper()
	dm := successBody(e.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, ownerCookie).
		WithHeader(middleware.MMTeamHeader, testutil.RealCompanyID).
		WithJSON(map[string]any{"peer_id": peerID}).
		Expect().Status(201))
	return dm.Value("id").String().Raw()
}

// seedGroup creates a group channel owned by ownerCookie with the given member
// IDs (owner is auto-added), returning the channel id. Drills through the
// success envelope wrapper.
//
// C012 P-D: id is now TEXT (string).
func (e *m4env) seedGroup(ownerCookie string, name string, memberIDs ...string) string {
	e.t.Helper()
	body := map[string]any{
		"name":       name,
		"member_ids": memberIDs,
	}
	resp := successBody(e.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, ownerCookie).
		WithJSON(body).
		Expect().Status(201))
	return resp.Value("id").String().Raw()
}

// seedMessage inserts a plain text message via the repo (bypassing HTTP) and
// returns the persisted *repo.Message — Batch-B tests use this to set up
// "this message exists" preconditions without paying the full
// POST /messages cost when the message contents are not under test.
//
// C012 P-D: channelID is TEXT (string).
func (e *m4env) seedMessage(channelID string, senderID, content string) *repo.Message {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := &repo.Message{
		ChannelID: channelID,
		SenderID:  senderID,
		MsgType:   repo.MsgTypeText,
		Content:   content,
	}
	require.NoError(e.t, e.messages.Send(ctx, m))
	return m
}

// ---- Envelope assertion helpers ---------------------------------------------
//
// Every 2xx response is wrapped by the responseEnvelope middleware as
// {"status":"success","data":<original-body>}; every non-2xx becomes
// {"status":"error","error":"<message>"}. Tests should call successBody /
// successBodyArray to drill into the data sub-tree, and use the raw
// expect chain for non-2xx (the wrapper still surfaces top-level "error",
// so .Value("error") on a 4xx body keeps working without changes).
//
// See docs/harness/C007 §3 / response_envelope.go for the contract.

// successBody asserts a 2xx envelope shape and returns the data sub-object.
func successBody(resp *httpexpect.Response) *httpexpect.Object {
	obj := resp.JSON().Object()
	obj.Value("status").IsEqual("success")
	return obj.Value("data").Object()
}

// successBodyArray asserts a 2xx envelope and returns the data sub-array.
// Use this for endpoints whose data field is a JSON array (rare today; most
// list endpoints wrap their array under {items: [...]}/{stats: [...]} etc.).
func successBodyArray(resp *httpexpect.Response) *httpexpect.Array {
	obj := resp.JSON().Object()
	obj.Value("status").IsEqual("success")
	return obj.Value("data").Array()
}

// errorBody asserts a non-2xx envelope shape and returns the wrapper object;
// callers can chain .Value("error").String()... to assert on the message.
// 4xx assertions that already used .Value("error") keep working — the
// envelope re-emits the top-level error field unchanged.
func errorBody(resp *httpexpect.Response) *httpexpect.Object {
	obj := resp.JSON().Object()
	obj.Value("status").IsEqual("error")
	return obj
}

// ---- WS push adapters --------------------------------------------------------
//
// Production wires HTTP handler push hooks through cmd/gateway/main.go's
// hubEventBroadcaster / hubUserEventPusher / hubFriendEventPusher etc., which
// route through crossPodDeps for Pulsar fan-out. The integration harness is
// single-pod, so we adapt directly via hub.PushToUser + channel.ListMembers.

// localBroadcaster fans MessageEventBroadcaster events to every member of
// channelID by calling hub.PushToUser. Since gateway.WSMessageType and
// imhttp.MessageEventType are both string aliases, the conversion is
// 1-to-1 and the wire frame ends up the same as production.
type localBroadcaster struct {
	hub      *gateway.Hub
	channels repo.ChannelRepo
}

// C012 P-D: channelID is TEXT (string).
func (b *localBroadcaster) BroadcastToMembers(channelID string, eventType imhttp.MessageEventType, payload any) {
	b.fanout(channelID, gateway.WSMessageType(eventType), payload)
}

// BroadcastMemberEvent satisfies service.ChannelMemberBroadcaster — the local
// counterpart of cmd/gateway/main.go::hubEventBroadcaster.BroadcastMemberEvent.
// Reuses the same per-channel fan-out as BroadcastToMembers so tests can
// observe channel_member_updated frames from TransferOwner / AddMember /
// RemoveMember / LeaveChannel without standing up a hub.
func (b *localBroadcaster) BroadcastMemberEvent(channelID string, eventType string, payload any) {
	b.fanout(channelID, gateway.WSMessageType(eventType), payload)
}

// fanout pushes one frame per channel member. Lifted from the original
// BroadcastToMembers body so both broadcaster methods share the same wire.
func (b *localBroadcaster) fanout(channelID string, msgType gateway.WSMessageType, payload any) {
	members, err := b.channels.ListMembers(context.Background(), channelID)
	if err != nil {
		return
	}
	for _, m := range members {
		b.hub.PushToUser(m.UserID, msgType, payload)
	}
}

// localUserEventPusher pushes a per-user event (approvals / notifications)
// straight to the user's local conns.
type localUserEventPusher struct {
	hub *gateway.Hub
}

func (p *localUserEventPusher) PushToUser(userID string, eventType imhttp.MessageEventType, payload any) {
	p.hub.PushToUser(userID, gateway.WSMessageType(eventType), payload)
}

// localChannelEventPusher pushes channel-add events as gateway.TypeChannelEvent
// frames carrying gateway.ChannelEventPayload.
type localChannelEventPusher struct {
	hub *gateway.Hub
}

// C012 P-D: channelID is TEXT (string).
func (p *localChannelEventPusher) PushChannelEvent(targetUserID string, eventType string, channelID string, name string) {
	p.hub.PushToUser(targetUserID, gateway.TypeChannelEvent, gateway.ChannelEventPayload{
		EventType: eventType,
		ChannelID: channelID,
		Name:      name,
	})
}

// localFriendEventPusher pushes friend events as gateway.TypeFriendEvent frames.
type localFriendEventPusher struct {
	hub *gateway.Hub
}

func (p *localFriendEventPusher) PushFriendEvent(targetUserID, eventType, fromUserID string) {
	p.hub.PushToUser(targetUserID, gateway.TypeFriendEvent, gateway.FriendEventPayload{
		EventType:  eventType,
		FromUserID: fromUserID,
	})
}

// localReactionPusher fans reaction events to every member of the channel
// (matching production hubReactionPusher.BroadcastReaction shape).
type localReactionPusher struct {
	hub      *gateway.Hub
	channels repo.ChannelRepo
}

// C012 P-D: channelID is TEXT (string).
func (p *localReactionPusher) BroadcastReaction(channelID string, eventType imhttp.ReactionEventType, payload any) {
	members, err := p.channels.ListMembers(context.Background(), channelID)
	if err != nil {
		return
	}
	for _, m := range members {
		p.hub.PushToUser(m.UserID, gateway.WSMessageType(eventType), payload)
	}
}

// localMessagePusher implements imhttp.MessagePusher for new-message fan-out
// (TypePushMsg). Builds the gateway.PushMsgPayload identical to production's
// hubMessagePusher and fans to every userID on the local hub.
type localMessagePusher struct {
	hub *gateway.Hub
}

// C012 P-D: channelID is TEXT (string); push-id format also string-based.
func (p *localMessagePusher) BroadcastMessage(channelID string, userIDs []string, msg *repo.Message) {
	payload := gateway.PushMsgPayload{
		PushID:    fmt.Sprintf("test-%s-%s", msg.ChannelID, msg.ID),
		ChannelID: msg.ChannelID,
		Seq:       msg.Seq,
		ServerID:  msg.ID,
		SenderID:  msg.SenderID,
		Content:   msg.Content,
		MsgType:   msg.MsgType,
		VisibleTo: []string(msg.VisibleTo),
		CreatedAt: msg.CreatedAt,
	}
	for _, uid := range userIDs {
		p.hub.PushToUser(uid, gateway.TypePushMsg, payload)
	}
}

// localReadSyncPusher implements imhttp.ReadSyncPusher: same-user multi-device
// read sync. Pushes ReadSyncPayload onto every conn the user has on this hub.
type localReadSyncPusher struct {
	hub *gateway.Hub
}

// C012 P-D: channelID is TEXT (string); readSeq stays int64 (counter).
func (p *localReadSyncPusher) PushReadSync(userID string, channelID string, readSeq int64) {
	p.hub.PushToUser(userID, gateway.TypeReadSync, gateway.ReadSyncPayload{
		ChannelID: channelID,
		ReadSeq:   readSeq,
	})
}
