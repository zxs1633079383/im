-- rollback M5: 删除 picture / picture_type 两列
ALTER TABLE channels
    DROP COLUMN IF EXISTS picture,
    DROP COLUMN IF EXISTS picture_type;
