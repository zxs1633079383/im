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
    status        SMALLINT     NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE channels (
    id          BIGSERIAL   PRIMARY KEY,
    type        SMALLINT    NOT NULL,
    name        VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url  TEXT         NOT NULL DEFAULT '',
    seq         BIGINT       NOT NULL DEFAULT 0,
    creator_id  BIGINT       REFERENCES users(id),
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_channels_type ON channels(type);

CREATE TABLE channel_members (
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    channel_id      BIGINT      NOT NULL REFERENCES channels(id),
    role            SMALLINT    NOT NULL DEFAULT 1,
    last_read_seq   BIGINT      NOT NULL DEFAULT 0,
    phantom_count   BIGINT      NOT NULL DEFAULT 0,
    phantom_at_read BIGINT      NOT NULL DEFAULT 0,
    joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX idx_channel_members_channel ON channel_members(channel_id);

CREATE TABLE messages (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL REFERENCES channels(id),
    seq           BIGINT      NOT NULL,
    client_msg_id VARCHAR(36),
    sender_id     BIGINT      NOT NULL REFERENCES users(id),
    msg_type      SMALLINT    NOT NULL DEFAULT 1,
    content       TEXT        NOT NULL DEFAULT '',
    visible_to    BIGINT[],
    reply_to      BIGINT,
    forwarded_from BIGINT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (channel_id, seq),
    UNIQUE (channel_id, client_msg_id)
);

CREATE INDEX idx_messages_channel_seq ON messages(channel_id, seq);
CREATE INDEX idx_messages_sender ON messages(sender_id, created_at);
CREATE INDEX idx_messages_content_search ON messages USING gin(to_tsvector('simple', content));

CREATE TABLE friendships (
    id            BIGSERIAL   PRIMARY KEY,
    requester_id  BIGINT      NOT NULL REFERENCES users(id),
    addressee_id  BIGINT      NOT NULL REFERENCES users(id),
    status        SMALLINT    NOT NULL DEFAULT 1,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (requester_id, addressee_id)
);

CREATE INDEX idx_friendships_addressee ON friendships(addressee_id, status);

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

CREATE TABLE message_favorites (
    user_id    BIGINT      NOT NULL REFERENCES users(id),
    message_id BIGINT      NOT NULL REFERENCES messages(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, message_id)
);

CREATE TABLE user_settings (
    user_id              BIGINT      PRIMARY KEY REFERENCES users(id),
    notification_enabled BOOLEAN     NOT NULL DEFAULT TRUE,
    theme                VARCHAR(20) NOT NULL DEFAULT 'light',
    language             VARCHAR(10) NOT NULL DEFAULT 'zh-CN',
    settings_json        JSONB       NOT NULL DEFAULT '{}',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

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
