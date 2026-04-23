//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"

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

func TestFile_FullFlow(t *testing.T) {
	dsn := containers.StartPostgres(t)
	db, err := repo.Open(repo.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, e := db.DB(); e == nil {
			_ = sqlDB.Close()
		}
	})

	users := repo.NewUserRepo(db)
	files := repo.NewFileRepo(db)

	// Seed a user directly via the repo so we have a known ID + token.
	u := &repo.User{
		Username:     "dave",
		Email:        "d@x.com",
		PasswordHash: "x",
		DisplayName:  "Dave",
		Status:       repo.UserStatusActive,
	}
	require.NoError(t, users.Create(context.Background(), u))
	require.NotZero(t, u.ID)

	tok, err := auth.GenerateToken(integrationSecret, u.ID, u.Username)
	require.NoError(t, err)

	// t.TempDir() avoids leaking files into /data/uploads on dev machines.
	uploadDir := t.TempDir()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	authedAPI := r.Group("/api")
	authedAPI.Use(middleware.JWTGin(integrationSecret))
	imhttp.RegisterFileRoutes(authedAPI, service.NewFileService(files, uploadDir), nil)

	e := testutil.NewExpect(t, r)

	// Upload a small file.
	body := "the quick brown fox jumps over the lazy dog"
	want := sha256.Sum256([]byte(body))
	wantHex := hex.EncodeToString(want[:])

	resp := e.POST("/api/files").
		WithHeader("Authorization", "Bearer "+tok).
		WithMultipart().
		WithFileBytes("file", "fox.txt", []byte(body)).
		Expect().Status(201).JSON().Object()

	resp.Value("file_name").IsEqual("fox.txt")
	resp.Value("file_size").IsEqual(len(body))
	resp.Value("uploader_id").IsEqual(u.ID)
	resp.NotContainsKey("storage_path") // never leak the on-disk path
	fileID := int64(resp.Value("id").Number().Raw())
	require.NotZero(t, fileID)

	// Download — content must round-trip byte-for-byte (sha256 match).
	idStr := strconv.FormatInt(fileID, 10)
	got := e.GET("/api/files/" + idStr).
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).Body().Raw()

	gotSum := sha256.Sum256([]byte(got))
	require.Equal(t, wantHex, hex.EncodeToString(gotSum[:]),
		"downloaded content must sha256-match the uploaded body")

	// Missing file id → 404.
	e.GET("/api/files/9999999").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(404)

	// Unauthed → 401.
	e.GET("/api/files/" + idStr).Expect().Status(401)

	// ListAttachments on a message with no attachments → empty array.
	e.GET("/api/messages/123456/attachments").
		WithHeader("Authorization", "Bearer "+tok).
		Expect().Status(200).JSON().Object().
		Value("files").Array().IsEmpty()
}
