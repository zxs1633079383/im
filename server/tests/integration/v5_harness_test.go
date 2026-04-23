//go:build integration

// Package integration — V5 harness shared by v5_single_flows_test.go and
// v5_groups_test.go.
//
// V5 exercises end-to-end business processes (OVERALL.md §5.3 / §5.3.1). The
// harness spins up a Postgres testcontainer with every migration applied
// (see containers.StartPostgres), wires every registered route onto one
// *gin.Engine, and returns a v5env value test cases can use to:
//
//   - create & authenticate users (CreateUserAndToken)
//   - create group/DM channels (CreateGroup, CreateOrGetDM)
//   - replay the HTTP API against a real repository stack
//   - observe push/broadcast events via recording fakes (PushRecorder /
//     BroadcastRecorder / ReadSyncRecorder / FriendRecorder / ChannelRecorder)
//
// All assertions use HTTP responses + recorder snapshots; no live WebSocket
// server is spun up. The recorders faithfully mirror the production
// hub-backed pushers' interfaces, so a pushed event in a V5 test is exactly
// what would reach a connected client.
package integration

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gavv/httpexpect/v2"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

// v5env bundles the plumbing every V5 test needs. One is built per test via
// newV5Env; it owns the DB, the Gin engine, recorders, and the HTTP client
// factory.
type v5env struct {
	t          *testing.T
	db         any // *gorm.DB, kept opaque — tests only interact via repos
	users      repo.UserRepo
	channels   repo.ChannelRepo
	messages   repo.MessageRepo
	files      repo.FileRepo
	friends    repo.FriendshipRepo
	governance repo.ChannelGovernanceRepo

	// HTTP + recorders.
	engine      *gin.Engine
	httpExpect  *httpexpect.Expect
	pushes      *PushRecorder
	readSyncs   *ReadSyncRecorder
	broadcasts  *BroadcastRecorder
	friendPush  *FriendRecorder
	channelPush *ChannelRecorder
}

// newV5Env builds a fresh V5 environment. Every test gets its own DB (one
// container per subtest) so they can run -parallel without cross-talk.
func newV5Env(t *testing.T) *v5env {
	t.Helper()

	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	channels := repo.NewChannelRepo(db)
	messages := repo.NewMessageRepo(db, channels)
	files := repo.NewFileRepo(db)
	friends := repo.NewFriendshipRepo(db)
	governance := repo.NewChannelGovernanceRepo(db)

	pushes := &PushRecorder{}
	readSyncs := &ReadSyncRecorder{}
	broadcasts := &BroadcastRecorder{}
	friendPush := &FriendRecorder{}
	channelPush := &ChannelRecorder{}

	gin.SetMode(gin.TestMode)
	r := gin.New()

	// Public auth routes attach directly to the engine.
	imhttp.RegisterAuthRoutes(r,
		service.NewAuthService(users, integrationSecret),
		users, integrationSecret,
	)

	authed := r.Group("/api")
	authed.Use(middleware.JWTGin(integrationSecret))

	imhttp.RegisterProfileRoutes(authed, service.NewProfileService(users))
	imhttp.RegisterChannelRoutes(authed,
		service.NewChannelService(channels, users), channelPush)
	governanceSvc := service.NewChannelGovernanceService(channels, governance, users)
	imhttp.RegisterChannelGovernanceRoutes(authed, governanceSvc, channelPush)
	announcements := repo.NewAnnouncementRepo(db)
	imhttp.RegisterAnnouncementRoutes(authed,
		service.NewAnnouncementService(announcements, channels, governanceSvc),
		broadcasts,
	)
	msgSvc := service.NewMessageService(messages, channels, files)
	urgentRepo := repo.NewUrgentRepo(db)
	imhttp.RegisterUrgentRoutes(authed,
		service.NewUrgentService(urgentRepo, messages, channels, msgSvc, governanceSvc),
		broadcasts,
	)
	imhttp.RegisterMessageRoutes(authed, msgSvc,
		imhttp.MessageRouteOpts{
			Pusher:      pushes,
			ReadSyncer:  readSyncs,
			Broadcaster: broadcasts,
		},
	)
	imhttp.RegisterSyncRoutes(authed,
		service.NewSyncService(channels, messages), nil)
	imhttp.RegisterFriendRoutes(authed,
		service.NewFriendService(friends, users), friendPush)
	// File service needs an upload dir; tests don't exercise upload paths
	// today (see BLOCKER in G8).
	imhttp.RegisterFileRoutes(authed,
		service.NewFileService(files, t.TempDir()), nil)

	return &v5env{
		t:           t,
		db:          db,
		users:       users,
		channels:    channels,
		messages:    messages,
		files:       files,
		friends:     friends,
		governance:  governance,
		engine:      r,
		httpExpect:  testutil.NewExpect(t, r),
		pushes:      pushes,
		readSyncs:   readSyncs,
		broadcasts:  broadcasts,
		friendPush:  friendPush,
		channelPush: channelPush,
	}
}

