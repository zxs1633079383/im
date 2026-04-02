# Plan 1: 项目基础 + 数据库 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搭建 Go 后端和 Tauri+Angular 客户端的项目骨架，建立完整的 PG 数据库 schema，配置开发环境，为后续所有功能计划提供地基。

**Architecture:** Go 后端分为三个独立服务（Gateway、MessageService、SyncService），共享 internal 包。客户端使用 Tauri v2 + Angular，本地存储用 SQLite。数据库迁移用 golang-migrate 管理。

**Tech Stack:** Go 1.22+, PostgreSQL 16, Redis 7, Apache Pulsar 3, Tauri v2, Angular 17+, SQLite

---

## 项目目录总览

```
im/
├── server/
│   ├── cmd/
│   │   ├── gateway/main.go          # WebSocket 网关入口
│   │   ├── message/main.go          # 消息处理服务入口
│   │   └── sync/main.go             # 同步服务入口
│   ├── internal/
│   │   ├── config/config.go         # 配置加载
│   │   ├── model/                   # 领域模型
│   │   │   ├── user.go
│   │   │   ├── channel.go
│   │   │   ├── message.go
│   │   │   └── friendship.go
│   │   ├── store/                   # 数据库访问层
│   │   │   ├── pg.go                # PG 连接池
│   │   │   ├── redis.go             # Redis 连接
│   │   │   ├── user.go
│   │   │   ├── channel.go
│   │   │   └── message.go
│   │   └── testutil/                # 测试辅助
│   │       └── db.go
│   ├── migrations/
│   │   ├── 001_init.up.sql
│   │   └── 001_init.down.sql
│   ├── go.mod
│   ├── go.sum
│   └── Makefile
├── client/                          # Tauri + Angular
│   ├── src/                         # Angular 源码
│   │   ├── app/
│   │   │   ├── core/
│   │   │   │   ├── db/
│   │   │   │   │   ├── database.service.ts
│   │   │   │   │   └── schema.ts
│   │   │   │   └── core.module.ts
│   │   │   └── app.component.ts
│   │   └── environments/
│   ├── src-tauri/                   # Tauri Rust 后端
│   │   ├── src/main.rs
│   │   ├── Cargo.toml
│   │   └── tauri.conf.json
│   ├── angular.json
│   ├── package.json
│   └── tsconfig.json
└── docs/
```

---

### Task 1: Go 项目脚手架

**Files:**
- Create: `server/go.mod`
- Create: `server/Makefile`
- Create: `server/cmd/gateway/main.go`
- Create: `server/cmd/message/main.go`
- Create: `server/cmd/sync/main.go`

- [ ] **Step 1: 初始化 Go module**

```bash
cd /Users/mac17/workspace/ai/im
mkdir -p server
cd server
go mod init im-server
```

- [ ] **Step 2: 创建目录结构**

```bash
cd /Users/mac17/workspace/ai/im/server
mkdir -p cmd/gateway cmd/message cmd/sync
mkdir -p internal/config internal/model internal/store internal/testutil
mkdir -p migrations
```

- [ ] **Step 3: 创建三个服务入口文件**

`server/cmd/gateway/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("gateway starting...")
	os.Exit(run())
}

func run() int {
	// TODO: Plan 6 实现
	return 0
}
```

`server/cmd/message/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("message service starting...")
	os.Exit(run())
}

func run() int {
	// TODO: Plan 5 实现
	return 0
}
```

`server/cmd/sync/main.go`:
```go
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("sync service starting...")
	os.Exit(run())
}

func run() int {
	// TODO: Plan 7 实现
	return 0
}
```

- [ ] **Step 4: 创建 Makefile**

`server/Makefile`:
```makefile
.PHONY: build-gateway build-message build-sync migrate-up migrate-down test

build-gateway:
	go build -o bin/gateway ./cmd/gateway

build-message:
	go build -o bin/message ./cmd/message

build-sync:
	go build -o bin/sync ./cmd/sync

build-all: build-gateway build-message build-sync

migrate-up:
	migrate -path migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path migrations -database "$(DATABASE_URL)" down 1

migrate-create:
	migrate create -ext sql -dir migrations -seq $(name)

test:
	go test ./... -v -count=1

test-short:
	go test ./... -v -short -count=1

.DEFAULT_GOAL := build-all
```

- [ ] **Step 5: 验证三个服务可编译**

Run: `cd /Users/mac17/workspace/ai/im/server && go build ./cmd/gateway && go build ./cmd/message && go build ./cmd/sync`
Expected: 无错误输出

- [ ] **Step 6: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/
git commit -m "chore: init Go project scaffolding with three service entries"
```

---

### Task 2: 配置系统

**Files:**
- Create: `server/internal/config/config.go`
- Create: `server/internal/config/config_test.go`
- Create: `server/config.example.yaml`

- [ ] **Step 1: 安装依赖**

```bash
cd /Users/mac17/workspace/ai/im/server
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: 编写配置加载测试**

`server/internal/config/config_test.go`:
```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFromFile(t *testing.T) {
	content := []byte(`
pg:
  dsn: "postgres://user:pass@localhost:5432/im?sslmode=disable"
redis:
  addr: "localhost:6379"
pulsar:
  url: "pulsar://localhost:6650"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.PG.DSN != "postgres://user:pass@localhost:5432/im?sslmode=disable" {
		t.Errorf("PG.DSN = %q, want postgres://...", cfg.PG.DSN)
	}
	if cfg.Redis.Addr != "localhost:6379" {
		t.Errorf("Redis.Addr = %q, want localhost:6379", cfg.Redis.Addr)
	}
	if cfg.Pulsar.URL != "pulsar://localhost:6650" {
		t.Errorf("Pulsar.URL = %q, want pulsar://localhost:6650", cfg.Pulsar.URL)
	}
}

