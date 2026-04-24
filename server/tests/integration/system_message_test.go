//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"im-server/internal/auth"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
	"im-server/internal/testutil"
	"im-server/internal/testutil/containers"
)

// TestSystemMessage_EndToEnd verifies that channel mutations land as
// msg_type=System rows with a JSON props payload, and that /api/sync returns
// them so offline clients can catch up.
//
// Scenario (Alice creates + mutates; Bob joins later and syncs):
//  1. Alice creates a group with Bob pre-seeded          → expect channel_created + member_joined(bob)
//  2. Alice renames the channel                          → expect channel_updated
//  3. Alice adds Carol                                   → expect member_joined(carol)
//  4. Alice removes Carol                                → expect member_removed(carol) BEFORE the DELETE
//  5. Bob calls /api/sync with cursor=0 on the channel   → server returns the system messages with props
func TestSystemMessage_EndToEnd(t *testing.T) {
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

	alice := &repo.User{Username: "alice", Email: "a@x.com", PasswordHash: "x", DisplayName: "A", Status: repo.UserStatusActive}
	bob := &repo.User{Username: "bob", Email: "b@x.com", PasswordHash: "x", DisplayName: "B", Status: repo.UserStatusActive}
	carol := &repo.User{Username: "carol", Email: "c@x.com", PasswordHash: "x", DisplayName: "C", Status: repo.UserStatusActive}
	ctx := context.Background()
	require.NoError(t, users.Create(ctx, alice))
	require.NoError(t, users.Create(ctx, bob))
	require.NoError(t, users.Create(ctx, carol))

	aliceTok, err := auth.GenerateToken(integrationSecret, alice.ID, alice.Username)
	require.NoError(t, err)
	bobTok, err := auth.GenerateToken(integrationSecret, bob.ID, bob.Username)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	channelSvc := service.NewChannelService(channels, users, messages)
	imhttp.RegisterChannelRoutes(authedAPI, channelSvc, nil)
	imhttp.RegisterSyncRoutes(authedAPI, service.NewSyncService(channels, messages), nil)
	e := testutil.NewExpect(t, r)

	// --- 1. CreateGroup ---
	chID := int64(e.POST("/api/channels").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]any{"name": "sys-test", "member_ids": []int64{bob.ID}}).
		Expect().Status(201).JSON().Object().Value("id").Number().Raw())

	// --- 2. Update ---
	e.PUT("/api/channels/"+strconv.FormatInt(chID, 10)).
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]string{"name": "renamed", "avatar_url": "a.png"}).
		Expect().Status(200)

	// --- 3. Add Carol ---
	e.POST("/api/channels/"+strconv.FormatInt(chID, 10)+"/members").
		WithHeader("Authorization", "Bearer "+aliceTok).
		WithJSON(map[string]int64{"user_id": carol.ID}).
		Expect().Status(201)

	// --- 4. Remove Carol ---
	e.DELETE("/api/channels/"+strconv.FormatInt(chID, 10)+"/members/"+strconv.FormatInt(carol.ID, 10)).
		WithHeader("Authorization", "Bearer "+aliceTok).
		Expect().Status(200)

	// --- assert rows directly ---
	kinds := collectSysTypes(t, db, chID)
	// channel_created + member_joined(bob) during CreateGroup,
	// channel_updated on rename, member_joined(carol) on add,
	// member_removed(carol) on remove = 5 entries in order.
	require.Equal(t, []string{
		repo.SysTypeChannelCreated,
		repo.SysTypeMemberJoined,
		repo.SysTypeChannelUpdated,
		repo.SysTypeMemberJoined,
		repo.SysTypeMemberRemoved,
	}, kinds, "system message emission order must match the mutation order")

	// --- 5. Bob /api/sync cursor=0: returns all 5 system messages ---
	res := e.POST("/api/sync").
		WithHeader("Authorization", "Bearer "+bobTok).
		WithJSON(map[string]any{"channels": []map[string]any{{"id": chID, "seq": 0}}}).
		Expect().Status(200).JSON().Object()
	chArr := res.Value("channels").Array()
	require.Equal(t, 1, int(chArr.Length().Raw()))
	msgs := chArr.Value(0).Object().Value("messages").Array()
	// all returned messages must be MsgTypeSystem with non-nil props
	for i := 0; i < int(msgs.Length().Raw()); i++ {
		m := msgs.Value(i).Object()
		m.Value("msg_type").IsEqual(float64(repo.MsgTypeSystem))
		m.Value("props").NotNull()
	}
}

// collectSysTypes reads all system messages (msg_type=MsgTypeSystem) for
// channelID ordered by seq and returns the sys_type discriminator from each
// row's props JSON. Decouples assertions from internal seq numbering so
// future refactors that shift seq don't break the test.
func collectSysTypes(t *testing.T, db *gorm.DB, channelID int64) []string {
	t.Helper()
	type row struct {
		Props string `gorm:"column:props"`
	}
	var rows []row
	err := db.Raw(
		`SELECT props FROM messages
		 WHERE channel_id = ? AND msg_type = ? AND props IS NOT NULL
		 ORDER BY seq`,
		channelID, repo.MsgTypeSystem,
	).Scan(&rows).Error
	require.NoError(t, err)

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(r.Props), &parsed))
		sysType, _ := parsed[repo.SysTypeKey].(string)
		out = append(out, sysType)
	}
	return out
}
