-- M3: shadow mapping mm_user_id (Mattermost UUID, 24-char hex) → im users.id (int64).
-- Lazy upsert by MattermostCookieAuth so cookie-only callers can reach handlers
-- without first registering an im-native account. NULL for accounts created
-- via /api/auth/register (JWT-native users).
ALTER TABLE users
    ADD COLUMN mm_user_id TEXT;

CREATE UNIQUE INDEX idx_users_mm_user_id
    ON users(mm_user_id)
    WHERE mm_user_id IS NOT NULL;