func TestLoadEnvOverride(t *testing.T) {
	content := []byte(`
pg:
  dsn: "postgres://default@localhost/im"
redis:
  addr: "localhost:6379"
pulsar:
  url: "pulsar://localhost:6650"
`)
	tmpFile := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(tmpFile, content, 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("IM_PG_DSN", "postgres://override@db:5432/im")

	cfg, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.PG.DSN != "postgres://override@db:5432/im" {
		t.Errorf("PG.DSN = %q, want override DSN", cfg.PG.DSN)
	}
}
```

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/config/ -v`
Expected: FAIL — `Load` 未定义

- [ ] **Step 4: 实现配置加载**

`server/internal/config/config.go`:
```go
package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	PG     PGConfig     `yaml:"pg"`
	Redis  RedisConfig  `yaml:"redis"`
	Pulsar PulsarConfig `yaml:"pulsar"`
}

type PGConfig struct {
	DSN         string `yaml:"dsn"`
	MaxConns    int    `yaml:"max_conns"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type PulsarConfig struct {
	URL string `yaml:"url"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		PG: PGConfig{MaxConns: 20},
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	applyEnvOverrides(cfg)
	return cfg, nil
}

func applyEnvOverrides(cfg *Config) {
	if v := os.Getenv("IM_PG_DSN"); v != "" {
		cfg.PG.DSN = v
	}
	if v := os.Getenv("IM_REDIS_ADDR"); v != "" {
		cfg.Redis.Addr = v
	}
	if v := os.Getenv("IM_PULSAR_URL"); v != "" {
		cfg.Pulsar.URL = v
	}
}
```

- [ ] **Step 5: 运行测试，确认通过**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 6: 创建示例配置**

`server/config.example.yaml`:
```yaml
pg:
  dsn: "postgres://im:im@localhost:5432/im?sslmode=disable"
  max_conns: 20

redis:
  addr: "localhost:6379"
  password: ""
  db: 0

pulsar:
  url: "pulsar://localhost:6650"
```

- [ ] **Step 7: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/
git commit -m "feat: add config loading with YAML + env override"
```

---

### Task 3: PG 数据库迁移

**Files:**
- Create: `server/migrations/001_init.up.sql`
- Create: `server/migrations/001_init.down.sql`

- [ ] **Step 1: 安装 golang-migrate CLI**

```bash
go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest
```

- [ ] **Step 2: 编写完整的初始化迁移 (up)**

`server/migrations/001_init.up.sql`:
```sql
-- ============================================================
-- 用户
-- ============================================================
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(50)  UNIQUE NOT NULL,
    email         VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    display_name  VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url    TEXT         NOT NULL DEFAULT '',
    status        SMALLINT     NOT NULL DEFAULT 1, -- 1=active, 2=disabled
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- ============================================================
-- 频道（私聊 + 群聊统一模型）
-- ============================================================
CREATE TABLE channels (
    id          BIGSERIAL   PRIMARY KEY,
    type        SMALLINT    NOT NULL, -- 1=DM, 2=GROUP
    name        VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url  TEXT         NOT NULL DEFAULT '',
    seq         BIGINT       NOT NULL DEFAULT 0,
    creator_id  BIGINT       REFERENCES users(id),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_channels_type ON channels(type);

-- ============================================================
-- 频道成员
-- ============================================================
CREATE TABLE channel_members (
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    channel_id      BIGINT      NOT NULL REFERENCES channels(id),
    role            SMALLINT    NOT NULL DEFAULT 1, -- 1=member, 2=admin, 3=owner
    last_read_seq   BIGINT      NOT NULL DEFAULT 0,
    phantom_count   BIGINT      NOT NULL DEFAULT 0,
    phantom_at_read BIGINT      NOT NULL DEFAULT 0,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX idx_channel_members_channel ON channel_members(channel_id);

-- ============================================================
-- 消息
-- ============================================================
CREATE TABLE messages (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL REFERENCES channels(id),
    seq           BIGINT      NOT NULL,
    client_msg_id VARCHAR(36),
    sender_id     BIGINT      NOT NULL REFERENCES users(id),
    msg_type      SMALLINT    NOT NULL DEFAULT 1, -- 1=text, 2=image, 3=file, 4=system
    content       TEXT        NOT NULL DEFAULT '',
    visible_to    BIGINT[],   -- NULL=所有人可见
    reply_to      BIGINT,     -- 回复的消息 ID
    forwarded_from BIGINT,    -- 转发来源消息 ID
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (channel_id, seq),
    UNIQUE (channel_id, client_msg_id)
);

CREATE INDEX idx_messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX idx_messages_sender ON messages(sender_id, created_at);

-- PG 全文搜索（临时方案，后续切 ES）
CREATE INDEX idx_messages_content_search
    ON messages USING gin(to_tsvector('simple', content));

-- ============================================================
-- 好友关系
-- ============================================================
CREATE TABLE friendships (
    id            BIGSERIAL   PRIMARY KEY,
    requester_id  BIGINT      NOT NULL REFERENCES users(id),
    addressee_id  BIGINT      NOT NULL REFERENCES users(id),
    status        SMALLINT    NOT NULL DEFAULT 1, -- 1=pending, 2=accepted, 3=rejected, 4=blocked
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (requester_id, addressee_id)
);

CREATE INDEX idx_friendships_addressee ON friendships(addressee_id, status);

-- ============================================================
-- 文件/附件
-- ============================================================
CREATE TABLE files (
    id             BIGSERIAL    PRIMARY KEY,
    uploader_id    BIGINT       NOT NULL REFERENCES users(id),
    file_name      VARCHAR(255) NOT NULL,
    file_size      BIGINT       NOT NULL,
    mime_type      VARCHAR(100) NOT NULL DEFAULT '',
    storage_path   TEXT         NOT NULL,
    thumbnail_path TEXT         NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE message_attachments (
    message_id BIGINT NOT NULL REFERENCES messages(id),
    file_id    BIGINT NOT NULL REFERENCES files(id),
    PRIMARY KEY (message_id, file_id)
);

-- ============================================================
-- 消息收藏
-- ============================================================
CREATE TABLE message_favorites (
    user_id    BIGINT      NOT NULL REFERENCES users(id),
    message_id BIGINT      NOT NULL REFERENCES messages(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, message_id)
);

-- ============================================================
-- 用户设置
-- ============================================================
CREATE TABLE user_settings (
    user_id              BIGINT      PRIMARY KEY REFERENCES users(id),
    notification_enabled BOOLEAN     NOT NULL DEFAULT TRUE,
    theme                VARCHAR(20) NOT NULL DEFAULT 'light',
    language             VARCHAR(10) NOT NULL DEFAULT 'zh-CN',
    settings_json        JSONB       NOT NULL DEFAULT '{}',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ============================================================
-- updated_at 自动更新触发器
-- ============================================================
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_channels_updated_at
    BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_friendships_updated_at
    BEFORE UPDATE ON friendships FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TRIGGER trg_user_settings_updated_at
    BEFORE UPDATE ON user_settings FOR EACH ROW EXECUTE FUNCTION update_updated_at();
```

- [ ] **Step 3: 编写回滚迁移 (down)**

`server/migrations/001_init.down.sql`:
```sql
DROP TRIGGER IF EXISTS trg_user_settings_updated_at ON user_settings;
DROP TRIGGER IF EXISTS trg_friendships_updated_at ON friendships;
DROP TRIGGER IF EXISTS trg_channels_updated_at ON channels;
DROP TRIGGER IF EXISTS trg_users_updated_at ON users;
DROP FUNCTION IF EXISTS update_updated_at;

DROP TABLE IF EXISTS message_favorites;
DROP TABLE IF EXISTS message_attachments;
DROP TABLE IF EXISTS files;
DROP TABLE IF EXISTS friendships;
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS channel_members;
DROP TABLE IF EXISTS channels;
DROP TABLE IF EXISTS user_settings;
DROP TABLE IF EXISTS users;
```

- [ ] **Step 4: 创建测试数据库并运行迁移**

```bash
createdb im_test 2>/dev/null || true
cd /Users/mac17/workspace/ai/im/server
DATABASE_URL="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make migrate-up
```

Expected: `1/u init (xxxms)`

- [ ] **Step 5: 验证回滚正常**

```bash
cd /Users/mac17/workspace/ai/im/server
DATABASE_URL="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make migrate-down
DATABASE_URL="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make migrate-up
```

Expected: down 成功，再 up 成功，无错误

- [ ] **Step 6: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/migrations/
git commit -m "feat: add initial PG schema migration with all tables"
```

---

### Task 4: 核心领域模型

**Files:**
- Create: `server/internal/model/user.go`
- Create: `server/internal/model/channel.go`
- Create: `server/internal/model/message.go`
- Create: `server/internal/model/friendship.go`

- [ ] **Step 1: 用户模型**

`server/internal/model/user.go`:
```go
package model

import "time"

type UserStatus int16

const (
	UserStatusActive   UserStatus = 1
	UserStatusDisabled UserStatus = 2
)

type User struct {
	ID           int64      `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	PasswordHash string     `json:"-"`
	DisplayName  string     `json:"display_name"`
	AvatarURL    string     `json:"avatar_url"`
	Status       UserStatus `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

type UserSettings struct {
	UserID              int64  `json:"user_id"`
	NotificationEnabled bool   `json:"notification_enabled"`
	Theme               string `json:"theme"`
	Language            string `json:"language"`
	SettingsJSON        []byte `json:"settings_json"`
}
```

- [ ] **Step 2: 频道模型**

`server/internal/model/channel.go`:
```go
package model

import "time"

type ChannelType int16

const (
	ChannelTypeDM    ChannelType = 1
	ChannelTypeGroup ChannelType = 2
)

type MemberRole int16

const (
	MemberRoleMember MemberRole = 1
	MemberRoleAdmin  MemberRole = 2
	MemberRoleOwner  MemberRole = 3
)

type Channel struct {
	ID        int64       `json:"id"`
	Type      ChannelType `json:"type"`
	Name      string      `json:"name"`
	AvatarURL string      `json:"avatar_url"`
	Seq       int64       `json:"seq"`
	CreatorID *int64      `json:"creator_id"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

type ChannelMember struct {
	UserID        int64      `json:"user_id"`
	ChannelID     int64      `json:"channel_id"`
	Role          MemberRole `json:"role"`
	LastReadSeq   int64      `json:"last_read_seq"`
	PhantomCount  int64      `json:"phantom_count"`
	PhantomAtRead int64      `json:"phantom_at_read"`
	JoinedAt      time.Time  `json:"joined_at"`
}

// UnreadCount 计算该成员的未读消息数
func (m *ChannelMember) UnreadCount(channelSeq int64) int64 {
	unread := (channelSeq - m.LastReadSeq) - (m.PhantomCount - m.PhantomAtRead)
	if unread < 0 {
		return 0
	}
	return unread
}
```

- [ ] **Step 3: 消息模型**

`server/internal/model/message.go`:
```go
package model

import "time"

type MsgType int16

const (
	MsgTypeText   MsgType = 1
	MsgTypeImage  MsgType = 2
	MsgTypeFile   MsgType = 3
	MsgTypeSystem MsgType = 4
)

type Message struct {
	ID            int64   `json:"id"`
	ChannelID     int64   `json:"channel_id"`
	Seq           int64   `json:"seq"`
	ClientMsgID   string  `json:"client_msg_id,omitempty"`
	SenderID      int64   `json:"sender_id"`
	MsgType       MsgType `json:"msg_type"`
	Content       string  `json:"content"`
	VisibleTo     []int64 `json:"visible_to,omitempty"` // nil=所有人可见
	ReplyTo       *int64  `json:"reply_to,omitempty"`
	ForwardedFrom *int64  `json:"forwarded_from,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// IsVisibleTo 判断消息对指定用户是否可见
func (m *Message) IsVisibleTo(userID int64) bool {
	if m.VisibleTo == nil {
		return true
	}
	for _, id := range m.VisibleTo {
		if id == userID {
			return true
		}
	}
	return false
}

// Phantom 为不可见用户生成的占位消息
type Phantom struct {
	Seq       int64  `json:"seq"`
	Type      string `json:"type"` // 固定为 "phantom"
	ChannelID int64  `json:"channel_id"`
}

func NewPhantom(channelID, seq int64) Phantom {
	return Phantom{
		Seq:       seq,
		Type:      "phantom",
		ChannelID: channelID,
	}
}

type File struct {
	ID            int64     `json:"id"`
	UploaderID    int64     `json:"uploader_id"`
	FileName      string    `json:"file_name"`
	FileSize      int64     `json:"file_size"`
	MimeType      string    `json:"mime_type"`
	StoragePath   string    `json:"-"`
	ThumbnailPath string    `json:"thumbnail_path,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type MessageFavorite struct {
	UserID    int64     `json:"user_id"`
	MessageID int64     `json:"message_id"`
	CreatedAt time.Time `json:"created_at"`
}
```

- [ ] **Step 4: 好友关系模型**

`server/internal/model/friendship.go`:
```go
package model

import "time"

type FriendshipStatus int16

const (
	FriendshipPending  FriendshipStatus = 1
	FriendshipAccepted FriendshipStatus = 2
	FriendshipRejected FriendshipStatus = 3
	FriendshipBlocked  FriendshipStatus = 4
)

type Friendship struct {
	ID          int64            `json:"id"`
	RequesterID int64            `json:"requester_id"`
	AddresseeID int64            `json:"addressee_id"`
	Status      FriendshipStatus `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}
```

- [ ] **Step 5: 编写模型单元测试**

`server/internal/model/model_test.go`:
```go
package model

import "testing"

func TestChannelMember_UnreadCount(t *testing.T) {
	tests := []struct {
		name       string
		member     ChannelMember
		channelSeq int64
		want       int64
	}{
		{
			name:       "no unread",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 5, PhantomAtRead: 5},
			channelSeq: 100,
			want:       0,
		},
		{
			name:       "3 normal unread",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 5, PhantomAtRead: 5},
			channelSeq: 103,
			want:       3,
		},
		{
			name:       "unread with phantom excluded",
			member:     ChannelMember{LastReadSeq: 100, PhantomCount: 6, PhantomAtRead: 5},
			channelSeq: 104,
			want:       3,
		},
		{
			name:       "after read",
			member:     ChannelMember{LastReadSeq: 106, PhantomCount: 6, PhantomAtRead: 6},
			channelSeq: 106,
			want:       0,
		},
		{
			name:       "visible directed message",
			member:     ChannelMember{LastReadSeq: 106, PhantomCount: 6, PhantomAtRead: 6},
			channelSeq: 107,
			want:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.member.UnreadCount(tt.channelSeq)
			if got != tt.want {
				t.Errorf("UnreadCount(%d) = %d, want %d", tt.channelSeq, got, tt.want)
			}
		})
	}
}

func TestMessage_IsVisibleTo(t *testing.T) {
	tests := []struct {
		name      string
		visibleTo []int64
		userID    int64
		want      bool
	}{
		{"nil means visible to all", nil, 999, true},
		{"in visible list", []int64{1, 2, 3}, 2, true},
		{"not in visible list", []int64{1, 2, 3}, 4, false},
		{"empty list means no one", []int64{}, 1, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Message{VisibleTo: tt.visibleTo}
			if got := m.IsVisibleTo(tt.userID); got != tt.want {
				t.Errorf("IsVisibleTo(%d) = %v, want %v", tt.userID, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 6: 运行测试**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/model/ -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/model/
git commit -m "feat: add core domain models with unread count and visibility logic"
```

---

### Task 5: 数据库连接池

**Files:**
- Create: `server/internal/store/pg.go`
- Create: `server/internal/store/pg_test.go`
- Create: `server/internal/store/redis.go`

- [ ] **Step 1: 安装依赖**

```bash
cd /Users/mac17/workspace/ai/im/server
go get github.com/jackc/pgx/v5/pgxpool
go get github.com/redis/go-redis/v9
```

- [ ] **Step 2: 编写 PG 连接测试**

`server/internal/store/pg_test.go`:
```go
package store

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestNewPGPool(t *testing.T) {
	dsn := os.Getenv("IM_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("IM_TEST_PG_DSN not set, skipping PG integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := NewPGPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("NewPGPool() error: %v", err)
	}
	defer pool.Close()

	var result int
	err = pool.QueryRow(ctx, "SELECT 1").Scan(&result)
	if err != nil {
		t.Fatalf("ping query error: %v", err)
	}
	if result != 1 {
		t.Errorf("SELECT 1 = %d, want 1", result)
	}
}
```

- [ ] **Step 3: 运行测试，确认失败**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/store/ -v -short`
Expected: FAIL — `NewPGPool` 未定义

- [ ] **Step 4: 实现 PG 连接池**

`server/internal/store/pg.go`:
```go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPGPool(ctx context.Context, dsn string, maxConns int) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse pg config: %w", err)
	}

	if maxConns > 0 {
		cfg.MaxConns = int32(maxConns)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pg pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pg: %w", err)
	}

	return pool, nil
}
```

- [ ] **Step 5: 实现 Redis 连接**

`server/internal/store/redis.go`:
```go
package store

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

func NewRedisClient(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	return client, nil
}
```

- [ ] **Step 6: 运行 PG 测试（需要本地 PG）**

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" go test ./internal/store/ -v -run TestNewPGPool
```

Expected: PASS（或 SKIP 如果没设环境变量）

- [ ] **Step 7: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/ server/go.mod server/go.sum
git commit -m "feat: add PG connection pool and Redis client"
```

---

### Task 6: 测试辅助工具

**Files:**
- Create: `server/internal/testutil/db.go`

- [ ] **Step 1: 创建测试数据库辅助**

`server/internal/testutil/db.go`:
```go
package testutil

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/store"
)

// PGPool 返回一个连接到测试数据库的连接池。
// 如果 IM_TEST_PG_DSN 未设置则跳过测试。
// 每次调用会清空所有业务表数据。
func PGPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("IM_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("IM_TEST_PG_DSN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := store.NewPGPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("connect to test PG: %v", err)
	}

	t.Cleanup(func() { pool.Close() })

	cleanTables(t, pool)
	return pool
}

func cleanTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	tables := []string{
		"message_favorites",
		"message_attachments",
		"files",
		"messages",
		"channel_members",
		"channels",
		"friendships",
		"user_settings",
		"users",
	}
	for _, table := range tables {
		_, err := pool.Exec(context.Background(), fmt.Sprintf("TRUNCATE TABLE %s CASCADE", table))
		if err != nil {
			t.Fatalf("truncate %s: %v", table, err)
		}
	}
}
```

- [ ] **Step 2: 验证编译通过**

Run: `cd /Users/mac17/workspace/ai/im/server && go build ./internal/testutil/`
Expected: 无错误

- [ ] **Step 3: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/testutil/
git commit -m "feat: add test utility for PG pool with table cleanup"
```

---

### Task 7: 基础 Store 层（用户 CRUD）

**Files:**
- Create: `server/internal/store/user.go`
- Create: `server/internal/store/user_test.go`

- [ ] **Step 1: 编写用户 Store 测试**

`server/internal/store/user_test.go`:
```go
package store

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/testutil"
)

func TestUserStore_CreateAndGet(t *testing.T) {
	pool := testutil.PGPool(t)
	us := NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{
		Username:     "testuser",
		Email:        "test@example.com",
		PasswordHash: "hashed_pw",
		DisplayName:  "Test User",
	}

	err := us.Create(ctx, user)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("Create() did not set user.ID")
	}

	got, err := us.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID() error: %v", err)
	}
	if got.Username != "testuser" {
		t.Errorf("Username = %q, want testuser", got.Username)
	}
	if got.Email != "test@example.com" {
		t.Errorf("Email = %q, want test@example.com", got.Email)
	}
}

func TestUserStore_GetByUsername(t *testing.T) {
	pool := testutil.PGPool(t)
	us := NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{
		Username:     "findme",
		Email:        "find@example.com",
		PasswordHash: "hashed",
		DisplayName:  "Find Me",
	}
	if err := us.Create(ctx, user); err != nil {
		t.Fatal(err)
	}

	got, err := us.GetByUsername(ctx, "findme")
	if err != nil {
		t.Fatalf("GetByUsername() error: %v", err)
	}
	if got.ID != user.ID {
		t.Errorf("ID = %d, want %d", got.ID, user.ID)
	}
}

func TestUserStore_DuplicateUsername(t *testing.T) {
	pool := testutil.PGPool(t)
	us := NewUserStore(pool)
	ctx := context.Background()

	user1 := &model.User{Username: "dup", Email: "a@example.com", PasswordHash: "h"}
	if err := us.Create(ctx, user1); err != nil {
		t.Fatal(err)
	}

	user2 := &model.User{Username: "dup", Email: "b@example.com", PasswordHash: "h"}
	err := us.Create(ctx, user2)
	if err == nil {
		t.Fatal("expected error for duplicate username, got nil")
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/store/ -v -run TestUserStore -short`
Expected: FAIL — `NewUserStore` 未定义

- [ ] **Step 3: 实现用户 Store**

`server/internal/store/user.go`:
```go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type UserStore struct {
	pool *pgxpool.Pool
}

func NewUserStore(pool *pgxpool.Pool) *UserStore {
	return &UserStore{pool: pool}
}

func (s *UserStore) Create(ctx context.Context, u *model.User) error {
	err := s.pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, display_name, avatar_url)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, status, created_at, updated_at`,
		u.Username, u.Email, u.PasswordHash, u.DisplayName, u.AvatarURL,
	).Scan(&u.ID, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (s *UserStore) GetByID(ctx context.Context, id int64) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

func (s *UserStore) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE username = $1`, username,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by username: %w", err)
	}
	return u, nil
}

func (s *UserStore) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	u := &model.User{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash, display_name, avatar_url, status, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.DisplayName, &u.AvatarURL, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	return u, nil
}
```

- [ ] **Step 4: 运行测试**

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" go test ./internal/store/ -v -run TestUserStore
```

Expected: PASS（或 SKIP）

- [ ] **Step 5: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/user.go server/internal/store/user_test.go
git commit -m "feat: add UserStore with Create, GetByID, GetByUsername"
```

---

### Task 8: 基础 Store 层（频道 + 消息）

**Files:**
- Create: `server/internal/store/channel.go`
- Create: `server/internal/store/channel_test.go`
- Create: `server/internal/store/message.go`
- Create: `server/internal/store/message_test.go`

- [ ] **Step 1: 编写频道 Store 测试**

`server/internal/store/channel_test.go`:
```go
package store

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/testutil"
)

func TestChannelStore_CreateGroupAndAddMember(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := NewChannelStore(pool)
	us := NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "alice", Email: "alice@test.com", PasswordHash: "h", DisplayName: "Alice"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatal(err)
	}

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "test-group", CreatorID: &user.ID}
	err := cs.Create(ctx, ch)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if ch.ID == 0 {
		t.Fatal("channel ID not set")
	}
	if ch.Seq != 0 {
		t.Errorf("initial seq = %d, want 0", ch.Seq)
	}

	err = cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleOwner)
	if err != nil {
		t.Fatalf("AddMember() error: %v", err)
	}

	members, err := cs.ListMembers(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("len(members) = %d, want 1", len(members))
	}
	if members[0].UserID != user.ID {
		t.Errorf("member UserID = %d, want %d", members[0].UserID, user.ID)
	}
}

func TestChannelStore_CreateDM(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := NewChannelStore(pool)
	us := NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "alice2", Email: "a2@test.com", PasswordHash: "h", DisplayName: "Alice"}
	bob := &model.User{Username: "bob2", Email: "b2@test.com", PasswordHash: "h", DisplayName: "Bob"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	ch := &model.Channel{Type: model.ChannelTypeDM}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatal(err)
	}
	cs.AddMember(ctx, ch.ID, alice.ID, model.MemberRoleMember)
	cs.AddMember(ctx, ch.ID, bob.ID, model.MemberRoleMember)

	channels, err := cs.ListByUser(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(channels) != 1 {
		t.Fatalf("ListByUser len = %d, want 1", len(channels))
	}
	if channels[0].Type != model.ChannelTypeDM {
		t.Errorf("type = %d, want DM", channels[0].Type)
	}
}

func TestChannelStore_IncrementSeq(t *testing.T) {
	pool := testutil.PGPool(t)
	cs := NewChannelStore(pool)
	ctx := context.Background()

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "seq-test"}
	cs.Create(ctx, ch)

	seq, err := cs.IncrementSeq(ctx, nil, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Errorf("first seq = %d, want 1", seq)
	}

	seq, err = cs.IncrementSeq(ctx, nil, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 2 {
		t.Errorf("second seq = %d, want 2", seq)
	}
}
```

- [ ] **Step 2: 运行测试，确认失败**

Run: `cd /Users/mac17/workspace/ai/im/server && go test ./internal/store/ -v -run TestChannelStore -short`
Expected: FAIL

- [ ] **Step 3: 实现频道 Store**

`server/internal/store/channel.go`:
```go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type ChannelStore struct {
	pool *pgxpool.Pool
}

func NewChannelStore(pool *pgxpool.Pool) *ChannelStore {
	return &ChannelStore{pool: pool}
}

func (s *ChannelStore) Create(ctx context.Context, ch *model.Channel) error {
	return s.pool.QueryRow(ctx,
		`INSERT INTO channels (type, name, avatar_url, creator_id)
		 VALUES ($1, $2, $3, $4)
		 RETURNING id, seq, created_at, updated_at`,
		ch.Type, ch.Name, ch.AvatarURL, ch.CreatorID,
	).Scan(&ch.ID, &ch.Seq, &ch.CreatedAt, &ch.UpdatedAt)
}

func (s *ChannelStore) GetByID(ctx context.Context, id int64) (*model.Channel, error) {
	ch := &model.Channel{}
	err := s.pool.QueryRow(ctx,
		`SELECT id, type, name, avatar_url, seq, creator_id, created_at, updated_at
		 FROM channels WHERE id = $1`, id,
	).Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get channel: %w", err)
	}
	return ch, nil
}

// IncrementSeq 原子递增 channel seq 并返回新值。
// 如果 tx 不为 nil，则在该事务中执行。
func (s *ChannelStore) IncrementSeq(ctx context.Context, tx pgx.Tx, channelID int64) (int64, error) {
	var seq int64
	q := `UPDATE channels SET seq = seq + 1 WHERE id = $1 RETURNING seq`
	var err error
	if tx != nil {
		err = tx.QueryRow(ctx, q, channelID).Scan(&seq)
	} else {
		err = s.pool.QueryRow(ctx, q, channelID).Scan(&seq)
	}
	if err != nil {
		return 0, fmt.Errorf("increment seq: %w", err)
	}
	return seq, nil
}

func (s *ChannelStore) AddMember(ctx context.Context, channelID, userID int64, role model.MemberRole) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO channel_members (user_id, channel_id, role)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (user_id, channel_id) DO NOTHING`,
		userID, channelID, role,
	)
	return err
}