// CreateUserAndToken seeds a user row and returns its ID + a fresh JWT. The
// password hash is deliberately a non-bcrypt string so login-via-password is
// still a distinct code path (covered by V5.1); tests that only need auth
// should use the returned token directly.
func (e *v5env) CreateUserAndToken(username, email string) (int64, string) {
	e.t.Helper()
	u := &repo.User{
		Username:     username,
		Email:        email,
		PasswordHash: "placeholder-not-a-valid-bcrypt",
		DisplayName:  username,
		Status:       repo.UserStatusActive,
	}
	require.NoError(e.t, e.users.Create(context.Background(), u))
	tok, err := auth.GenerateToken(integrationSecret, u.ID, u.Username)
	require.NoError(e.t, err)
	return u.ID, tok
}

// bearer returns "Bearer <tok>" for use with the httpexpect chain.
func bearer(tok string) string { return "Bearer " + tok }

// waitUntil polls cond at short intervals until it returns true or the
// overall deadline lapses. The message push pipeline fires in a goroutine
// after the POST responds, so tests need this to bridge the async gap
// without resorting to time.Sleep.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond() // one last chance
}

// waitForPushCount blocks until r.Snapshot() reports at least n events or
// timeout lapses. Returns the final snapshot so the caller can assert on it.
func waitForPushCount(t *testing.T, r *PushRecorder, n int, timeout time.Duration) []PushEvent {
	t.Helper()
	waitUntil(t, timeout, func() bool { return len(r.Snapshot()) >= n })
	return r.Snapshot()
}

// waitForBroadcastCount blocks until the broadcaster has recorded at least n
// events or timeout lapses.
func waitForBroadcastCount(t *testing.T, r *BroadcastRecorder, n int, timeout time.Duration) []BroadcastEvent {
	t.Helper()
	waitUntil(t, timeout, func() bool { return len(r.Snapshot()) >= n })
	return r.Snapshot()
}

// CreateGroup makes the caller create a group named `name` with optional
// additional member IDs. Returns the new channel ID.
func (e *v5env) CreateGroup(tok, name string, memberIDs ...int64) int64 {
	e.t.Helper()
	body := map[string]any{"name": name}
	if len(memberIDs) > 0 {
		body["member_ids"] = memberIDs
	}
	chID := int64(e.httpExpect.POST("/api/channels").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(body).
		Expect().Status(201).JSON().Object().
		Value("id").Number().Raw())
	require.NotZero(e.t, chID, "create group returned zero channel id")
	return chID
}

// CreateOrGetDM sends POST /api/channels/dm. Returns the channel ID; 200 and
// 201 are both accepted (legacy create-or-get contract).
func (e *v5env) CreateOrGetDM(tok string, peerID int64) int64 {
	e.t.Helper()
	resp := e.httpExpect.POST("/api/channels/dm").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]int64{"peer_id": peerID}).
		Expect()
	code := resp.Raw().StatusCode
	require.Truef(e.t, code == 200 || code == 201, "dm create: got %d", code)
	return int64(resp.JSON().Object().Value("id").Number().Raw())
}

