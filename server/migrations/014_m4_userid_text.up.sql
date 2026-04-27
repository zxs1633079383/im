-- ============================================================
-- M4: user-id model overhaul — drop the local users table, switch all
-- user-foreign-key columns from BIGINT to TEXT (24-char Mattermost UUID),
-- and add team_id (TEXT NULL) to channels + messages.
--
-- Strategy: DROP & RECREATE. pre/dev are test environments with no
-- production data to preserve; in-place ALTER for 13 FKs would be three
-- times the work for the same result. prod has not yet been cut over,
-- so there is also no production state to migrate.
--
-- Safety guard: the migration runner refuses to apply this if IM_ENV=prod
-- without --allow-destructive (see server/cmd/migrate/main.go). Locally
-- and in CI we just trust the env gate.
--
-- Field renames / type flips:
--   users                        -> DROP (cookie auth resolves identity from
--                                          Mattermost Redis; im no longer
--                                          maintains a local user record)
--   *.user_id / sender_id /...   -> BIGINT -> TEXT (no FK; mm UUID 24-hex)
--   messages.visible_to          -> BIGINT[] -> TEXT[]
--   channels.team_id             -> NEW (TEXT NULL, source MMUser.CompanyID)
--   messages.team_id             -> NEW (TEXT NULL, denormalised from channels)
-- ============================================================

-- 0. Tear down M1+M2+M3 schema completely. Order matters only because of
--    FK back-pointers; CASCADE cleans the rest.
DROP TRIGGER IF EXISTS trg_user_settings_updated_at ON user_settings;
DROP TRIGGER IF EXISTS trg_friendships_updated_at   ON friendships;
DROP TRIGGER IF EXISTS trg_channels_updated_at      ON channels;
DROP TRIGGER IF EXISTS trg_users_updated_at         ON users;

DROP TABLE IF EXISTS quick_replies                   CASCADE;
DROP TABLE IF EXISTS scheduled_messages              CASCADE;
DROP TABLE IF EXISTS notifications                   CASCADE;
DROP TABLE IF EXISTS approvals                       CASCADE;
DROP TABLE IF EXISTS urgent_confirmations            CASCADE;
DROP TABLE IF EXISTS announcement_acknowledgements   CASCADE;
DROP TABLE IF EXISTS announcements                   CASCADE;
DROP TABLE IF EXISTS channel_pinned_messages         CASCADE;
DROP TABLE IF EXISTS channel_managers                CASCADE;
DROP TABLE IF EXISTS message_favorites               CASCADE;
DROP TABLE IF EXISTS message_attachments             CASCADE;
DROP TABLE IF EXISTS files                           CASCADE;
DROP TABLE IF EXISTS friendships                     CASCADE;
DROP TABLE IF EXISTS user_settings                   CASCADE;
DROP TABLE IF EXISTS messages                        CASCADE;
DROP TABLE IF EXISTS channel_members                 CASCADE;
DROP TABLE IF EXISTS channels                        CASCADE;
DROP TABLE IF EXISTS users                           CASCADE;

-- 1. updated_at trigger function (recreate; CASCADE may have nuked it).
CREATE OR REPLACE FUNCTION update_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- 2. channels — `creator_id TEXT NOT NULL`, plus team_id, plus M2/M3 columns
--    folded in (notice/purpose/picture_url/props/orient/permission/is_top,
--    root_id/root_message_id for topics).
CREATE TABLE channels (
    id              BIGSERIAL    PRIMARY KEY,
    type            SMALLINT     NOT NULL,
    name            VARCHAR(100) NOT NULL DEFAULT '',
    avatar_url      TEXT         NOT NULL DEFAULT '',
    seq             BIGINT       NOT NULL DEFAULT 0,
    creator_id      TEXT         NOT NULL,
    team_id         TEXT         NULL,
    notice          TEXT         NOT NULL DEFAULT '',
    purpose         TEXT         NOT NULL DEFAULT '',
    picture_url     TEXT         NOT NULL DEFAULT '',
    props           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    orient          SMALLINT     NOT NULL DEFAULT 0,
    permission      SMALLINT     NOT NULL DEFAULT 0,
    is_top          BOOLEAN      NOT NULL DEFAULT FALSE,
    root_id         BIGINT       NULL,
    root_message_id BIGINT       NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_channels_root FOREIGN KEY (root_id)
        REFERENCES channels(id) ON DELETE CASCADE
);
CREATE INDEX idx_channels_type    ON channels(type);
CREATE INDEX idx_channels_team    ON channels(team_id) WHERE team_id IS NOT NULL;
CREATE INDEX idx_channels_creator ON channels(creator_id);
CREATE INDEX idx_channels_root_id ON channels(root_id) WHERE root_id IS NOT NULL;
CREATE TRIGGER trg_channels_updated_at
    BEFORE UPDATE ON channels FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- 3. channel_members — composite PK on (user_id, channel_id). user_id TEXT.
