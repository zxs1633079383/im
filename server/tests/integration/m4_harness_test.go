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
	"log/slog"
	"testing"
	"time"

	"github.com/gavv/httpexpect/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

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

	engine := buildEngine(buildEngineDeps{
		rdb:        rdb,
		channels:   channelRepo,
		messages:   messageRepo,
		friends:    friendRepo,
		files:      fileRepo,
		favorites:  favoriteRepo,
		urgents:    urgentRepo,
		governance: governanceRepo,
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
	rdb        redis.UniversalClient
	channels   repo.ChannelRepo
	messages   repo.MessageRepo
	friends    repo.FriendshipRepo
	files      repo.FileRepo
	favorites  repo.FavoriteRepo
	urgents    repo.UrgentRepo
	governance repo.ChannelGovernanceRepo
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
	imhttp.RegisterAuthRoutes(engine, middleware.MattermostCookieResolve(d.rdb, log))

	authed := engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(d.rdb, log))
	authed.Use(middleware.CookieRequired())

	channelSvc := service.NewChannelService(d.channels, d.messages)
	imhttp.RegisterChannelRoutes(authed, channelSvc, nil)

	messageSvc := service.NewMessageService(d.messages, d.channels, d.files)
	imhttp.RegisterMessageRoutes(authed, messageSvc, imhttp.MessageRouteOpts{})

	syncSvc := service.NewSyncService(d.channels, d.messages)
	imhttp.RegisterSyncRoutes(authed, syncSvc, log)

	friendSvc := service.NewFriendService(d.friends)
	imhttp.RegisterFriendRoutes(authed, friendSvc, nil)

	governanceSvc := service.NewChannelGovernanceService(d.channels, d.governance)
	imhttp.RegisterChannelGovernanceRoutes(authed, governanceSvc, nil)

	favoriteSvc := service.NewFavoriteService(d.favorites)
	imhttp.RegisterFavoriteRoutes(authed, favoriteSvc)

	urgentSvc := service.NewUrgentService(d.urgents, d.messages, d.channels, messageSvc, governanceSvc)
	imhttp.RegisterUrgentRoutes(authed, urgentSvc, nil)

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
// returns the channel id.
func (e *m4env) seedDM(ownerCookie, peerID string) int64 {
	e.t.Helper()
	dm := e.expect.POST("/api/channels/dm").
		WithHeader(middleware.MMCookieHeader, ownerCookie).
		WithJSON(map[string]any{"peer_id": peerID}).
		Expect().Status(201).JSON().Object()
	return int64(dm.Value("id").Number().Raw())
}

// seedGroup creates a group channel owned by ownerCookie with the given member
// IDs (owner is auto-added), returning the channel id.
func (e *m4env) seedGroup(ownerCookie string, name string, memberIDs ...string) int64 {
	e.t.Helper()
	body := map[string]any{
		"name":       name,
		"member_ids": memberIDs,
	}
	resp := e.expect.POST("/api/channels").
		WithHeader(middleware.MMCookieHeader, ownerCookie).
		WithJSON(body).
		Expect().Status(201).JSON().Object()
	return int64(resp.Value("id").Number().Raw())
}

// seedMessage inserts a plain text message via the repo (bypassing HTTP) and
// returns the persisted *repo.Message — Batch-B tests use this to set up
// "this message exists" preconditions without paying the full
// POST /messages cost when the message contents are not under test.
func (e *m4env) seedMessage(channelID int64, senderID, content string) *repo.Message {
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
