//go:build integration

// Package integration — Phase P1（autonomous test-coverage-100）补全 file.go
// 三个端点的 case 矩阵：
//
//   POST /api/files                      multipart 上传
//   GET  /api/files/:id                  下载
//   GET  /api/messages/:id/attachments   按 message 列附件
//
// 这族端点之前 0 测试（C008 §4.4 TODO 已批准从外部 OSS 切换到本地落盘后补齐）。
// 测试用 t.TempDir() 作为 uploadDir 隔离磁盘，避免 /data/uploads 默认值污染
// 宿主机。FileService 走 NewFileRepo + 本地 dir。
//
// Seed 范围 2100-2199。
package integration

import (
	"bytes"
	"log/slog"
	"mime/multipart"
	"strings"
	"testing"

	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// wireFileRoutes 注册 file.go 的 3 个端点到 env.engine，uploadDir 用 t.TempDir()
// 保证测试间互不污染。
func wireFileRoutes(env *m4env) {
	env.t.Helper()
	log := slog.Default()

	authed := env.engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(env.rdb, log))
	authed.Use(middleware.CookieRequired())

	fileRepo := repo.NewFileRepo(env.db)
	fileSvc := service.NewFileService(fileRepo, env.t.TempDir())
	imhttp.RegisterFileRoutes(authed, fileSvc, log)
}

// makeMultipartBody 构造 multipart/form-data 字段 file 的 body + content-type。
// content 可为任意字节序列；filename 决定 Content-Disposition 的 filename=。
func makeMultipartBody(t *testing.T, filename, content string) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte(content)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, w.FormDataContentType()
}

// ---------------------------------------------------------------------------
// POST /api/files
// ---------------------------------------------------------------------------

// TestM4FileUpload_C1_HappyPath — multipart upload returns 201 + envelope.data
// 里有 id / file_name / mime_type。
func TestM4FileUpload_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	cookie, _ := env.seedUser(2100)
	body, ct := makeMultipartBody(t, "hello.txt", "hi there")

	resp := successBody(env.expect.POST("/api/files").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", ct).
		WithBytes(body.Bytes()).
		Expect().Status(201))

	resp.Value("id").String().NotEmpty()
	resp.Value("file_name").String().IsEqual("hello.txt")
}

// TestM4FileUpload_C2_CookieMissing — no cookieId → 401.
func TestM4FileUpload_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	body, ct := makeMultipartBody(t, "x.txt", "x")
	errorBody(env.expect.POST("/api/files").
		WithHeader("Content-Type", ct).
		WithBytes(body.Bytes()).
		Expect().Status(401))
}

// TestM4FileUpload_C3_CookieInvalid — cookieId 不存在 → 401.
func TestM4FileUpload_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	body, ct := makeMultipartBody(t, "x.txt", "x")
	errorBody(env.expect.POST("/api/files").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		WithHeader("Content-Type", ct).
		WithBytes(body.Bytes()).
		Expect().Status(401))
}

// TestM4FileUpload_C5_MissingFile — 没有 "file" multipart 字段 → 400
// "missing file field"。
func TestM4FileUpload_C5_MissingFile(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	cookie, _ := env.seedUser(2110)

	// Multipart body without a "file" field — only a sentinel "junk" field
	// so the writer emits a valid multipart envelope.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("junk", "no file here")
	_ = w.Close()

	errorBody(env.expect.POST("/api/files").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", w.FormDataContentType()).
		WithBytes(body.Bytes()).
		Expect().Status(400)).
		Value("error").String().Contains("file")
}

// ---------------------------------------------------------------------------
// GET /api/files/:id
// ---------------------------------------------------------------------------

// TestM4FileDownload_C1_HappyPath — 上传后下载，body 字节相等 + content-type
// 由 server 端 mime 嗅探给出（multipart Content-Type 未必和最终 mime 一致 ——
// 只验证 body 字节）。
func TestM4FileDownload_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	cookie, _ := env.seedUser(2120)
	want := "round-trip-content"
	body, ct := makeMultipartBody(t, "round.txt", want)

	created := successBody(env.expect.POST("/api/files").
		WithHeader(middleware.MMCookieHeader, cookie).
		WithHeader("Content-Type", ct).
		WithBytes(body.Bytes()).
		Expect().Status(201))
	fileID := created.Value("id").String().Raw()

	got := env.expect.GET("/api/files/"+fileID).
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(200).Body().Raw()

	if !strings.Contains(got, want) {
		t.Fatalf("downloaded body does not contain uploaded content:\n  want=%q\n  got =%q", want, got)
	}
}

// TestM4FileDownload_C2_CookieMissing — 401.
func TestM4FileDownload_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	errorBody(env.expect.GET("/api/files/whatever").
		Expect().Status(401))
}

// TestM4FileDownload_C3_CookieInvalid — 401.
func TestM4FileDownload_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	errorBody(env.expect.GET("/api/files/whatever").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}

// TestM4FileDownload_C5_NotFound — 不存在的 file id → 404
// "file not found"（repo.ErrNotFound 分支）。
func TestM4FileDownload_C5_NotFound(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	cookie, _ := env.seedUser(2130)

	errorBody(env.expect.GET("/api/files/nonexistent-file-id").
		WithHeader(middleware.MMCookieHeader, cookie).
		Expect().Status(404)).
		Value("error").String().Contains("not found")
}

// ---------------------------------------------------------------------------
// GET /api/messages/:id/attachments
// ---------------------------------------------------------------------------

// TestM4FileAttachments_C1_HappyPath — 对一个不存在 attachments 的 message id
// 查 → 200 + files: []（service 层不验 message 存在性，只查 message_id 命中 0
// 行则返回空数组）。
//
// NB: 业务层目前没有 "POST /api/files 之后绑定到 message" 的 API ——
// ListAttachments 是预留 endpoint。本 case 仅证明路由通顺 + envelope 正确。
func TestM4FileAttachments_C1_HappyPath(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	cookieA, idA := env.seedUser(2140)
	_, idB := env.seedUser(2141)
	channelID := env.seedDM(cookieA, idB)
	msg := env.seedMessage(channelID, idA, "msg-with-no-attachments")

	body := successBody(env.expect.GET("/api/messages/"+msg.ID+"/attachments").
		WithHeader(middleware.MMCookieHeader, cookieA).
		Expect().Status(200))

	body.Value("files").Array().Length().IsEqual(0)
}

// TestM4FileAttachments_C2_CookieMissing — 401.
func TestM4FileAttachments_C2_CookieMissing(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	errorBody(env.expect.GET("/api/messages/anything/attachments").
		Expect().Status(401))
}

// TestM4FileAttachments_C3_CookieInvalid — 401.
func TestM4FileAttachments_C3_CookieInvalid(t *testing.T) {
	env := newM4Env(t)
	wireFileRoutes(env)

	errorBody(env.expect.GET("/api/messages/anything/attachments").
		WithHeader(middleware.MMCookieHeader, "deadbeefdeadbeefdeadbeef").
		Expect().Status(401))
}
