DROP INDEX IF EXISTS idx_users_mm_user_id;
ALTER TABLE users
    DROP COLUMN IF EXISTS mm_user_id;
