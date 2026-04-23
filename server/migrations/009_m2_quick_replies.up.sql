-- ============================================================
-- M2-G: quick_replies — per-user preset text snippets. Semantically aligned
-- with Mattermost csesapi/posts.go quickReply: the client reads the list and
-- injects the chosen content into a normal POST /messages call.
-- ============================================================

CREATE TABLE IF NOT EXISTS quick_replies (
    id         BIGSERIAL   PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    label      TEXT        NOT NULL,
    content    TEXT        NOT NULL,
    sort_order INT         NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_quick_replies_user
    ON quick_replies(user_id, sort_order);
