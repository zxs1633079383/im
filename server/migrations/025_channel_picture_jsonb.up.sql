-- M5: channels 表恢复 picture (JSONB) + picture_type (VARCHAR(15))
-- 支持 cses-avatar 三态渲染：USER / NAME / PICTURE
-- picture 存储规则：
--   USER  → {"userIds": ["<uid1>", "<uid2>"]}
--   NAME  → {"color": "#RRGGBB", "text": "<首字符>"}
--   PICTURE → {"url": "<url>"}
-- 镜像 cses-client harness C007

ALTER TABLE channels
    ADD COLUMN IF NOT EXISTS picture      JSONB        NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS picture_type VARCHAR(15)  NOT NULL DEFAULT '';

-- 历史数据回填
-- 1. 有 picture_url → PICTURE 优先级最高
UPDATE channels
   SET picture_type = 'PICTURE',
       picture      = jsonb_build_object('url', picture_url)
 WHERE picture_url <> ''
   AND picture_type = '';

-- 2. type=1 (DM) 且无 picture_url → USER
UPDATE channels
   SET picture_type = 'USER',
       picture      = jsonb_build_object('userIds', ARRAY[creator_id])
 WHERE type = 1
   AND picture_type = '';

-- 3. 其余（群聊等）→ NAME，用 name 首字符 + 固定色块
UPDATE channels
   SET picture_type = 'NAME',
       picture      = jsonb_build_object(
                          'color', '#5B9BFF',
                          'text',  LEFT(CASE WHEN name = '' THEN '#' ELSE name END, 1)
                      )
 WHERE picture_type = '';