CREATE TABLE channel_members (
    user_id          TEXT        NOT NULL,
    channel_id       BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
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

-- 4. messages — sender_id TEXT, team_id TEXT NULL, visible_to TEXT[].
--    All M2 + M3 columns folded in.
CREATE TABLE messages (
    id             BIGSERIAL   PRIMARY KEY,
    channel_id     BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    seq            BIGINT      NOT NULL,
    client_msg_id  VARCHAR(36),
    sender_id      TEXT        NOT NULL,
    team_id        TEXT        NULL,
    msg_type       SMALLINT    NOT NULL DEFAULT 1,
    content        TEXT        NOT NULL DEFAULT '',
    visible_to     TEXT[],
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
CREATE INDEX idx_messages_team                ON messages(team_id) WHERE team_id IS NOT NULL;

-- 5. channels.root_message_id has a forward-FK to messages — add now that
--    both tables exist.
ALTER TABLE channels
    ADD CONSTRAINT fk_channels_root_message
        FOREIGN KEY (root_message_id) REFERENCES messages(id) ON DELETE SET NULL;

-- 6. friendships — surrogate PK + uniq (requester, addressee).
CREATE TABLE friendships (
    id           BIGSERIAL   PRIMARY KEY,
    requester_id TEXT        NOT NULL,
    addressee_id TEXT        NOT NULL,
    status       SMALLINT    NOT NULL DEFAULT 1,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (requester_id, addressee_id)
);
CREATE INDEX idx_friendships_addressee ON friendships(addressee_id, status);
CREATE TRIGGER trg_friendships_updated_at
    BEFORE UPDATE ON friendships FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- 7. files — uploader_id TEXT.
CREATE TABLE files (
    id             BIGSERIAL    PRIMARY KEY,
    uploader_id    TEXT         NOT NULL,
    file_name      VARCHAR(255) NOT NULL,
    file_size      BIGINT       NOT NULL,
    mime_type      VARCHAR(100) NOT NULL DEFAULT '',
    storage_path   TEXT         NOT NULL,
    thumbnail_path TEXT         NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 8. message_attachments — pure join, unchanged shape.
CREATE TABLE message_attachments (
    message_id BIGINT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    file_id    BIGINT NOT NULL REFERENCES files(id)    ON DELETE CASCADE,
    PRIMARY KEY (message_id, file_id)
);

-- 9. message_favorites — user_id TEXT.
CREATE TABLE message_favorites (
    user_id    TEXT        NOT NULL,
    message_id BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, message_id)
);

-- 10. user_settings — user_id PK as TEXT.
CREATE TABLE user_settings (
    user_id              TEXT        PRIMARY KEY,
    notification_enabled BOOLEAN     NOT NULL DEFAULT TRUE,
    theme                VARCHAR(20) NOT NULL DEFAULT 'light',
    language             VARCHAR(10) NOT NULL DEFAULT 'zh-CN',
    settings_json        JSONB       NOT NULL DEFAULT '{}',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE TRIGGER trg_user_settings_updated_at
    BEFORE UPDATE ON user_settings FOR EACH ROW EXECUTE FUNCTION update_updated_at();

-- 11. M2-A: channel_managers / channel_pinned_messages — user_id TEXT, all
--     legacy BIGINT refs intact.
CREATE TABLE channel_managers (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    TEXT        NOT NULL,
    added_by   TEXT        NOT NULL,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, user_id)
);
CREATE INDEX idx_channel_managers_user ON channel_managers(user_id);

CREATE TABLE channel_pinned_messages (
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    message_id BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    pinned_by  TEXT        NOT NULL,
    pinned_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (channel_id, message_id)
);
CREATE INDEX idx_channel_pins_msg ON channel_pinned_messages(message_id);

-- 12. M2-B: announcements + acks. creator_id / user_id TEXT.
CREATE TABLE announcements (
    id         BIGSERIAL   PRIMARY KEY,
    channel_id BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    creator_id TEXT        NOT NULL,
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
    user_id         TEXT        NOT NULL,
    acknowledged_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (announcement_id, user_id)
);

-- 13. M2-C: urgent confirmations.
CREATE TABLE urgent_confirmations (
    message_id   BIGINT      NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id      TEXT        NOT NULL,
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (message_id, user_id)
);

-- 14. M2-D: approvals.
CREATE TABLE approvals (
    id            BIGSERIAL   PRIMARY KEY,
    channel_id    BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    requester_id  TEXT        NOT NULL,
    approver_id   TEXT        NOT NULL,
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

-- 15. M2-E: notifications.
CREATE TABLE notifications (
    id          BIGSERIAL   PRIMARY KEY,
    sender_id   TEXT        NOT NULL,
    receiver_id TEXT        NOT NULL,
    title       TEXT        NOT NULL,
    body        TEXT        NOT NULL DEFAULT '',
    type        SMALLINT    NOT NULL DEFAULT 0,
    read_at     TIMESTAMPTZ,
    props       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_notifications_receiver_unread ON notifications(receiver_id, created_at DESC) WHERE read_at IS NULL;
CREATE INDEX idx_notifications_sender          ON notifications(sender_id, created_at DESC);

-- 16. M2-F: scheduled_messages — sender_id + visible_to TEXT[].
CREATE TABLE scheduled_messages (
    id                   BIGSERIAL   PRIMARY KEY,
    channel_id           BIGINT      NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    sender_id            TEXT        NOT NULL,
    content              TEXT        NOT NULL,
    msg_type             SMALLINT    NOT NULL DEFAULT 1,
    visible_to           TEXT[],
    reply_to             BIGINT,
    file_ids             BIGINT[],
    scheduled_at         TIMESTAMPTZ NOT NULL,
    status               SMALLINT    NOT NULL DEFAULT 0,
    delivered_message_id BIGINT      REFERENCES messages(id) ON DELETE SET NULL,
    error                TEXT,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_scheduled_due    ON scheduled_messages(scheduled_at) WHERE status = 0;
CREATE INDEX idx_scheduled_sender ON scheduled_messages(sender_id, scheduled_at DESC);

-- 17. M2-G: quick_replies.
CREATE TABLE quick_replies (
    id         BIGSERIAL   PRIMARY KEY,
    user_id    TEXT        NOT NULL,
    label      TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_quick_replies_user ON quick_replies(user_id, sort_order);