// SendMessage posts to POST /api/channels/:id/messages. Returns the server
// seq of the new message. clientMsgID should be unique per logical send to
// bypass the idempotency guard.
func (e *v5env) SendMessage(tok string, channelID int64, content, clientMsgID string) int64 {
	e.t.Helper()
	obj := e.httpExpect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{
			"content":       content,
			"client_msg_id": clientMsgID,
		}).
		Expect().Status(201).JSON().Object()
	return int64(obj.Value("seq").Number().Raw())
}

// SendReply posts a message that references root msgID via reply_to.
func (e *v5env) SendReply(tok string, channelID, rootMsgID int64, content, clientMsgID string) int64 {
	e.t.Helper()
	obj := e.httpExpect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{
			"content":       content,
			"client_msg_id": clientMsgID,
			"reply_to":      rootMsgID,
		}).
		Expect().Status(201).JSON().Object()
	return int64(obj.Value("id").Number().Raw())
}

// MustSendAndReturnMsgID sends and returns the server-assigned message ID.
func (e *v5env) MustSendAndReturnMsgID(tok string, channelID int64, content, clientMsgID string) int64 {
	e.t.Helper()
	obj := e.httpExpect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/messages").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]any{
			"content":       content,
			"client_msg_id": clientMsgID,
		}).
		Expect().Status(201).JSON().Object()
	return int64(obj.Value("id").Number().Raw())
}

// MarkRead calls POST /api/channels/:id/read. Returns the stored seq.
func (e *v5env) MarkRead(tok string, channelID int64) int64 {
	e.t.Helper()
	return int64(e.httpExpect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/read").
		WithHeader("Authorization", bearer(tok)).
		Expect().Status(200).JSON().Object().
		Value("seq").Number().Raw())
}

// DeleteMessage sends DELETE /api/messages/:id as the caller.
func (e *v5env) DeleteMessage(tok string, msgID int64) {
	e.t.Helper()
	e.httpExpect.DELETE("/api/messages/"+strconv.FormatInt(msgID, 10)).
		WithHeader("Authorization", bearer(tok)).
		Expect().Status(200)
}

// EditMessage sends PATCH /api/messages/:id with new content.
func (e *v5env) EditMessage(tok string, msgID int64, content string) {
	e.t.Helper()
	e.httpExpect.PATCH("/api/messages/"+strconv.FormatInt(msgID, 10)).
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]string{"content": content}).
		Expect().Status(200)
}

// AddMember calls POST /api/channels/:id/members.
func (e *v5env) AddMember(tok string, channelID, userID int64) {
	e.t.Helper()
	e.httpExpect.POST("/api/channels/" + strconv.FormatInt(channelID, 10) + "/members").
		WithHeader("Authorization", bearer(tok)).
		WithJSON(map[string]int64{"user_id": userID}).
		Expect().Status(201)
}

// RemoveMember calls DELETE /api/channels/:id/members/:user_id.
func (e *v5env) RemoveMember(tok string, channelID, userID int64) {
	e.t.Helper()
	e.httpExpect.DELETE(fmt.Sprintf("/api/channels/%d/members/%d", channelID, userID)).
		WithHeader("Authorization", bearer(tok)).
		Expect().Status(200)
}

// ============================================================================
// Recorders — drop-in fakes for production pushers/broadcasters. Every public
// interface method is implemented with a lock + append pattern, and a matching
// Snapshot() returns an immutable copy for assertions.
// ============================================================================

// PushRecorder captures MessagePusher.PushMessage calls.
type PushRecorder struct {
	mu     sync.Mutex
	events []PushEvent
}

// PushEvent is a single captured PushMessage invocation.
type PushEvent struct {
	UserID int64
	Msg    *repo.Message
}

