-- ============================================================
-- M4 down — restore M3 schema shape (BIGINT FK + users table). The
-- down migration is a sanity-only revert: pre/dev are test environments
-- so we do not preserve data, and prod is not yet cut over.
--
-- After running, the schema looks like the result of migration 013, with
-- the same triggers and indices. team_id columns are dropped; visible_to
-- reverts to BIGINT[]; users + mm_user_id shadow column come back.
-- ============================================================

DROP TABLE IF EXISTS quick_replies                   CASCADE;
DROP TABLE IF EXISTS scheduled_messages              CASCADE;
DROP TABLE IF EXISTS notifications                   CASCADE;
DROP TABLE IF EXISTS approvals                       CASCADE;
DROP TABLE IF EXISTS urgent_confirmations            CASCADE;
DROP TABLE IF EXISTS announcement_acknowledgements   CASCADE;
DROP TABLE IF EXISTS announcements                   CASCADE;
DROP TABLE IF EXISTS channel_pinned_messages         CASCADE;
DROP TABLE IF EXISTS channel_managers                CASCADE;
DROP TABLE IF EXISTS user_settings                   CASCADE;
DROP TABLE IF EXISTS message_favorites               CASCADE;
DROP TABLE IF EXISTS message_attachments             CASCADE;
DROP TABLE IF EXISTS files                           CASCADE;
DROP TABLE IF EXISTS friendships                     CASCADE;
DROP TABLE IF EXISTS messages                        CASCADE;
DROP TABLE IF EXISTS channel_members                 CASCADE;
DROP TABLE IF EXISTS channels                        CASCADE;

-- Recreate users table (M1 + M3 mm_user_id shadow column).
CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(50)  UNIQUE NOT NULL,
    email         VARCHAR(255) UNIQUE NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    display_name  VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url    TEXT         NOT NULL DEFAULT '',
    status        SMALLINT     NOT NULL DEFAULT 1,
    mm_user_id    TEXT,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX idx_users_mm_user_id ON users(mm_user_id) WHERE mm_user_id IS NOT NULL;

CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON users FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- channels (BIGINT creator_id, no team_id, M2/M3 columns intact).
CREATE TABLE channels (
    id              BIGSERIAL    PRIMARY KEY,
    type            SMALLINT     NOT NULL,
    name            VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url      TEXT         NOT NULL DEFAULT '',
    seq             BIGINT       NOT NULL DEFAULT 0,
    creator_id      BIGINT       REFERENCES users(id),
    notice          TEXT         NOT NULL DEFAULT '',
    purpose         TEXT         NOT NULL DEFAULT '',
    picture_url     TEXT         NOT NULL DEFAULT '',
    props           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    orient          SMALLINT     NOT NULL DEFAULT 0,
    permission      SMALLINT     NOT NULL DEFAULT 0,
    is_top          BOOLEAN      NOT NULL DEFAULT FALSE,
    root_id         BIGINT       REFERENCES channels(id) ON DELETE CASCADE,
    root_message_id BIGINT,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_channels_type    ON channels(type);
CREATE INDEX idx_channels_root_id ON channels(root_id) WHERE root_id IS NOT NULL;
CREATE TRIGGER trg_channels_updated_at
    BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TABLE channel_members (
    user_id          BIGINT      NOT NULL REFERENCES users(id),
    channel_id       BIGINT      NOT NULL REFERENCES channels(id),
    role             SMALLINT    NOT NULL DEFAULT 1,
    last_read_seq    BIGINT      NOT NULL DEFAULT 0,
    phantom_count    BIGINT      NOT NULL DEFAULT 0,
    phantom_at_read  BIGINT      NOT NULL DEFAULT 0,
    notify_pref      SMALLINT    NOT NULL DEFAULT 0,
    joined_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, channel_id)
);
CREATE INDEX idx_channel_members_channel             ON channel_members(channel_id);
CREATE INDEX idx_channel_members_chid_lastreadseq    ON channel_members(channel_id, last_read_seq);
CREATE INDEX idx_channel_members_channel_user        ON channel_members(channel_id, user_id);

