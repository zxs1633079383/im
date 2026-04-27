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
	t        *testing.T
	db       *gorm.DB
	rdb      redis.UniversalClient
	engine   *gin.Engine
	expect   *httpexpect.Expect
	channels repo.ChannelRepo
	messages repo.MessageRepo
	friends  repo.FriendshipRepo
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

	engine := buildEngine(rdb, channelRepo, messageRepo, friendRepo, fileRepo)

	return &m4env{
		t:        t,
		db:       db,
		rdb:      rdb,
		engine:   engine,
		expect:   testutil.NewExpect(t, engine),
		channels: channelRepo,
		messages: messageRepo,
		friends:  friendRepo,
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

// buildEngine wires the Gin handler tree exactly the way cmd/gateway/main.go
// does for the routes the M4 happy paths exercise: auth, channels (incl.
// topics), messages, sync, friends. Real-time pushers are nil — happy
// paths read response bodies + DB state, not WebSocket fan-out.
func buildEngine(
	rdb redis.UniversalClient,
	channels repo.ChannelRepo,
	messages repo.MessageRepo,
	friends repo.FriendshipRepo,
	files repo.FileRepo,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	log := slog.Default()
	imhttp.RegisterAuthRoutes(engine, middleware.MattermostCookieResolve(rdb, log))

	authed := engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(rdb, log))
	authed.Use(middleware.CookieRequired())

	channelSvc := service.NewChannelService(channels, messages)
	imhttp.RegisterChannelRoutes(authed, channelSvc, nil)

	messageSvc := service.NewMessageService(messages, channels, files)
	imhttp.RegisterMessageRoutes(authed, messageSvc, imhttp.MessageRouteOpts{})

	syncSvc := service.NewSyncService(channels, messages)
	imhttp.RegisterSyncRoutes(authed, syncSvc, log)

	friendSvc := service.NewFriendService(friends)
	imhttp.RegisterFriendRoutes(authed, friendSvc, nil)

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