// PushMessage satisfies imhttp.MessagePusher.
func (r *PushRecorder) PushMessage(userID int64, msg *repo.Message) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, PushEvent{UserID: userID, Msg: msg})
}

// Snapshot returns a copy of every push event so far.
func (r *PushRecorder) Snapshot() []PushEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PushEvent, len(r.events))
	copy(out, r.events)
	return out
}

// Reset clears every recorded event. Useful between phases of a group test.
func (r *PushRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// ReadSyncRecorder captures ReadSyncPusher.PushReadSync calls.
type ReadSyncRecorder struct {
	mu     sync.Mutex
	events []ReadSyncEvent
}

// ReadSyncEvent is a single captured PushReadSync invocation.
type ReadSyncEvent struct {
	UserID    int64
	ChannelID int64
	ReadSeq   int64
}

// PushReadSync satisfies imhttp.ReadSyncPusher.
func (r *ReadSyncRecorder) PushReadSync(userID, channelID, readSeq int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ReadSyncEvent{UserID: userID, ChannelID: channelID, ReadSeq: readSeq})
}

// Snapshot returns a copy of every read-sync event so far.
func (r *ReadSyncRecorder) Snapshot() []ReadSyncEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ReadSyncEvent, len(r.events))
	copy(out, r.events)
	return out
}

// BroadcastRecorder captures MessageEventBroadcaster.BroadcastToMembers calls.
type BroadcastRecorder struct {
	mu     sync.Mutex
	events []BroadcastEvent
}

// BroadcastEvent is a single captured BroadcastToMembers invocation.
type BroadcastEvent struct {
	ChannelID int64
	EventType imhttp.MessageEventType
	Payload   any
}

// BroadcastToMembers satisfies imhttp.MessageEventBroadcaster.
func (r *BroadcastRecorder) BroadcastToMembers(channelID int64, eventType imhttp.MessageEventType, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, BroadcastEvent{
		ChannelID: channelID, EventType: eventType, Payload: payload,
	})
}

// Snapshot returns a copy of every broadcast event so far.
func (r *BroadcastRecorder) Snapshot() []BroadcastEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]BroadcastEvent, len(r.events))
	copy(out, r.events)
	return out
}

// Reset clears every recorded event.
func (r *BroadcastRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = nil
}

// FriendRecorder captures FriendEventPusher.PushFriendEvent calls.
type FriendRecorder struct {
	mu     sync.Mutex
	events []FriendEvent
}

// FriendEvent is a single captured PushFriendEvent invocation.
type FriendEvent struct {
	TargetUserID int64
	EventType    string
	FromUserID   int64
}

// PushFriendEvent satisfies imhttp.FriendEventPusher.
func (r *FriendRecorder) PushFriendEvent(targetUserID int64, eventType string, fromUserID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, FriendEvent{
		TargetUserID: targetUserID, EventType: eventType, FromUserID: fromUserID,
	})
}

// Snapshot returns a copy of every friend event so far.
func (r *FriendRecorder) Snapshot() []FriendEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]FriendEvent, len(r.events))
	copy(out, r.events)
	return out
}

// ChannelRecorder captures ChannelEventPusher.PushChannelEvent calls.
type ChannelRecorder struct {
	mu     sync.Mutex
	events []ChannelEvent
}

// ChannelEvent is a single captured PushChannelEvent invocation.
type ChannelEvent struct {
	TargetUserID int64
	EventType    string
	ChannelID    int64
	Name         string
}

// PushChannelEvent satisfies imhttp.ChannelEventPusher.
func (r *ChannelRecorder) PushChannelEvent(targetUserID int64, eventType string, channelID int64, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ChannelEvent{
		TargetUserID: targetUserID, EventType: eventType, ChannelID: channelID, Name: name,
	})
}

// Snapshot returns a copy of every channel event so far.
func (r *ChannelRecorder) Snapshot() []ChannelEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ChannelEvent, len(r.events))
	copy(out, r.events)
	return out
}