CREATE TABLE messages (
    id             BIGSERIAL   PRIMARY KEY,
    channel_id     BIGINT      NOT NULL REFERENCES channels(id),
    seq            BIGINT      NOT NULL,
    client_msg_id  VARCHAR(36),
    sender_id      BIGINT      NOT NULL REFERENCES users(id),
    msg_type       SMALLINT    NOT NULL DEFAULT 1,
    content        TEXT        NOT NULL DEFAULT '',
    visible_to     BIGINT[],
    reply_to       BIGINT,
    forwarded_from BIGINT,
    is_urgent      BOOLEAN     NOT NULL DEFAULT FALSE,
    props          JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ,
    deleted        BOOLEAN     NOT NULL DEFAULT FALSE,
    deleted_at     TIMESTAMPTZ,
    UNIQUE (channel_id, seq),
    UNIQUE (channel_id, client_msg_id)
);
CREATE INDEX idx_messages_channel_seq         ON messages(channel_id, seq);
CREATE INDEX idx_messages_channel_created     ON messages(channel_id, created_at);
CREATE INDEX idx_messages_sender              ON messages(sender_id, created_at);
CREATE INDEX idx_messages_reply_to            ON messages(reply_to) WHERE reply_to IS NOT NULL;
CREATE INDEX idx_messages_content_search      ON messages USING gin(to_tsvector('simple', content));
CREATE INDEX idx_messages_urgent              ON messages(channel_id, is_urgent) WHERE is_urgent = TRUE;

ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root_message
        FOREIGN KEY (root_message_id) REFERENCES messages(id) ON DELETE SET NULL;

CREATE TABLE friendships (
    id           BIGSERIAL   PRIMARY KEY,
    requester_id BIGINT      NOT NULL REFERENCES users(id),
    addressee_id BIGINT      NOT NULL REFERENCES users(id),
    status       SMALLINT    NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (requester_id, addressee_id)
);
CREATE INDEX idx_friendships_addressee ON friendships(addressee_id, status);
CREATE TRIGGER trg_friendships_updated_at
    BEFORE UPDATE ON friendships FOR EACH ROW EXECUTE FUNCTION update_updated_at();

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
CREATE TRIGGER trg_user_settings_updated_at
    BEFORE UPDATE ON user_settings FOR EACH ROW EXECUTE FUNCTION update_updated_at();

CREATE TABLE channel_managers (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    BIGINT      NOT NULL,
    added_by   BIGINT      NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX idx_channel_managers_user ON channel_managers(user_id);

CREATE TABLE channel_pinned_messages (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    pinned_by  BIGINT      NOT NULL,
    pinned_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, message_id)
);
CREATE INDEX idx_channel_pins_msg ON channel_pinned_messages(message_id);

CREATE TABLE announcements (
    id         BIGSERIAL   PRIMARY KEY,
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    creator_id BIGINT      NOT NULL REFERENCES users(id),
    title      TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    props      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    deleted    BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_announcements_channel ON announcements(channel_id, created_at DESC);

CREATE TABLE announcement_acknowledgements (
    announcement_id BIGINT      NOT NULL REFERENCES announcements(id) ON DELETE CASCADE,
    user_id         BIGINT      NOT NULL REFERENCES users(id),
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (announcement_id, user_id)
);

CREATE TABLE urgent_confirmations (
    message_id   BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id      BIGINT      NOT NULL REFERENCES users(id),
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, user_id)
);

CREATE TABLE approvals (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    requester_id  BIGINT      NOT NULL,
    approver_id   BIGINT      NOT NULL,
    subject       TEXT        NOT NULL,
    content       TEXT        NOT NULL,
    props         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status        SMALLINT    NOT NULL DEFAULT 0,
    decided_at    TIMESTAMPTZ,
    decision_note TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_approvals_approver_pending ON approvals(approver_id, status) WHERE status = 0;
CREATE INDEX idx_approvals_requester        ON approvals(requester_id, created_at DESC);
CREATE INDEX idx_approvals_channel          ON approvals(channel_id, created_at DESC);

CREATE TABLE notifications (
    id          BIGSERIAL   PRIMARY KEY,
    sender_id   BIGINT      NOT NULL,
    receiver_id BIGINT      NOT NULL,
    title       TEXT        NOT NULL,
    body        TEXT        NOT NULL DEFAULT '',
    type        SMALLINT    NOT NULL DEFAULT 0,
    read_at     TIMESTAMPTZ,
    props       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_notifications_receiver_unread ON notifications(receiver_id, created_at DESC) WHERE read_at IS NULL;
CREATE INDEX idx_notifications_sender          ON notifications(sender_id, created_at DESC);

CREATE TABLE scheduled_messages (
    id                   BIGSERIAL   PRIMARY KEY,
    channel_id           BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_id            BIGINT      NOT NULL,
    content              TEXT        NOT NULL,
    msg_type             SMALLINT    NOT NULL DEFAULT 1,
    visible_to           BIGINT[],
    reply_to             BIGINT,
    file_ids             BIGINT[],
    scheduled_at         TIMESTAMPTZ NOT NULL,
    status               SMALLINT    NOT NULL DEFAULT 0,
    delivered_message_id BIGINT      REFERENCES messages(id),
    error                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_scheduled_due    ON scheduled_messages(scheduled_at) WHERE status = 0;
CREATE INDEX idx_scheduled_sender ON scheduled_messages(sender_id, scheduled_at DESC);

CREATE TABLE quick_replies (
    id         BIGSERIAL   PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    label      TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_quick_replies_user ON quick_replies(user_id, sort_order);