func (s *ChannelStore) RemoveMember(ctx context.Context, channelID, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM channel_members WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID,
	)
	return err
}

func (s *ChannelStore) GetMember(ctx context.Context, channelID, userID int64) (*model.ChannelMember, error) {
	m := &model.ChannelMember{}
	err := s.pool.QueryRow(ctx,
		`SELECT user_id, channel_id, role, last_read_seq, phantom_count, phantom_at_read, joined_at
		 FROM channel_members WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID,
	).Scan(&m.UserID, &m.ChannelID, &m.Role, &m.LastReadSeq, &m.PhantomCount, &m.PhantomAtRead, &m.JoinedAt)
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return m, nil
}

func (s *ChannelStore) ListMembers(ctx context.Context, channelID int64) ([]model.ChannelMember, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id, channel_id, role, last_read_seq, phantom_count, phantom_at_read, joined_at
		 FROM channel_members WHERE channel_id = $1`, channelID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var members []model.ChannelMember
	for rows.Next() {
		var m model.ChannelMember
		if err := rows.Scan(&m.UserID, &m.ChannelID, &m.Role, &m.LastReadSeq, &m.PhantomCount, &m.PhantomAtRead, &m.JoinedAt); err != nil {
			return nil, err
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

func (s *ChannelStore) ListByUser(ctx context.Context, userID int64) ([]model.Channel, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT c.id, c.type, c.name, c.avatar_url, c.seq, c.creator_id, c.created_at, c.updated_at
		 FROM channels c
		 JOIN channel_members cm ON cm.channel_id = c.id
		 WHERE cm.user_id = $1
		 ORDER BY c.updated_at DESC`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var channels []model.Channel
	for rows.Next() {
		var ch model.Channel
		if err := rows.Scan(&ch.ID, &ch.Type, &ch.Name, &ch.AvatarURL, &ch.Seq, &ch.CreatorID, &ch.CreatedAt, &ch.UpdatedAt); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}

func (s *ChannelStore) MarkRead(ctx context.Context, channelID, userID, seq int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE channel_members
		 SET last_read_seq = $3, phantom_at_read = phantom_count
		 WHERE user_id = $1 AND channel_id = $2`,
		userID, channelID, seq,
	)
	return err
}

func (s *ChannelStore) IncrementPhantomCount(ctx context.Context, tx pgx.Tx, channelID int64, excludeUserIDs []int64) error {
	q := `UPDATE channel_members SET phantom_count = phantom_count + 1
	      WHERE channel_id = $1 AND user_id != ALL($2)`
	var err error
	if tx != nil {
		_, err = tx.Exec(ctx, q, channelID, excludeUserIDs)
	} else {
		_, err = s.pool.Exec(ctx, q, channelID, excludeUserIDs)
	}
	return err
}
```

- [ ] **Step 4: 运行频道 Store 测试**

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" go test ./internal/store/ -v -run TestChannelStore
```

Expected: PASS

- [ ] **Step 5: 编写消息 Store 测试**

`server/internal/store/message_test.go`:
```go
package store

import (
	"context"
	"testing"

	"im-server/internal/model"
	"im-server/internal/testutil"
)

func TestMessageStore_SendAndFetch(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := NewMessageStore(pool)
	cs := NewChannelStore(pool)
	us := NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "sender", Email: "s@test.com", PasswordHash: "h", DisplayName: "Sender"}
	us.Create(ctx, user)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "msg-test"}
	cs.Create(ctx, ch)
	cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleMember)

	msg := &model.Message{
		ChannelID:   ch.ID,
		SenderID:    user.ID,
		ClientMsgID: "uuid-001",
		MsgType:     model.MsgTypeText,
		Content:     "hello world",
	}
	err := ms.Send(ctx, msg)
	if err != nil {
		t.Fatalf("Send() error: %v", err)
	}
	if msg.Seq != 1 {
		t.Errorf("seq = %d, want 1", msg.Seq)
	}

	messages, err := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("len = %d, want 1", len(messages))
	}
	if messages[0].Content != "hello world" {
		t.Errorf("content = %q", messages[0].Content)
	}
}

func TestMessageStore_Idempotent(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := NewMessageStore(pool)
	cs := NewChannelStore(pool)
	us := NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "idem", Email: "idem@test.com", PasswordHash: "h", DisplayName: "Idem"}
	us.Create(ctx, user)
	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "idem-test"}
	cs.Create(ctx, ch)

	msg1 := &model.Message{ChannelID: ch.ID, SenderID: user.ID, ClientMsgID: "same-uuid", Content: "first"}
	if err := ms.Send(ctx, msg1); err != nil {
		t.Fatal(err)
	}

	msg2 := &model.Message{ChannelID: ch.ID, SenderID: user.ID, ClientMsgID: "same-uuid", Content: "duplicate"}
	if err := ms.Send(ctx, msg2); err != nil {
		t.Fatal(err)
	}

	// 幂等：第二次发送应该返回第一条的 seq
	if msg2.Seq != msg1.Seq {
		t.Errorf("duplicate seq = %d, want %d", msg2.Seq, msg1.Seq)
	}

	messages, _ := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if len(messages) != 1 {
		t.Errorf("message count = %d after duplicate send, want 1", len(messages))
	}
}

func TestMessageStore_FetchForUser_Phantom(t *testing.T) {
	pool := testutil.PGPool(t)
	ms := NewMessageStore(pool)
	cs := NewChannelStore(pool)
	us := NewUserStore(pool)
	ctx := context.Background()

	alice := &model.User{Username: "alice3", Email: "a3@test.com", PasswordHash: "h", DisplayName: "Alice"}
	bob := &model.User{Username: "bob3", Email: "b3@test.com", PasswordHash: "h", DisplayName: "Bob"}
	us.Create(ctx, alice)
	us.Create(ctx, bob)

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "phantom-test"}
	cs.Create(ctx, ch)

	// 普通消息
	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m1", Content: "public"})
	// 定向消息：只有 alice 可见
	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m2", Content: "secret", VisibleTo: []int64{alice.ID}})
	// 另一条普通消息
	ms.Send(ctx, &model.Message{ChannelID: ch.ID, SenderID: alice.ID, ClientMsgID: "m3", Content: "public2"})

	// Alice 应该看到全部3条
	aliceMsgs, _ := ms.FetchAfter(ctx, ch.ID, 0, 50)
	if len(aliceMsgs) != 3 {
		t.Errorf("alice sees %d messages, want 3", len(aliceMsgs))
	}

	// Bob 应该看到 seq 1(public) + seq 2(phantom) + seq 3(public2)
	bobView, _ := ms.FetchForUser(ctx, ch.ID, bob.ID, 0, 50)
	if len(bobView) != 3 {
		t.Fatalf("bob sees %d items, want 3", len(bobView))
	}
	// seq 2 应该是 phantom（content 为空）
	if bobView[1].Content != "" {
		t.Errorf("bob sees content %q for phantom, want empty", bobView[1].Content)
	}
	if bobView[1].MsgType != model.MsgTypePhantom {
		t.Errorf("bob msg_type = %d, want phantom(%d)", bobView[1].MsgType, model.MsgTypePhantom)
	}
}
```

- [ ] **Step 6: 更新 model 增加 MsgTypePhantom 常量**

在 `server/internal/model/message.go` 中 MsgType 常量块里添加：

```go
const (
	MsgTypeText    MsgType = 1
	MsgTypeImage   MsgType = 2
	MsgTypeFile    MsgType = 3
	MsgTypeSystem  MsgType = 4
	MsgTypePhantom MsgType = 99
)
```

- [ ] **Step 7: 实现消息 Store**

`server/internal/store/message.go`:
```go
package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"im-server/internal/model"
)

type MessageStore struct {
	pool    *pgxpool.Pool
	channel *ChannelStore
}

func NewMessageStore(pool *pgxpool.Pool) *MessageStore {
	return &MessageStore{pool: pool, channel: NewChannelStore(pool)}
}

// Send 在事务中分配 seq 并插入消息。支持 client_msg_id 幂等。
func (s *MessageStore) Send(ctx context.Context, msg *model.Message) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	// 检查幂等：如果 client_msg_id 已存在，直接返回已有消息的信息
	if msg.ClientMsgID != "" {
		var existingSeq int64
		var existingID int64
		err := tx.QueryRow(ctx,
			`SELECT id, seq FROM messages WHERE channel_id = $1 AND client_msg_id = $2`,
			msg.ChannelID, msg.ClientMsgID,
		).Scan(&existingID, &existingSeq)
		if err == nil {
			msg.ID = existingID
			msg.Seq = existingSeq
			return nil // 幂等：消息已存在
		}
	}

	// 分配 seq
	seq, err := s.channel.IncrementSeq(ctx, tx, msg.ChannelID)
	if err != nil {
		return err
	}
	msg.Seq = seq

	// 插入消息
	err = tx.QueryRow(ctx,
		`INSERT INTO messages (channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		 RETURNING id, created_at`,
		msg.ChannelID, msg.Seq, nilIfEmpty(msg.ClientMsgID), msg.SenderID, msg.MsgType,
		msg.Content, msg.VisibleTo, msg.ReplyTo, msg.ForwardedFrom,
	).Scan(&msg.ID, &msg.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}

	// 定向消息：更新不可见用户的 phantom_count
	if msg.VisibleTo != nil {
		visibleWithSender := append(msg.VisibleTo, msg.SenderID)
		if err := s.channel.IncrementPhantomCount(ctx, tx, msg.ChannelID, visibleWithSender); err != nil {
			return fmt.Errorf("update phantom count: %w", err)
		}
	}

	return tx.Commit(ctx)
}

// FetchAfter 获取 channel 中 seq > afterSeq 的消息（不做可见性过滤）。
func (s *MessageStore) FetchAfter(ctx context.Context, channelID int64, afterSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`,
		channelID, afterSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMessages(rows)
}

// FetchForUser 获取消息并对不可见的定向消息返回 phantom。
func (s *MessageStore) FetchForUser(ctx context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq > $2
		 ORDER BY seq
		 LIMIT $3`,
		channelID, afterSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

// FetchBefore 获取 seq < beforeSeq 的消息（用于向上翻页）。
func (s *MessageStore) FetchBefore(ctx context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		 FROM messages
		 WHERE channel_id = $1 AND seq < $2
		 ORDER BY seq DESC
		 LIMIT $3`,
		channelID, beforeSeq, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

// FetchAround 获取 seq 附近的消息（用于跳转）。
func (s *MessageStore) FetchAround(ctx context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error) {
	half := limit / 2
	rows, err := s.pool.Query(ctx,
		`(SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		  FROM messages WHERE channel_id = $1 AND seq <= $2 ORDER BY seq DESC LIMIT $3)
		 UNION ALL
		 (SELECT id, channel_id, seq, client_msg_id, sender_id, msg_type, content, visible_to, reply_to, forwarded_from, created_at
		  FROM messages WHERE channel_id = $1 AND seq > $2 ORDER BY seq LIMIT $3)
		 ORDER BY seq`,
		channelID, aroundSeq, half,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	all, err := scanMessages(rows)
	if err != nil {
		return nil, err
	}

	result := make([]model.Message, 0, len(all))
	for _, m := range all {
		if m.IsVisibleTo(userID) {
			result = append(result, m)
		} else {
			result = append(result, model.Message{
				ChannelID: m.ChannelID,
				Seq:       m.Seq,
				MsgType:   model.MsgTypePhantom,
				CreatedAt: m.CreatedAt,
			})
		}
	}
	return result, nil
}

func scanMessages(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]model.Message, error) {
	var messages []model.Message
	for rows.Next() {
		var m model.Message
		var clientMsgID *string
		if err := rows.Scan(&m.ID, &m.ChannelID, &m.Seq, &clientMsgID, &m.SenderID, &m.MsgType,
			&m.Content, &m.VisibleTo, &m.ReplyTo, &m.ForwardedFrom, &m.CreatedAt); err != nil {
			return nil, err
		}
		if clientMsgID != nil {
			m.ClientMsgID = *clientMsgID
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
```

- [ ] **Step 8: 运行所有 Store 测试**

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" go test ./internal/store/ -v
```

Expected: ALL PASS

- [ ] **Step 9: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add server/internal/store/ server/internal/model/
git commit -m "feat: add Channel and Message store with phantom support and idempotent send"
```

---

### Task 9: Tauri + Angular 客户端初始化

**Files:**
- Create: `client/` (Angular + Tauri 项目)

- [ ] **Step 1: 创建 Angular 项目**

```bash
cd /Users/mac17/workspace/ai/im
npx @angular/cli@latest new client --style=scss --routing --ssr=false --skip-git
```

- [ ] **Step 2: 初始化 Tauri**

```bash
cd /Users/mac17/workspace/ai/im/client
npm install @tauri-apps/cli@latest @tauri-apps/api@latest
npx tauri init
```

交互式配置：
- App name: `IM`
- Window title: `IM`
- Web assets relative path: `../dist/client/browser`
- URL of dev server: `http://localhost:4200`
- Frontend dev command: `npm run start`
- Frontend build command: `npm run build`

- [ ] **Step 3: 安装 Tauri SQLite 插件**

```bash
cd /Users/mac17/workspace/ai/im/client
npm install @tauri-apps/plugin-sql
```

在 `src-tauri/Cargo.toml` 的 `[dependencies]` 中添加：

```toml
tauri-plugin-sql = { version = "2", features = ["sqlite"] }
```

在 `src-tauri/src/main.rs` 中注册插件：

```rust
fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_sql::Builder::new().build())
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
```

- [ ] **Step 4: 验证 Angular dev server 启动**

```bash
cd /Users/mac17/workspace/ai/im/client
npm run start
```

Expected: Angular dev server 在 `http://localhost:4200` 启动成功

- [ ] **Step 5: 验证 Tauri 编译**

```bash
cd /Users/mac17/workspace/ai/im/client
npx tauri build --debug
```

Expected: 编译成功（首次可能需要下载 Rust 依赖）

- [ ] **Step 6: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add client/
git commit -m "chore: init Tauri + Angular client project with SQLite plugin"
```

---

### Task 10: 客户端 SQLite Schema + Database Service

**Files:**
- Create: `client/src/app/core/db/schema.ts`
- Create: `client/src/app/core/db/database.service.ts`
- Create: `client/src/app/core/db/database.service.spec.ts`

- [ ] **Step 1: 定义 SQLite Schema**

`client/src/app/core/db/schema.ts`:
```typescript
export const SCHEMA_VERSION = 1;

export const CREATE_TABLES_SQL = `
CREATE TABLE IF NOT EXISTS local_channels (
    id              TEXT PRIMARY KEY,
    type            INTEGER NOT NULL,           -- 1=DM, 2=GROUP
    name            TEXT NOT NULL DEFAULT '',
    avatar_url      TEXT NOT NULL DEFAULT '',
    server_seq      INTEGER NOT NULL DEFAULT 0,
    unread_count    INTEGER NOT NULL DEFAULT 0,
    last_msg_preview TEXT NOT NULL DEFAULT '',
    last_msg_time   INTEGER NOT NULL DEFAULT 0,
    updated_at      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS local_messages (
    channel_id  TEXT NOT NULL,
    seq         INTEGER NOT NULL,
    server_id   TEXT NOT NULL DEFAULT '',
    client_id   TEXT NOT NULL DEFAULT '',
    sender_id   TEXT NOT NULL DEFAULT '',
    msg_type    INTEGER NOT NULL DEFAULT 1,     -- 1=text, 2=image, 3=file, 4=system, 99=phantom
    content     TEXT NOT NULL DEFAULT '',
    visible     INTEGER NOT NULL DEFAULT 1,     -- phantom=0
    reply_to    TEXT,
    created_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (channel_id, seq)
);

CREATE INDEX IF NOT EXISTS idx_msg_visible
    ON local_messages(channel_id, seq) WHERE visible = 1;

CREATE TABLE IF NOT EXISTS local_outbox (
    client_id   TEXT PRIMARY KEY,
    channel_id  TEXT NOT NULL,
    msg_type    INTEGER NOT NULL DEFAULT 1,
    content     TEXT NOT NULL DEFAULT '',
    reply_to    TEXT,
    retry_count INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS local_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);
`;
```

- [ ] **Step 2: 实现 Database Service**

`client/src/app/core/db/database.service.ts`:
```typescript
import { Injectable } from '@angular/core';
import Database from '@tauri-apps/plugin-sql';
import { CREATE_TABLES_SQL, SCHEMA_VERSION } from './schema';

@Injectable({ providedIn: 'root' })
export class DatabaseService {
  private db: Database | null = null;

  async initialize(): Promise<void> {
    this.db = await Database.load('sqlite:im.db');
    await this.db.execute(CREATE_TABLES_SQL);
    await this.db.execute(
      `INSERT OR IGNORE INTO local_meta (key, value) VALUES ('schema_version', $1)`,
      [String(SCHEMA_VERSION)]
    );
  }

  async execute(sql: string, params: unknown[] = []): Promise<void> {
    this.ensureInitialized();
    await this.db!.execute(sql, params);
  }

  async query<T>(sql: string, params: unknown[] = []): Promise<T[]> {
    this.ensureInitialized();
    return await this.db!.select<T[]>(sql, params);
  }

  async getOne<T>(sql: string, params: unknown[] = []): Promise<T | null> {
    const rows = await this.query<T>(sql, params);
    return rows.length > 0 ? rows[0] : null;
  }

  private ensureInitialized(): void {
    if (!this.db) {
      throw new Error('Database not initialized. Call initialize() first.');
    }
  }
}
```

- [ ] **Step 3: 编写测试**

`client/src/app/core/db/database.service.spec.ts`:
```typescript
import { TestBed } from '@angular/core/testing';
import { DatabaseService } from './database.service';

describe('DatabaseService', () => {
  let service: DatabaseService;

  beforeEach(() => {
    TestBed.configureTestingModule({});
    service = TestBed.inject(DatabaseService);
  });

  it('should be created', () => {
    expect(service).toBeTruthy();
  });

  // 注意：完整的 SQLite 集成测试需要在 Tauri 环境中运行
  // 这里只验证 service 实例化
});
```

- [ ] **Step 4: 运行 Angular 测试**

```bash
cd /Users/mac17/workspace/ai/im/client
npx ng test --watch=false --browsers=ChromeHeadless
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add client/src/app/core/
git commit -m "feat: add client SQLite schema and DatabaseService"
```

---

### Task 11: 全量集成验证

- [ ] **Step 1: 运行所有 Go 测试**

```bash
cd /Users/mac17/workspace/ai/im/server
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make test
```

Expected: ALL PASS

- [ ] **Step 2: 运行所有 Angular 测试**

```bash
cd /Users/mac17/workspace/ai/im/client
npx ng test --watch=false --browsers=ChromeHeadless
```

Expected: ALL PASS

- [ ] **Step 3: 验证 PG 迁移 up/down 幂等**

```bash
cd /Users/mac17/workspace/ai/im/server
DATABASE_URL="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make migrate-down
DATABASE_URL="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make migrate-up
IM_TEST_PG_DSN="postgres://$(whoami)@localhost:5432/im_test?sslmode=disable" make test
```

Expected: 迁移 up/down 无错误，测试全部通过

- [ ] **Step 4: 最终 Commit**

```bash
cd /Users/mac17/workspace/ai/im
git add -A
git commit -m "chore: Plan 1 complete - project foundation with PG schema, Go stores, Tauri+Angular client"
```
